//go:build linux && !portable

package audio

// nixpkgs lilv (0.26+) links zix as a separate shared library, so the cgo
// final link needs -lzix-0. Distro lilv (0.24, e.g. Ubuntu/Debian) vendors
// zix internally and ships no libzix, so the portable build used for the
// PyPI wheel omits this flag via `-tags portable`. See
// .github/workflows/publish.yml.
//
// The `linux` term is required now that macOS is a build target: LV2 (and
// therefore lilv/zix) is Linux-only, so darwin must never see -lzix-0
// (`ld: library not found for -lzix`). This is the only nix-vs-distro link
// difference on Linux; everything else is identical.

// #cgo LDFLAGS: -lzix-0
import "C"
