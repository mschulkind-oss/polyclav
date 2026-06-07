package bootstrap

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Options control a Run() invocation. Construct via Bootstrap's CLI
// flag parsing (see cmd/polyclav/main.go) or by hand from tests.
type Options struct {
	// Dest is the destination root for soundfont content. Production
	// default is ~/.local/share/polyclav/soundfonts/ (matching the
	// `~/...` paths in polyclav.example.toml). Tests pass a t.TempDir().
	Dest string

	// AcceptLicenses, when true, skips the interactive consent prompt
	// (CLI flag: --accept-licenses / -y). The licenses are still
	// printed and written to LICENSES.txt — only the prompt is
	// bypassed.
	AcceptLicenses bool

	// SkipExisting, when true (the default), leaves an item alone if
	// AbsOnDisk(dest) already exists. Idempotency: re-running
	// bootstrap after a partial completion picks up where it left off
	// without re-downloading.
	SkipExisting bool

	// Items is the list of bootstrap items to process. Production
	// callers pass ItemList(); tests substitute a mock list pointing
	// at httptest.NewServer.
	Items []Item

	// HTTPClient is the underlying client. Defaults to a generous
	// 30-minute timeout suitable for the Salamander download over a
	// modest connection. Tests inject httptest.NewServer's client.
	HTTPClient *http.Client

	// Stdin/Stdout/Stderr override the standard streams for the
	// interactive prompt and progress output. Defaults are
	// os.Stdin/Stdout/Stderr.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Run executes the bootstrap workflow: print the license / consent
// header, prompt for acceptance unless --accept-licenses, then
// download + unpack each item in order. Returns nil on success or the
// first hard failure (network, archive-corruption, missing post-unpack
// target). Caller invokes os.Exit on error.
func Run(ctx context.Context, opts Options) error {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Minute}
	}
	if len(opts.Items) == 0 {
		opts.Items = ItemList()
	}
	if opts.Dest == "" {
		return fmt.Errorf("bootstrap: Dest is required")
	}

	if err := os.MkdirAll(opts.Dest, 0o755); err != nil {
		return fmt.Errorf("bootstrap: mkdir %q: %w", opts.Dest, err)
	}

	// License header.
	fmt.Fprintf(opts.Stdout, "polyclav bootstrap — downloading %d soundfont packs to %s\n\n",
		len(opts.Items), opts.Dest)
	for i, it := range opts.Items {
		fmt.Fprintf(opts.Stdout, "  [%d/%d] %-14s  %-8s  %s\n",
			i+1, len(opts.Items), it.Name, humanBytes(it.SizeBytes), it.License)
	}
	fmt.Fprintln(opts.Stdout)

	// Always persist licenses to LICENSES.txt — useful for
	// redistribution audits even if the user ran with -y.
	if err := writeLicensesFile(opts.Dest, opts.Items); err != nil {
		return fmt.Errorf("bootstrap: write LICENSES.txt: %w", err)
	}

	if !opts.AcceptLicenses {
		if !confirm(opts.Stdin, opts.Stdout, "Download all and accept the licenses above? [y/N]: ") {
			fmt.Fprintln(opts.Stdout, "bootstrap aborted by user")
			return nil
		}
	}

	for i, it := range opts.Items {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		fmt.Fprintf(opts.Stdout, "\n[%d/%d] %s\n", i+1, len(opts.Items), it.Name)
		final := it.AbsOnDisk(opts.Dest)
		if opts.SkipExisting {
			if _, err := os.Stat(final); err == nil {
				fmt.Fprintf(opts.Stdout, "  skip: already present at %s\n", final)
				continue
			}
		}
		if err := downloadAndUnpack(ctx, opts, it); err != nil {
			return fmt.Errorf("bootstrap %s: %w", it.Name, err)
		}
		if _, err := os.Stat(final); err != nil {
			return fmt.Errorf("bootstrap %s: post-unpack stat %q: %w", it.Name, final, err)
		}
		fmt.Fprintf(opts.Stdout, "  ok: %s\n", final)
	}

	fmt.Fprintln(opts.Stdout, "\nbootstrap complete. Start the daemon with: polyclav")
	return nil
}

func confirm(r io.Reader, w io.Writer, prompt string) bool {
	fmt.Fprint(w, prompt)
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	resp := strings.ToLower(strings.TrimSpace(line))
	return resp == "y" || resp == "yes"
}

// downloadAndUnpack streams the URL to a .partial file, atomically
// renames it on completion, then dispatches to the per-archive
// extractor. Raw items skip the unpack step (the download IS the
// final asset).
func downloadAndUnpack(ctx context.Context, opts Options, it Item) error {
	final := it.AbsOnDisk(opts.Dest)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", filepath.Dir(final), err)
	}

	// Download target — Raw items write directly to `final.partial`;
	// archive items write to a temp file in dest, unpacked after.
	var downloadPath string
	if it.Archive == ArchiveRaw {
		downloadPath = final + ".partial"
	} else {
		downloadPath = filepath.Join(opts.Dest, fmt.Sprintf(".%s-download.partial", it.Name))
	}

	if err := streamDownload(ctx, opts, it, downloadPath); err != nil {
		_ = os.Remove(downloadPath)
		return err
	}

	if it.Archive == ArchiveRaw {
		// Atomically place the bare file at its final location.
		if err := os.Rename(downloadPath, final); err != nil {
			return fmt.Errorf("rename %q -> %q: %w", downloadPath, final, err)
		}
		return nil
	}

	// Archive items: unpack into the chosen directory, then run any
	// post-unpack renames, then drop the temp file. Best-effort
	// cleanup via defer — production runs leave the partial behind on
	// error so the user can inspect.
	if err := unpack(opts, it, downloadPath); err != nil {
		return err
	}
	_ = os.Remove(downloadPath)

	if it.RenameDirFrom != "" && it.RenameDirTo != "" {
		from := filepath.Join(opts.Dest, it.RenameDirFrom)
		to := filepath.Join(opts.Dest, it.RenameDirTo)
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("rename dir %q -> %q: %w", from, to, err)
		}
	}
	if it.RenameFrom != "" {
		from := filepath.Join(opts.Dest, it.RenameFrom)
		if err := os.Rename(from, final); err != nil {
			return fmt.Errorf("rename %q -> %q: %w", from, final, err)
		}
	}
	return nil
}

// streamDownload performs a single GET, streams to dest with a simple
// progress line, and never leaves a truncated file at its final name
// (caller passes dest as a .partial path; rename-on-success is the
// caller's job).
func streamDownload(ctx context.Context, opts Options, it Item, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, it.URL, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", it.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", it.URL, resp.StatusCode)
	}

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %q: %w", dest, err)
	}
	defer out.Close()

	total := resp.ContentLength
	if total <= 0 {
		total = it.SizeBytes
	}

	r := &progressReader{
		r:        resp.Body,
		w:        opts.Stdout,
		total:    total,
		name:     it.Name,
		lastTick: time.Now(),
	}
	if _, err := io.Copy(out, r); err != nil {
		return fmt.Errorf("download body: %w", err)
	}
	r.finish()
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync %q: %w", dest, err)
	}
	return nil
}

// progressReader prints a "name … N MB / total MB\r" line every ~200ms.
// Counting-reader pattern from the io.Copy docs — no third-party deps.
type progressReader struct {
	r        io.Reader
	w        io.Writer
	total    int64
	read     int64
	name     string
	lastTick time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	if time.Since(p.lastTick) > 200*time.Millisecond {
		p.print()
		p.lastTick = time.Now()
	}
	return n, err
}

func (p *progressReader) print() {
	if p.total > 0 {
		pct := float64(p.read) / float64(p.total) * 100.0
		fmt.Fprintf(p.w, "  %s … %s / %s (%.0f%%)\r",
			p.name, humanBytes(p.read), humanBytes(p.total), pct)
	} else {
		fmt.Fprintf(p.w, "  %s … %s\r", p.name, humanBytes(p.read))
	}
}

func (p *progressReader) finish() {
	p.print()
	fmt.Fprintln(p.w)
}

// unpack dispatches to the archive-specific extractor based on
// it.Archive. The extracted layout is rooted under unpackRoot(opts.Dest, it).
func unpack(opts Options, it Item, archivePath string) error {
	root := unpackRoot(opts.Dest, it)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", root, err)
	}
	switch it.Archive {
	case ArchiveZip:
		return unpackZip(archivePath, root)
	case Archive7z:
		return unpack7z(archivePath, root)
	case ArchiveTarBz2:
		return unpackTar(archivePath, root, "-xjf")
	case ArchiveTarXz:
		return unpackTar(archivePath, root, "-xJf")
	default:
		return fmt.Errorf("unsupported archive kind %d for %s", it.Archive, it.Name)
	}
}

func unpackRoot(dest string, it Item) string {
	if it.UnpackInto != "" {
		return filepath.Join(dest, it.UnpackInto)
	}
	return dest
}

// unpackZip uses archive/zip from the stdlib. ZipSlip-safe: each entry
// is resolved relative to root and rejected if it escapes.
func unpackZip(archivePath, root string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip %q: %w", archivePath, err)
	}
	defer r.Close()
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	for _, f := range r.File {
		target := filepath.Join(root, f.Name)
		targetAbs, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(targetAbs, rootAbs+string(filepath.Separator)) && targetAbs != rootAbs {
			return fmt.Errorf("zip entry %q escapes destination", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode())
		if err != nil {
			src.Close()
			return err
		}
		if _, err := io.Copy(dst, src); err != nil {
			src.Close()
			dst.Close()
			return err
		}
		src.Close()
		dst.Close()
	}
	return nil
}

// unpack7z shells out to the system `7z` binary (p7zip). Required for
// FreePats archives, which are .7z. p7zip is documented in
// yolo-jail.jsonc and docs/INSTALL.md as a dependency for this very
// reason.
func unpack7z(archivePath, root string) error {
	return runCmd(root, "7z", "x", "-y", "-o"+root, archivePath)
}

// unpackTar shells out to the system `tar` binary. flag is "-xjf"
// (bz2) or "-xJf" (xz). Stdlib's archive/tar handles uncompressed tar
// but compression framing needs an extra dep we don't carry.
func unpackTar(archivePath, root, flag string) error {
	return runCmd(root, "tar", flag, archivePath, "-C", root)
}

func runCmd(cwd, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w (stderr: %s)",
			name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// writeLicensesFile drops a LICENSES.txt at the destination root with
// one line per pack. Idempotent — overwrite each bootstrap so license
// updates propagate.
func writeLicensesFile(dest string, items []Item) error {
	var b strings.Builder
	b.WriteString("polyclav bootstrap — soundfont licenses\n")
	b.WriteString("Generated by `polyclav bootstrap`.\n\n")
	for _, it := range items {
		fmt.Fprintf(&b, "[%s]\n  %s\n  Source: %s\n\n", it.Name, it.License, it.URL)
	}
	return os.WriteFile(filepath.Join(dest, "LICENSES.txt"), []byte(b.String()), 0o644)
}
