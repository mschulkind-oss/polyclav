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
	"testing"
)

func TestInstallSfizzMacOSNoOpOnOtherPlatforms(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("this test specifically checks the non-darwin no-op path")
	}
	dest := t.TempDir()
	err := InstallSfizzMacOS(context.Background(), SfizzLibOptions{
		Dest: dest,
		URL:  "http://127.0.0.1:1/unreachable", // would fail loudly if ever actually fetched
	})
	if err != nil {
		t.Fatalf("expected a silent no-op on %s, got: %v", runtime.GOOS, err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, sfizzAssetName)); statErr == nil {
		t.Error("no-op path must not have written a file")
	}
}

func TestInstallSfizzLibDownloadsAndPlaces(t *testing.T) {
	body := []byte("fake-dylib-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dest := t.TempDir()
	var stdout bytes.Buffer
	err := installSfizzLib(context.Background(), SfizzLibOptions{
		Dest:       dest,
		URL:        srv.URL,
		HTTPClient: srv.Client(),
		Stdout:     &stdout,
	})
	if err != nil {
		t.Fatalf("installSfizzLib: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, sfizzAssetName))
	if err != nil {
		t.Fatalf("read installed lib: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("unexpected contents: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dest, sfizzAssetName+".partial")); err == nil {
		t.Error("partial file should have been renamed away")
	}
	if !bytes.Contains(stdout.Bytes(), []byte("installed")) {
		t.Errorf("expected an install confirmation line, got:\n%s", stdout.String())
	}
}

func TestInstallSfizzLibSkipsExisting(t *testing.T) {
	dest := t.TempDir()
	existing := filepath.Join(dest, sfizzAssetName)
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
		URL:        srv.URL,
		HTTPClient: srv.Client(),
		Stdout:     io.Discard,
	})
	if err == nil {
		t.Fatal("expected an error on HTTP 404")
	}
	if _, statErr := os.Stat(filepath.Join(dest, sfizzAssetName)); statErr == nil {
		t.Error("no file should be left behind on failed download")
	}
	if _, statErr := os.Stat(filepath.Join(dest, sfizzAssetName+".partial")); statErr == nil {
		t.Error("partial file should not survive a failed download")
	}
}

func TestInstallSfizzLibDestRequired(t *testing.T) {
	if err := installSfizzLib(context.Background(), SfizzLibOptions{Stdout: io.Discard}); err == nil {
		t.Error("expected an error when Dest is empty")
	}
}
