package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// xdgDataDirSuffix is polyclav's data dir under the user's home directory. It
// is the one spelling shared by DataDir, remapDataDir and the embedded example
// config, which (like the docs) writes paths as `~/.local/share/polyclav/...`.
const xdgDataDirSuffix = "/.local/share/polyclav"

// DataDir returns polyclav's per-user data directory — the parent of the
// `soundfonts/`, `lib/` and `clips/` directories bootstrap, the sfizz loader
// and the audition player use:
//
//	Linux, macOS: ~/.local/share/polyclav (XDG, unchanged)
//	Windows:      %LOCALAPPDATA%\polyclav
//
// Windows gets %LOCALAPPDATA% rather than an XDG-style `~/.local/share` because
// that is the platform convention for downloaded content, and because it sits
// beside the config dir os.UserConfigDir resolves (%APPDATA%).
//
// It never fails, deliberately: with no home directory (or no %LOCALAPPDATA%)
// it returns the literal `~/.local/share/polyclav`, which is what ExpandHome
// leaves behind in that same situation. Callers keep the old "expand what you
// can, let the OS fail to find the path" behaviour instead of gaining an error
// path at startup.
func DataDir() string {
	home, _ := os.UserHomeDir()
	dir, err := dataDirFor(runtime.GOOS, home, os.Getenv("LOCALAPPDATA"))
	if err != nil {
		return "~" + xdgDataDirSuffix
	}
	return dir
}

// dataDirFor is DataDir's whole platform decision, split out so tests can pin
// the Windows shape on any host.
func dataDirFor(goos, home, localAppData string) (string, error) {
	if goos == "windows" {
		if localAppData == "" {
			return "", fmt.Errorf("polyclav data dir: %%LOCALAPPDATA%% is not set")
		}
		return filepath.Join(localAppData, "polyclav"), nil
	}
	if home == "" {
		return "", fmt.Errorf("polyclav data dir: home directory is not known")
	}
	return home + xdgDataDirSuffix, nil
}

// expandDataPath is the single expansion step for every user-supplied
// soundfont/plugin path: ExpandHome's tilde expansion, then the legacy-XDG
// remap below. Both steps stay together so no call site gets one without the
// other.
func expandDataPath(path string) string {
	return remapLegacyDataDir(ExpandHome(path))
}

// remapLegacyDataDir rewrites a leading `~/.local/share/polyclav` in path (as
// ExpandHome left it) onto DataDir.
func remapLegacyDataDir(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	return remapDataDir(path, home, DataDir())
}

// remapDataDir is remapLegacyDataDir's whole decision, split out so tests can
// pin the Windows shape on any host.
//
// It exists because the embedded example config (polyclav.example.toml) and
// the docs spell soundfont paths the XDG way while bootstrap writes to the
// platform data dir: without the remap, a fresh Windows install downloads
// soundfonts into `%USERPROFILE%\.local\share\polyclav`, a directory the stock
// config never reads, and the daemon then refuses to start with missing
// dependencies. On Linux and macOS dataDir IS the legacy path, so this is a
// byte-identical no-op there.
//
// Only the exact leading prefix is rewritten — a path that merely contains it
// further along, or that shares a partial prefix, is left alone — and the
// remainder is rejoined with the platform's own separator.
func remapDataDir(path, home, dataDir string) string {
	legacy := home + xdgDataDirSuffix
	if dataDir == "" || dataDir == legacy {
		return path
	}
	if path == legacy {
		return dataDir
	}
	rest, ok := strings.CutPrefix(path, legacy+"/")
	if !ok {
		return path
	}
	return filepath.Join(dataDir, filepath.FromSlash(rest))
}
