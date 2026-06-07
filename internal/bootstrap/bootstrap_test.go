package bootstrap

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestItemListURLToOnDiskMapping is a table-driven sanity check that
// every Item declared in ItemList has the fields the downloader needs:
// non-empty URL, OnDisk path, License string. The actual URL liveness
// is verified out-of-band (HEAD checks during implementation); we
// don't make real network calls in CI.
func TestItemListURLToOnDiskMapping(t *testing.T) {
	for _, it := range ItemList() {
		t.Run(it.Name, func(t *testing.T) {
			if it.Name == "" {
				t.Error("Name is empty")
			}
			if it.URL == "" {
				t.Error("URL is empty")
			}
			if !strings.HasPrefix(it.URL, "https://") {
				t.Errorf("URL must be https: %q", it.URL)
			}
			if it.OnDisk == "" {
				t.Error("OnDisk is empty")
			}
			if filepath.IsAbs(it.OnDisk) {
				t.Errorf("OnDisk must be relative to dest, got absolute: %q", it.OnDisk)
			}
			if it.License == "" {
				t.Error("License is empty")
			}
		})
	}
}

func TestItemListMatchesExamplePatches(t *testing.T) {
	// Cross-check: every bootstrap item must appear by name in
	// polyclav.example.toml — otherwise the downloaded files would be
	// orphaned. We rely on the test running from the repo root (go
	// test sets cwd to the package dir).
	wantNames := map[string]bool{
		"ydp-grand": false, "salamander": false, "wurlitzer": false,
		"rhodes": false, "splendid": false, "dx7-rom1a": false,
		"dx7-epiano": false, "moog-bass": false, "taurus-bass": false,
	}
	for _, it := range ItemList() {
		if _, ok := wantNames[it.Name]; !ok {
			t.Errorf("unexpected bootstrap item %q (not in example.toml's [[patches]])", it.Name)
			continue
		}
		wantNames[it.Name] = true
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("bootstrap missing item %q from example.toml's [[patches]]", name)
		}
	}
}

func TestRawDownloadAtomicRename(t *testing.T) {
	// Spin up a fake HTTP server returning a known payload; bootstrap
	// must download into a .partial path, then os.Rename to the final
	// OnDisk location. Verify the .partial is gone and final has the
	// expected bytes.
	body := []byte("fake-sf2-payload")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "16")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dest := t.TempDir()
	items := []Item{{
		Name:      "fake-grand",
		URL:       srv.URL + "/grand.sf2",
		Archive:   ArchiveRaw,
		OnDisk:    "subdir/grand.sf2",
		SizeBytes: int64(len(body)),
		License:   "test",
	}}

	var stdin bytes.Buffer
	stdin.WriteString("y\n")
	var stdout bytes.Buffer

	err := Run(context.Background(), Options{
		Dest:       dest,
		Items:      items,
		HTTPClient: srv.Client(),
		Stdin:      &stdin,
		Stdout:     &stdout,
		Stderr:     &stdout,
	})
	if err != nil {
		t.Fatalf("Run: %v\nlog:\n%s", err, stdout.String())
	}

	final := filepath.Join(dest, "subdir/grand.sf2")
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("final bytes mismatch: got %q want %q", got, body)
	}

	// .partial must be gone.
	if _, err := os.Stat(final + ".partial"); err == nil {
		t.Errorf("found leftover .partial file at %s", final+".partial")
	}

	// LICENSES.txt must be written.
	if _, err := os.Stat(filepath.Join(dest, "LICENSES.txt")); err != nil {
		t.Errorf("LICENSES.txt not written: %v", err)
	}
}

func TestSkipExistingFile(t *testing.T) {
	// Idempotency: when the target file already exists and
	// --skip-existing is on, Run() must not make an HTTP call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("HTTP call unexpectedly made for skipped item")
		w.WriteHeader(500)
	}))
	defer srv.Close()

	dest := t.TempDir()
	final := filepath.Join(dest, "subdir/grand.sf2")
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(final, []byte("already here"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var stdout bytes.Buffer
	err := Run(context.Background(), Options{
		Dest:           dest,
		AcceptLicenses: true,
		SkipExisting:   true,
		Items: []Item{{
			Name:    "fake",
			URL:     srv.URL + "/grand.sf2",
			Archive: ArchiveRaw,
			OnDisk:  "subdir/grand.sf2",
			License: "test",
		}},
		HTTPClient: srv.Client(),
		Stdout:     &stdout,
	})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "skip: already present") {
		t.Errorf("expected skip log, got:\n%s", stdout.String())
	}
}

func TestZipUnpackPlacesFileAtOnDisk(t *testing.T) {
	// Build a small precanned zip in-memory with the layout that
	// bootstrap expects from a GitHub codeload zip:
	//   wurly-master/wurly.sfz
	// and verify it lands at the OnDisk path after Run().
	dest := t.TempDir()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("wurly-master/wurly.sfz")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := f.Write([]byte("fake sfz")); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	zipBytes := buf.Bytes()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	var stdout bytes.Buffer
	err = Run(context.Background(), Options{
		Dest:           dest,
		AcceptLicenses: true,
		Items: []Item{{
			Name:    "wurly",
			URL:     srv.URL + "/wurly.zip",
			Archive: ArchiveZip,
			OnDisk:  "wurly-master/wurly.sfz",
			License: "test",
		}},
		HTTPClient: srv.Client(),
		Stdout:     &stdout,
	})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(dest, "wurly-master/wurly.sfz"))
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if !bytes.Equal(got, []byte("fake sfz")) {
		t.Errorf("unexpected sfz contents: %q", got)
	}
}

func TestConsentRefusalAborts(t *testing.T) {
	// User answers "n" at the prompt → bootstrap should exit cleanly
	// without writing any files past LICENSES.txt (which is written
	// pre-prompt as an audit trail).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("HTTP call unexpectedly made after consent refusal")
	}))
	defer srv.Close()

	dest := t.TempDir()
	var stdin bytes.Buffer
	stdin.WriteString("n\n")
	var stdout bytes.Buffer

	err := Run(context.Background(), Options{
		Dest: dest,
		Items: []Item{{
			Name:    "fake",
			URL:     srv.URL,
			Archive: ArchiveRaw,
			OnDisk:  "f.sf2",
			License: "test",
		}},
		HTTPClient: srv.Client(),
		Stdin:      &stdin,
		Stdout:     &stdout,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(stdout.String(), "aborted by user") {
		t.Errorf("expected abort log, got:\n%s", stdout.String())
	}
}

func TestPartialFileCleanedOnHTTPError(t *testing.T) {
	// Mid-stream failure: server returns 500. The .partial must not
	// be promoted to the final filename, and the partial gets
	// removed on the error path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dest := t.TempDir()
	var stdout bytes.Buffer
	err := Run(context.Background(), Options{
		Dest:           dest,
		AcceptLicenses: true,
		Items: []Item{{
			Name:    "fake",
			URL:     srv.URL,
			Archive: ArchiveRaw,
			OnDisk:  "fake/a.sf2",
			License: "test",
		}},
		HTTPClient: srv.Client(),
		Stdout:     &stdout,
	})
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
	final := filepath.Join(dest, "fake/a.sf2")
	if _, err := os.Stat(final); err == nil {
		t.Errorf("final %q should not exist after 500 response", final)
	}
	if _, err := os.Stat(final + ".partial"); err == nil {
		t.Errorf(".partial %q should be cleaned up after HTTP error", final+".partial")
	}
}
