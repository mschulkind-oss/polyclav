package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDataDirFor(t *testing.T) {
	// Windows paths are joined with the host's separator (dataDirFor is a pure
	// decision, only ever called with the host's own GOOS in production), so
	// the expectation uses filepath.Join too and holds on any host.
	winLocalAppData := `C:\Users\alice\AppData\Local`

	tests := []struct {
		name         string
		goos         string
		home         string
		localAppData string
		want         string
		wantErr      bool
	}{
		{
			name: "linux keeps the XDG path byte for byte",
			goos: "linux", home: "/home/alice", localAppData: winLocalAppData,
			want: "/home/alice/.local/share/polyclav",
		},
		{
			name: "macOS keeps the XDG path byte for byte",
			goos: "darwin", home: "/Users/alice", localAppData: winLocalAppData,
			want: "/Users/alice/.local/share/polyclav",
		},
		{
			name: "windows uses %LOCALAPPDATA%",
			goos: "windows", home: `C:\Users\alice`, localAppData: winLocalAppData,
			want: filepath.Join(winLocalAppData, "polyclav"),
		},
		{
			name: "windows without %LOCALAPPDATA% is an error, not a guess",
			goos: "windows", home: `C:\Users\alice`, localAppData: "",
			wantErr: true,
		},
		{
			name: "posix without a home dir is an error",
			goos: "linux", home: "", localAppData: "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dataDirFor(tt.goos, tt.home, tt.localAppData)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("dataDirFor(%q, %q, %q) = %q, want an error", tt.goos, tt.home, tt.localAppData, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("dataDirFor(%q, %q, %q): %v", tt.goos, tt.home, tt.localAppData, err)
			}
			if got != tt.want {
				t.Errorf("dataDirFor(%q, %q, %q) = %q, want %q", tt.goos, tt.home, tt.localAppData, got, tt.want)
			}
		})
	}
}

func TestDataDirPOSIXUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asserts the POSIX spelling; the host is Windows")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("no home dir on this host: %v", err)
	}
	// Byte-identical to what the old hardcoded `~/.local/share/polyclav/...`
	// constants produced through ExpandHome.
	if want, got := home+"/.local/share/polyclav", DataDir(); got != want {
		t.Errorf("DataDir() = %q, want %q", got, want)
	}
}

func TestRemapDataDir(t *testing.T) {
	posixHome := "/home/alice"
	posixDataDir := posixHome + "/.local/share/polyclav"

	winHome := `C:\Users\alice`
	winLegacy := winHome + "/.local/share/polyclav"
	winDataDir := filepath.Join(`C:\Users\alice\AppData\Local`, "polyclav")

	tests := []struct {
		name    string
		path    string
		home    string
		dataDir string
		want    string
	}{
		{
			name: "posix data dir itself is untouched",
			path: posixDataDir, home: posixHome, dataDir: posixDataDir,
			want: posixDataDir,
		},
		{
			name: "posix soundfont path is untouched",
			path: posixDataDir + "/soundfonts/YDP/p.sf2", home: posixHome, dataDir: posixDataDir,
			want: posixDataDir + "/soundfonts/YDP/p.sf2",
		},
		{
			name: "windows remaps the bare legacy data dir",
			path: winLegacy, home: winHome, dataDir: winDataDir,
			want: winDataDir,
		},
		{
			name: "windows remaps an example-config soundfont path",
			path: winLegacy + "/soundfonts/YDP-GrandPiano-SF2-20160804/YDP.sf2", home: winHome, dataDir: winDataDir,
			want: filepath.Join(winDataDir, filepath.FromSlash("soundfonts/YDP-GrandPiano-SF2-20160804/YDP.sf2")),
		},
		{
			name: "windows remaps a user-authored subpath under the legacy dir",
			path: winLegacy + "/my-own-fonts/kawai.sf2", home: winHome, dataDir: winDataDir,
			want: filepath.Join(winDataDir, "my-own-fonts", "kawai.sf2"),
		},
		{
			name: "a path merely containing the legacy prefix later is not rewritten",
			path: "/srv/mirror" + winLegacy + "/soundfonts/x.sf2", home: winHome, dataDir: winDataDir,
			want: "/srv/mirror" + winLegacy + "/soundfonts/x.sf2",
		},
		{
			name: "a shared but different prefix is not rewritten",
			path: winLegacy + "X/soundfonts/x.sf2", home: winHome, dataDir: winDataDir,
			want: winLegacy + "X/soundfonts/x.sf2",
		},
		{
			name: "another user's legacy dir is not rewritten",
			path: "/home/bob/.local/share/polyclav/soundfonts/x.sf2", home: posixHome, dataDir: "/opt/polyclav-data",
			want: "/home/bob/.local/share/polyclav/soundfonts/x.sf2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := remapDataDir(tt.path, tt.home, tt.dataDir); got != tt.want {
				t.Errorf("remapDataDir(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestExpandDataPathPOSIXUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asserts the POSIX spelling; the host is Windows")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("no home dir on this host: %v", err)
	}
	// What config.Load did before the data dir became platform-aware: plain
	// ExpandHome, nothing more.
	for _, path := range []string{
		"~/.local/share/polyclav/soundfonts/YDP-GrandPiano-20160804.sf2",
		"~/.local/share/polyclav/lib/libsfizz.so",
		"/opt/soundfonts/other.sf2",
		"~",
	} {
		want := ExpandHome(path)
		if got := expandDataPath(path); got != want {
			t.Errorf("expandDataPath(%q) = %q, want %q (ExpandHome unchanged on POSIX)", path, got, want)
		}
	}
	if got, want := expandDataPath("~/.local/share/polyclav/soundfonts/x.sf2"), home+"/.local/share/polyclav/soundfonts/x.sf2"; got != want {
		t.Errorf("expandDataPath soundfont path = %q, want %q", got, want)
	}
}
