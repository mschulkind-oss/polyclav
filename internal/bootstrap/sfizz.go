package bootstrap

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Neither macOS nor Linux gets a usable prebuilt libsfizz from upstream:
//
//   - sfztools' own official macOS release (sfizz-<ver>-macos.tar.gz) is
//     x86_64-only — their CI still targets macos-11, from before GitHub had
//     Apple Silicon runners — and a native arm64 process cannot dlopen a
//     foreign-architecture dylib (Rosetta 2 translates whole *processes* at
//     launch, not a library loaded into an already-running native one).
//   - Linux is worse: sfztools' GitHub releases ship no Linux binary at
//     all, only macOS and Windows bundles.
//
// polyclav therefore builds its own libsfizz for each platform
// (.github/workflows/build-sfizz-macos.yml, build-sfizz-linux.yml — from
// sfztools/sfizz's vendored-submodule release tree, the same tree their own
// tagged-release CI job builds) and publishes it as a polyclav GitHub
// Release asset, one release tag per OS (sfizz-macos-<ver>,
// sfizz-linux-<ver>, since the two are built and verified independently).
// audio-core/src/sfizz_sys.rs looks for the result at a fixed path under
// the user's data dir on both platforms; this file is what puts it there.
const sfizzVersion = "1.2.3"

const sfizzLicenseURL = "https://github.com/sfztools/sfizz/blob/" + sfizzVersion + "/LICENSE" // BSD-2-Clause

// sfizzAssetFor returns the bootstrap-installed filename and download URL
// for goos's prebuilt libsfizz, or ok=false if polyclav doesn't build one
// for that platform yet.
func sfizzAssetFor(goos string) (assetName, url string, ok bool) {
	switch goos {
	case "darwin":
		return "libsfizz.dylib", "https://github.com/mschulkind-oss/polyclav/releases/download/sfizz-macos-" + sfizzVersion + "/libsfizz.dylib", true
	case "linux":
		return "libsfizz.so", "https://github.com/mschulkind-oss/polyclav/releases/download/sfizz-linux-" + sfizzVersion + "/libsfizz.so", true
	default:
		return "", "", false
	}
}

// SfizzLibOptions controls InstallSfizzLib. A separate, smaller shape than
// Options (the soundfont-pack pipeline): there is exactly one file, no
// archive to unpack, and no license-consent prompt needed — it's
// polyclav's own CI-built artifact of a BSD-2-Clause library, not
// third-party copyrighted content the user must explicitly accept.
type SfizzLibOptions struct {
	// Dest is the directory the lib is placed in. Production default:
	// ~/.local/share/polyclav/lib — matches the fixed path
	// audio-core/src/sfizz_sys.rs checks first on both macOS and Linux.
	Dest string

	// URL overrides the platform-computed asset URL; tests point this at
	// an httptest.NewServer.
	URL string

	// AssetName overrides the platform-computed destination filename;
	// tests pin this alongside URL so they don't depend on GOOS.
	AssetName string

	// SkipExisting, when true (the default), leaves an existing file at
	// Dest/<asset name> alone rather than re-downloading.
	SkipExisting bool

	HTTPClient *http.Client
	Stdout     io.Writer
}

// InstallSfizzLib downloads polyclav's own prebuilt libsfizz to opts.Dest
// for the current GOOS. A no-op (returns nil immediately, no network
// access) on any platform polyclav doesn't build one for yet.
func InstallSfizzLib(ctx context.Context, opts SfizzLibOptions) error {
	assetName, url, ok := sfizzAssetFor(runtime.GOOS)
	if !ok {
		return nil
	}
	if opts.AssetName == "" {
		opts.AssetName = assetName
	}
	if opts.URL == "" {
		opts.URL = url
	}
	return installSfizzLib(ctx, opts)
}

// installSfizzLib is InstallSfizzLib's actual logic, split out so tests
// can exercise the download/skip/error paths deterministically on any
// host OS — the public entry point's GOOS gate would otherwise make this
// completely untestable on a platform polyclav doesn't build for.
func installSfizzLib(ctx context.Context, opts SfizzLibOptions) error {
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Dest == "" {
		return fmt.Errorf("bootstrap: sfizz lib Dest is required")
	}
	if opts.AssetName == "" {
		return fmt.Errorf("bootstrap: sfizz lib AssetName is required")
	}
	if opts.URL == "" {
		return fmt.Errorf("bootstrap: sfizz lib URL is required")
	}

	final := filepath.Join(opts.Dest, opts.AssetName)
	if opts.SkipExisting {
		if _, err := os.Stat(final); err == nil {
			fmt.Fprintf(opts.Stdout, "sfizz: already present at %s\n", final)
			return nil
		}
	}

	if err := os.MkdirAll(opts.Dest, 0o755); err != nil {
		return fmt.Errorf("bootstrap: mkdir %q: %w", opts.Dest, err)
	}

	fmt.Fprintf(opts.Stdout, "sfizz: downloading %s\n", opts.URL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.URL, nil)
	if err != nil {
		return fmt.Errorf("bootstrap: sfizz request: %w", err)
	}
	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("bootstrap: sfizz download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bootstrap: sfizz download: GET %s: HTTP %d", opts.URL, resp.StatusCode)
	}

	partial := final + ".partial"
	out, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("bootstrap: sfizz: open %q: %w", partial, err)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		_ = os.Remove(partial)
		return fmt.Errorf("bootstrap: sfizz: download body: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("bootstrap: sfizz: close %q: %w", partial, err)
	}
	if err := os.Rename(partial, final); err != nil {
		return fmt.Errorf("bootstrap: sfizz: rename %q -> %q: %w", partial, final, err)
	}

	fmt.Fprintf(opts.Stdout, "sfizz: installed %s (%s, BSD-2-Clause)\n", final, sfizzLicenseURL)
	return nil
}
