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

// sfztools' own official macOS release (sfizz-<ver>-macos.tar.gz) is
// x86_64-only — their CI still targets macos-11, from before GitHub had
// Apple Silicon runners, and was never updated. A native arm64 process
// cannot dlopen a foreign-architecture dylib (Rosetta 2 translates whole
// *processes* at launch, not a library loaded into an already-running
// native one), so that tarball is useless on the Apple Silicon Macs most
// people actually have today.
//
// polyclav therefore builds its own arm64 libsfizz.dylib
// (.github/workflows/build-sfizz-macos.yml, on a real macos-15
// Apple-Silicon runner, from sfztools/sfizz's vendored-submodule release
// tree — the same tree their own tagged-release CI job builds, just on
// newer hardware) and publishes it as a polyclav GitHub Release asset.
// audio-core/src/sfizz_sys.rs looks for it at a fixed path under the
// user's data dir; this file is what puts it there.
const (
	sfizzBuildTag   = "sfizz-macos-1.2.3"
	sfizzAssetName  = "libsfizz.dylib"
	sfizzAssetURL   = "https://github.com/mschulkind-oss/polyclav/releases/download/" + sfizzBuildTag + "/" + sfizzAssetName
	sfizzLicenseURL = "https://github.com/sfztools/sfizz/blob/1.2.3/LICENSE.md" // BSD-2-Clause
)

// SfizzLibOptions controls InstallSfizzMacOS. A separate, smaller shape
// than Options (the soundfont-pack pipeline): there is exactly one file,
// no archive to unpack, and no license-consent prompt needed — it's
// polyclav's own CI-built artifact of a BSD-2-Clause library, not
// third-party copyrighted content the user must explicitly accept.
type SfizzLibOptions struct {
	// Dest is the directory libsfizz.dylib is placed in. Production
	// default: ~/.local/share/polyclav/lib — matches the fixed path
	// audio-core/src/sfizz_sys.rs's macOS search list checks first.
	// Tests pass a t.TempDir().
	Dest string

	// URL overrides sfizzAssetURL; tests point this at an
	// httptest.NewServer.
	URL string

	// SkipExisting, when true (the default), leaves an existing file at
	// Dest/libsfizz.dylib alone rather than re-downloading.
	SkipExisting bool

	HTTPClient *http.Client
	Stdout     io.Writer
}

// InstallSfizzMacOS downloads polyclav's own prebuilt arm64 libsfizz.dylib
// to opts.Dest. A no-op (returns nil immediately, no network access) on
// every OS other than macOS.
func InstallSfizzMacOS(ctx context.Context, opts SfizzLibOptions) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return installSfizzLib(ctx, opts)
}

// installSfizzLib is InstallSfizzMacOS's actual logic, split out so tests
// can exercise the download/skip/error paths deterministically on any
// host OS — the public entry point's GOOS gate would otherwise make this
// completely untestable from Linux CI.
func installSfizzLib(ctx context.Context, opts SfizzLibOptions) error {
	if opts.URL == "" {
		opts.URL = sfizzAssetURL
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Dest == "" {
		return fmt.Errorf("bootstrap: sfizz lib Dest is required")
	}

	final := filepath.Join(opts.Dest, sfizzAssetName)
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
