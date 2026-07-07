//go:build linux

package audio

// Linux link flags: PipeWire (+ libspa) via pkg-config, plus the LV2 host
// chain. The Rust staticlib does NOT re-propagate its dependencies'
// cargo:rustc-link-lib directives to the cgo link, so we restate them here:
//   -lpolyclav_audio_core : the Rust staticlib itself (built to target/release)
//   -lpthread -ldl -lm    : std / libloading(dlopen) / DSP math
//   -llilv-0 etc.         : the LV2 host stack (livi -> lilv -> serd/sord/sratom)
// The matching -L / -Wl,-rpath for the nixpkgs lilv/pipewire .so chain come
// from CGO_LDFLAGS in mise.toml. zix is linked separately in zix_link.go
// (nixpkgs lilv 0.26+ only).

// #cgo pkg-config: libpipewire-0.3 libspa-0.2
// #cgo LDFLAGS: -L${SRCDIR}/../../target/release -lpolyclav_audio_core -lpthread -ldl -lm
// #cgo LDFLAGS: -llilv-0 -lserd-0 -lsord-0 -lsratom-0
import "C"
