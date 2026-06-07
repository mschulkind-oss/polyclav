// Package bootstrap implements `polyclav bootstrap`: download the example
// soundfonts referenced by the default polyclav.toml into
// ~/.local/share/polyclav/soundfonts/ so the daemon can start without the
// user manually wrangling free-soundfont archives.
//
// The download list is the curated set referenced by polyclav.example.toml
// (see ItemList). Each entry maps a remote URL to the on-disk layout
// the example config expects; archives are unpacked in place so the
// resulting paths match the [[patches]] soundfont = ... lines verbatim.
//
// Bootstrap is intentionally not invoked from daemon startup — the
// "Errors not warnings" rule in docs/USER_GUIDE.md says explicit
// consent is required (the user must run `polyclav bootstrap`). Daemon
// startup just refuses to start if dependencies are missing and
// instructs the user to either bootstrap or trim the config.
package bootstrap

import (
	"fmt"
	"path/filepath"
)

// Item is one downloadable soundfont pack. URL is the canonical source
// verified live during implementation (see the implementer's report
// for the HEAD-check pass). Archive selects how Run() unpacks the
// download; OnDisk is the path (relative to the destination root, e.g.
// ~/.local/share/polyclav/soundfonts/) that polyclav.example.toml's
// soundfont = ... line points at — Run() must leave the file there
// after extraction, or fail loudly.
type Item struct {
	// Name is the [[patches]].name from polyclav.example.toml. Used in
	// progress lines and the per-pack license header.
	Name string

	// URL is the direct download. Verified to return HTTP 200 with a
	// real payload at implementation time; if upstream moves, the
	// downloader fails loudly with the URL so the user can fetch by
	// hand.
	URL string

	// Archive selects the unpack strategy applied after the download
	// completes. Raw means "drop the file at OnDisk verbatim".
	Archive ArchiveKind

	// OnDisk is the path (relative to the destination root) that
	// polyclav.example.toml expects to find the soundfont at. For Raw
	// downloads this is also the final filename; for archives it's
	// the path to the chosen .sfz/.sf2 inside the unpacked tree —
	// Run() verifies it exists after extraction.
	OnDisk string

	// UnpackInto is the directory (relative to dest) into which the
	// archive's contents are placed. For archives whose top-level
	// directory already matches OnDisk's parent, this is empty (the
	// archive is unpacked at the dest root). For freepats 7z packs
	// the example.toml wraps them in an extra parent dir (e.g.
	// "freepats-fm-piano1") that UnpackInto creates.
	UnpackInto string

	// RenameFrom is set when the archive places the target file at a
	// different path than OnDisk. Run() does a single os.Rename after
	// unpack to land the file where the example config expects it.
	// Empty means OnDisk already matches the archive layout.
	// Path is relative to dest, same as OnDisk.
	RenameFrom string

	// RenameDirFrom is set when the archive's top-level directory
	// name doesn't match what polyclav.example.toml expects (e.g.
	// Salamander unpacks to "SalamanderGrandPianoV3+20161209_44khz16bit"
	// but example.toml expects "SalamanderGrandPianoV3_44.1khz16bit").
	// Run() renames the whole directory after extraction. Mutually
	// exclusive with RenameFrom — pick one.
	RenameDirFrom string
	RenameDirTo   string

	// SizeBytes is the approximate download size, used for the
	// "[1/9] foo … 12 MB / 24 MB" progress line. From a HEAD request
	// during URL verification; doesn't have to be exact (the
	// downloader reads the live Content-Length too).
	SizeBytes int64

	// License is a short human string ("CC-BY-3.0", "CC0", …) plus
	// the upstream URL. Printed at the consent prompt and written to
	// LICENSES.txt in the destination directory for posterity.
	License string
}

// ArchiveKind selects the unpack strategy in Run().
type ArchiveKind int

const (
	// ArchiveRaw is a single bare file — no extraction, the download
	// IS the final asset (e.g. a single .sf2).
	ArchiveRaw ArchiveKind = iota
	// ArchiveZip is a .zip extracted with the stdlib archive/zip.
	ArchiveZip
	// Archive7z is a .7z extracted via the system `7z` binary
	// (p7zip). Stdlib has no native 7z support; p7zip is documented
	// as a dependency in yolo-jail.jsonc and INSTALL.md.
	Archive7z
	// ArchiveTarBz2 is a .tar.bz2 extracted via the system `tar`
	// binary (stdlib's archive/tar handles tar but not bz2 framing
	// without an extra dep; `tar -xjf` is universally available).
	ArchiveTarBz2
	// ArchiveTarXz is a .tar.xz extracted via the system `tar`
	// binary (`tar -xJf`). Stdlib has no native xz codec.
	ArchiveTarXz
)

// ItemList returns the full ordered set of bootstrap items. Order
// matches polyclav.example.toml's [[patches]] blocks 1..9 so the user
// sees a familiar sequence in the progress output. The 10th patch
// (moog-bass-native) needs no download — it's pure-Rust.
func ItemList() []Item {
	return []Item{
		{
			Name:      "ydp-grand",
			URL:       "https://freepats.zenvoid.org/Piano/YDP-GrandPiano/YDP-GrandPiano-SF2-20160804.tar.bz2",
			Archive:   ArchiveTarBz2,
			OnDisk:    "YDP-GrandPiano-SF2-20160804/YDP-GrandPiano-20160804.sf2",
			SizeBytes: 36737684,
			License:   "CC-BY-3.0 — https://freepats.zenvoid.org/Piano/acoustic-grand-piano.html",
		},
		{
			// Older 44.1kHz/16-bit re-release of Salamander Grand Piano v3.
			// The current freepats catalogue ships only V3+20200602 (with
			// a "+" in the dir name), but polyclav.example.toml's path
			// `SalamanderGrandPianoV3_44.1khz16bit/SalamanderGrandPianoV3.sfz`
			// targets the older convention. Unpack writes the file at
			// that legacy path so the example config resolves verbatim.
			Name:      "salamander",
			URL:       "https://freepats.zenvoid.org/Piano/SalamanderGrandPiano/SalamanderGrandPianoV3+20161209_44khz16bit.tar.xz",
			Archive:   ArchiveTarXz,
			OnDisk:    "SalamanderGrandPianoV3_44.1khz16bit/SalamanderGrandPianoV3.sfz",
			SizeBytes: 412313804,
			License:   "CC-BY-3.0 — https://freepats.zenvoid.org/Piano/acoustic-grand-piano.html",
		},
		{
			Name:      "wurlitzer",
			URL:       "https://codeload.github.com/sfzinstruments/GregSullivan.E-Pianos/zip/refs/heads/master",
			Archive:   ArchiveZip,
			OnDisk:    "GregSullivan.E-Pianos-master/Wurlitzer EP200/Wurlitzer EP200.sfz",
			SizeBytes: 0, // codeload doesn't return Content-Length on streaming zips
			License:   "CC-BY-SA — https://github.com/sfzinstruments/GregSullivan.E-Pianos",
		},
		{
			Name:      "rhodes",
			URL:       "https://codeload.github.com/sfzinstruments/jlearman.jRhodes3d/zip/refs/heads/master",
			Archive:   ArchiveZip,
			OnDisk:    "jlearman.jRhodes3d-master/jRhodes3d-mono/_jRhodes3d-mono-flac.sfz",
			SizeBytes: 0,
			License:   "CC-BY-SA — https://github.com/sfzinstruments/jlearman.jRhodes3d",
		},
		{
			Name:      "splendid",
			URL:       "https://codeload.github.com/sfzinstruments/SplendidGrandPiano/zip/refs/heads/master",
			Archive:   ArchiveZip,
			OnDisk:    "SplendidGrandPiano-master/Splendid Grand Piano.sfz",
			SizeBytes: 0,
			License:   "CC-BY-SA — https://github.com/sfzinstruments/SplendidGrandPiano",
		},
		{
			Name:      "dx7-rom1a",
			URL:       "https://raw.githubusercontent.com/Caskexe/DX/master/DX7/Factory%20ROM/Yamaha%20DX7%20ROM%201A.sf2",
			Archive:   ArchiveRaw,
			OnDisk:    "dx7-caskexe/Yamaha-DX7-ROM-1A.sf2",
			SizeBytes: 18219796,
			License:   "Unlicense / Public Domain — https://github.com/Caskexe/DX",
		},
		{
			Name:       "dx7-epiano",
			URL:        "https://github.com/freepats/fm-piano1/releases/download/2019-09-16/FM-Piano1-SFZ+FLAC-20190916.7z",
			Archive:    Archive7z,
			OnDisk:     "freepats-fm-piano1/FM-Piano1 SFZ+FLAC-20190916/FM-Piano1 20190916.sfz",
			UnpackInto: "freepats-fm-piano1",
			SizeBytes:  24932509,
			License:    "CC0 — https://freepats.zenvoid.org/ElectricPiano/synthesized-piano.html",
		},
		{
			Name:       "moog-bass",
			URL:        "https://github.com/freepats/synth-bass-1/releases/download/2019-07-23/SynthBass1-SFZ+FLAC-20190723.7z",
			Archive:    Archive7z,
			OnDisk:     "freepats-synth-bass-1/SynthBass1 SFZ+FLAC-20190723/SynthBass1 20190723.sfz",
			UnpackInto: "freepats-synth-bass-1",
			SizeBytes:  748547,
			License:    "CC0 — https://freepats.zenvoid.org/Synthesizer/synth-bass.html",
		},
		{
			Name:       "taurus-bass",
			URL:        "https://github.com/freepats/lately-bass/releases/download/2024-04-09/LatelyBass-SFZ+FLAC-20240409.7z",
			Archive:    Archive7z,
			OnDisk:     "freepats-lately-bass/LatelyBass SFZ+FLAC-20240409/LatelyBass 20240409.sfz",
			UnpackInto: "freepats-lately-bass",
			SizeBytes:  1181315,
			License:    "CC0 — https://freepats.zenvoid.org/Synthesizer/synth-bass.html",
		},
	}
}

// AbsOnDisk joins dest and the item's OnDisk into the absolute final
// location the daemon will stat when validating the config.
func (it Item) AbsOnDisk(dest string) string {
	return filepath.Join(dest, it.OnDisk)
}

// String renders an Item compactly for progress lines.
func (it Item) String() string {
	return fmt.Sprintf("%s (%s)", it.Name, humanBytes(it.SizeBytes))
}

func humanBytes(n int64) string {
	if n <= 0 {
		return "?"
	}
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
