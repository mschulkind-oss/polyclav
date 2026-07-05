package web

import (
	_ "embed"
	"net/http"
)

// indexHTML is the INTERIM dashboard: a single hand-written vanilla
// HTML+CSS+JS page with zero external requests. The real frontend per
// docs/WEB_UI.md is a Next.js static export embedded from web/out/ —
// when that lands, this file and its embed are replaced wholesale.
//
//go:embed static/index.html
var indexHTML []byte

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(indexHTML)
}
