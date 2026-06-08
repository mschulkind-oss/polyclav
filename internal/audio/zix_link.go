//go:build !portable

package audio

// nixpkgs lilv (0.26+) links zix as a separate shared library, so the cgo
// final link needs -lzix-0. Distro lilv (0.24, e.g. Ubuntu/Debian) vendors
// zix internally and ships no libzix, so the portable build used for the
// PyPI wheel omits this flag via `-tags portable`. See
// .github/workflows/publish.yml. This is the only nix-vs-distro link
// difference; everything else is identical.

// #cgo LDFLAGS: -lzix-0
import "C"
