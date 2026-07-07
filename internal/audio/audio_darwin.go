//go:build darwin

package audio

// macOS link flags: CoreAudio via cpal. A Rust staticlib does NOT propagate
// its transitive cargo:rustc-link-lib=framework directives to the cgo link, so
// we restate the frameworks cpal/coreaudio-rs need. -framework is on cgo's
// default LDFLAGS allowlist, so no CGO_LDFLAGS_ALLOW is required.
//   -lpolyclav_audio_core : the Rust staticlib itself (built to target/release)
//   CoreAudio             : the HAL -- AudioObject/AudioHardware property APIs
//   AudioUnit             : the output AudioUnit (AUHAL) used for playback
//   AudioToolbox          : where AUHAL symbols live on modern coreaudio-rs
//   CoreFoundation        : CFString/CFNumber/CFDictionary used by the HAL APIs
// Both AudioUnit and AudioToolbox are listed on purpose (which one carries
// AUHAL has shifted across coreaudio-rs versions); an unused -framework to an
// always-present system framework is a no-op to ld.
//
// NO -ldl: macOS has no libdl (dlopen is in libSystem, auto-linked); passing
// -ldl would be a hard "library not found for -ldl". -lpthread/-lm are harmless
// (resolve via libSystem). LV2/CLAP are gated OUT of the macOS Rust build, so
// none of the lilv/serd/sord/sratom/zix chain is linked here.

// #cgo LDFLAGS: -L${SRCDIR}/../../target/release -lpolyclav_audio_core -lpthread -lm
// #cgo LDFLAGS: -framework CoreAudio -framework AudioUnit -framework AudioToolbox -framework CoreFoundation
import "C"
