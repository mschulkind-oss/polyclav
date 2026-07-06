package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// Static pages served by the daemon (docs/WEB_UI.md):
//
//   - GET /       → 302 to /app/ when the Next.js export is embedded (the
//     normal case: the export is committed under static/app/); if the
//     tree was somehow built without an export, / falls back to the
//     interim page so the daemon is never UI-less.
//   - GET /app/*  → the Next.js static export from web/, refreshed by
//     `just web-build` (pnpm build + copy into static/app/) and COMMITTED,
//     so `go build` needs no node toolchain.
//   - GET /legacy → the interim single-file vanilla dashboard
//     (static/index.html), kept until the Next app fully replaces it.
//
// routes() in server.go calls routesStatic() to register /app/ and
// /legacy; the GET /{$} handler (handleIndex) lives here too.

// indexHTML is the INTERIM dashboard: a single hand-written vanilla
// HTML+CSS+JS page with zero external requests.
//
//go:embed static/index.html
var indexHTML []byte

// appExport embeds the Next.js static export. The `all:` prefix keeps
// _next/ and the __next.*.txt RSC payloads — plain go:embed patterns
// skip _-prefixed names.
//
//go:embed all:static/app
var appExport embed.FS

// appFS is appExport rooted at the export directory, so file names match
// URL paths under /app/.
var appFS = func() fs.FS {
	sub, err := fs.Sub(appExport, "static/app")
	if err != nil {
		panic("web: static/app not embedded: " + err.Error()) // unreachable: embed guarantees the dir
	}
	return sub
}()

// appPresent reports whether a built export is actually in the embed —
// static/app/ could in principle be emptied in a checkout; the daemon
// then keeps serving the interim page at / instead of redirecting into
// a 404.
var appPresent = func() bool {
	_, err := fs.Stat(appFS, "index.html")
	return err == nil
}()

// routesStatic registers the static-page routes owned by this file.
// Called from routes() in server.go.
func (s *Server) routesStatic() {
	s.mux.HandleFunc("GET /legacy", s.handleLegacy)
	// Subtree pattern: also gives the /app → /app/ redirect for free.
	s.mux.HandleFunc("GET /app/", s.handleApp)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if appPresent {
		http.Redirect(w, r, "/app/", http.StatusFound)
		return
	}
	s.serveInterim(w)
}

func (s *Server) handleLegacy(w http.ResponseWriter, _ *http.Request) {
	s.serveInterim(w)
}

func (s *Server) serveInterim(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(indexHTML)
}

// handleApp serves the embedded Next.js export. The export uses
// trailingSlash, so routes are directories with an index.html; a
// slashless page URL still resolves via the dir/index.html retry. The
// export's own 404 page backs unknown paths.
func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	if !appPresent {
		writeErr(w, http.StatusNotFound, "web app not embedded — run `just web-build` and rebuild")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/app/")
	if name == "" || strings.HasSuffix(name, "/") {
		name += "index.html"
	}
	name = path.Clean(name)

	data, err := fs.ReadFile(appFS, name)
	if err != nil && !strings.Contains(path.Base(name), ".") {
		// Page route without the trailing slash (e.g. /app/foo).
		if d, err2 := fs.ReadFile(appFS, name+"/index.html"); err2 == nil {
			name, data, err = name+"/index.html", d, nil
		}
	}
	if err != nil {
		if nf, err2 := fs.ReadFile(appFS, "404.html"); err2 == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(nf)
			return
		}
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", contentTypeFor(name))
	if strings.HasPrefix(name, "_next/static/") {
		// Content-hashed asset names: safe to cache forever.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	_, _ = w.Write(data)
}

// extTypes pins content types for everything a Next export actually
// contains, so serving doesn't depend on the host's mime database.
var extTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".txt":   "text/plain; charset=utf-8",
	".svg":   "image/svg+xml",
	".ico":   "image/x-icon",
	".png":   "image/png",
	".woff2": "font/woff2",
}

func contentTypeFor(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if t, ok := extTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}
