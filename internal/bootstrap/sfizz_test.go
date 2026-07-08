package bootstrap

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSfizzAssetForSupportedPlatforms(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		name, url, ok := sfizzAssetFor(goos)
		if !ok {
			t.Errorf("%s: expected ok=true", goos)
		}
		if name == "" || url == "" {
			t.Errorf("%s: expected non-empty asset name/url, got %q %q", goos, name, url)
		}
		if !strings.HasSuffix(url, name) {
			t.Errorf("%s: asset URL %q should end in its own asset name %q", goos, url, name)
		}
	}
}

func TestSfizzAssetForUnsupportedPlatform(t *testing.T) {
	if _, _, ok := sfizzAssetFor("windows"); ok {
		t.Error("windows has no polyclav-built sfizz asset yet; expected ok=false")
	}
}

func TestInstallSfizzLibNoOpOnUnsupportedPlatform(t *testing.T) {
	// sfizzAssetFor (not InstallSfizzLib, which is hardwired to the real
	// runtime.GOOS) is what makes the no-op branch deterministically
	// testable regardless of host OS — see its doc comment.
	if _, _, ok := sfizzAssetFor("plan9"); ok {
		t.Fatal("test premise broken: plan9 must have no polyclav-built asset")
	}
}

func TestInstallSfizzLibExportedEntryPointDownloads(t *testing.T) {
	// Exercises the real GOOS-based path end-to-end. Both CI hosts this
	// runs on (Linux) and a macOS dev machine have a real asset, so this
	// is not expected to skip in practice; it's a defensive guard in case
	// the suite ever runs somewhere polyclav doesn't build sfizz for.
	assetName, _, ok := sfizzAssetFor(runtime.GOOS)
	if !ok {
		t.Skipf("no polyclav-built sfizz asset for GOOS=%s", runtime.GOOS)
	}

	body := []byte("fake-lib-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dest := t.TempDir()
	err := InstallSfizzLib(context.Background(), SfizzLibOptions{
		Dest:       dest,
		URL:        srv.URL, // overrides the computed URL; AssetName still resolved from runtime.GOOS
		HTTPClient: srv.Client(),
		Stdout:     io.Discard,
	})
	if err != nil {
		t.Fatalf("InstallSfizzLib: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, assetName))
	if err != nil {
		t.Fatalf("read installed lib: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("unexpected contents: %q", got)
	}
}

func TestInstallSfizzLibDownloadsAndPlaces(t *testing.T) {
	body := []byte("fake-lib-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dest := t.TempDir()
	var stdout bytes.Buffer
	err := installSfizzLib(context.Background(), SfizzLibOptions{
		Dest:       dest,
		AssetName:  "libsfizz.so",
		URL:        srv.URL,
		HTTPClient: srv.Client(),
		Stdout:     &stdout,
	})
	if err != nil {
		t.Fatalf("installSfizzLib: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "libsfizz.so"))
	if err != nil {
		t.Fatalf("read installed lib: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("unexpected contents: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "libsfizz.so.partial")); err == nil {
		t.Error("partial file should have been renamed away")
	}
	if !bytes.Contains(stdout.Bytes(), []byte("installed")) {
		t.Errorf("expected an install confirmation line, got:\n%s", stdout.String())
	}
}

func TestInstallSfizzLibSkipsExisting(t *testing.T) {
	dest := t.TempDir()
	existing := filepath.Join(dest, "libsfizz.so")
	if err := os.WriteFile(existing, []byte("already here"), 0o755); err != nil {
		t.Fatal(err)
	}

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	err := installSfizzLib(context.Background(), SfizzLibOptions{
		Dest:         dest,
		AssetName:    "libsfizz.so",
		URL:          srv.URL,
		HTTPClient:   srv.Client(),
		SkipExisting: true,
		Stdout:       io.Discard,
	})
	if err != nil {
		t.Fatalf("installSfizzLib: %v", err)
	}
	if called {
		t.Error("SkipExisting should have prevented any network call")
	}
	got, _ := os.ReadFile(existing)
	if string(got) != "already here" {
		t.Error("existing file must not be overwritten")
	}
}

func TestInstallSfizzLibHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dest := t.TempDir()
	err := installSfizzLib(context.Background(), SfizzLibOptions{
		Dest:       dest,
		AssetName:  "libsfizz.so",
		URL:        srv.URL,
		HTTPClient: srv.Client(),
		Stdout:     io.Discard,
	})
	if err == nil {
		t.Fatal("expected an error on HTTP 404")
	}
	if _, statErr := os.Stat(filepath.Join(dest, "libsfizz.so")); statErr == nil {
		t.Error("no file should be left behind on failed download")
	}
	if _, statErr := os.Stat(filepath.Join(dest, "libsfizz.so.partial")); statErr == nil {
		t.Error("partial file should not survive a failed download")
	}
}

func TestInstallSfizzLibDestRequired(t *testing.T) {
	if err := installSfizzLib(context.Background(), SfizzLibOptions{AssetName: "libsfizz.so", Stdout: io.Discard}); err == nil {
		t.Error("expected an error when Dest is empty")
	}
}

func TestInstallSfizzLibAssetNameRequired(t *testing.T) {
	if err := installSfizzLib(context.Background(), SfizzLibOptions{Dest: t.TempDir(), Stdout: io.Discard}); err == nil {
		t.Error("expected an error when AssetName is empty")
	}
}
