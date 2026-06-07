package audio

// #cgo CFLAGS: -I${SRCDIR}/../../audio-core/include
// #cgo LDFLAGS: -L${SRCDIR}/../../target/release -lpolyclav_audio_core -lpthread -ldl -lm
// #cgo LDFLAGS: -lsfizz
// // Phase 1 LV2 plugin host: livi -> lilv -> serd/sord/sratom/zix. The
// // Rust staticlib swallows their `cargo:rustc-link-lib` directives, so we
// // re-state them at the cgo link step. The matching -L / -Wl,-rpath
// // entries come from CGO_LDFLAGS in mise.toml.
// #cgo LDFLAGS: -llilv-0 -lserd-0 -lsord-0 -lsratom-0 -lzix-0
// // CLAP plugin host (clack-host) uses libloading -> libdl; -ldl already
// // present above. No extra system library is needed at link time.
// #cgo pkg-config: libpipewire-0.3 libspa-0.2
// #include "polyclav_audio.h"
// #include <stdlib.h>
// #include <stdint.h>
// // Forward declarations for Phase 1 plugin host FFI (Agent A is producing
// // these in audio-core; declared here so Go compiles even if the header
// // lands later. Once the header has them, these are harmless redundant
// // declarations).
// int32_t polyclav_audio_set_lv2_plugin(const char *uri);
// int32_t polyclav_audio_set_clap_plugin(const char *bundle_path, const char *plugin_id);
// // Native synth backend (Phase 1; see docs/ROADMAP.md).
// int32_t polyclav_audio_set_native_patch(const char *engine);
// void    polyclav_dsp_set_native_cutoff_hz(float hz);
import "C"

import (
	"fmt"
	"unsafe"
)

func Start() error {
	rc := C.polyclav_audio_start()
	if rc != 0 {
		return fmt.Errorf("audio-core start failed: %d", int(rc))
	}
	return nil
}

func Stop() {
	C.polyclav_audio_stop()
}

// SetSoundfont points the audio engine at an SF2 file. Empty path clears it
// (sine fallback). Must be called before Start.
func SetSoundfont(path string) {
	if path == "" {
		C.polyclav_audio_set_soundfont(nil)
		return
	}
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	C.polyclav_audio_set_soundfont(cpath)
}

// ReloadSoundfont triggers a background load of whatever path is currently
// set by SetSoundfont. The audio thread swaps to the new backend on the
// next callback. Safe to call while playing; the previous backend is
// dropped after the swap.
func ReloadSoundfont() error {
	rc := C.polyclav_audio_reload_soundfont()
	if rc != 0 {
		return fmt.Errorf("audio-core reload failed: %d", int(rc))
	}
	return nil
}

// SetLv2Plugin selects an LV2 plugin by URI. The plugin loads on a background
// thread; the audio thread swaps to it on the next callback. Returns an error
// if scheduling the load failed (audio not running, unknown URI, or
// instantiation failure reported synchronously by the Rust side). Empty uri
// is rejected.
func SetLv2Plugin(uri string) error {
	if uri == "" {
		return fmt.Errorf("audio-core set lv2 plugin: empty uri")
	}
	curi := C.CString(uri)
	defer C.free(unsafe.Pointer(curi))
	rc := C.polyclav_audio_set_lv2_plugin(curi)
	if rc != 0 {
		return fmt.Errorf("audio-core set lv2 plugin %q failed: %d", uri, int(rc))
	}
	return nil
}

// SetClapPlugin selects a CLAP plugin by bundle path and plugin id. The
// plugin loads on a background thread; the audio thread swaps to it on the
// next callback. Returns an error if scheduling the load failed. Empty
// bundlePath or pluginID are rejected.
func SetClapPlugin(bundlePath, pluginID string) error {
	if bundlePath == "" {
		return fmt.Errorf("audio-core set clap plugin: empty bundle path")
	}
	if pluginID == "" {
		return fmt.Errorf("audio-core set clap plugin: empty plugin id")
	}
	cpath := C.CString(bundlePath)
	defer C.free(unsafe.Pointer(cpath))
	cid := C.CString(pluginID)
	defer C.free(unsafe.Pointer(cid))
	rc := C.polyclav_audio_set_clap_plugin(cpath, cid)
	if rc != 0 {
		return fmt.Errorf("audio-core set clap plugin %q/%q failed: %d", bundlePath, pluginID, int(rc))
	}
	return nil
}

// SetNativePatch selects a pure-Rust native synth patch by engine name.
// Phase 1 only ships "minimoog" (see docs/ROADMAP.md).
// The synth instantiates on a background thread; the audio thread swaps
// to it on the next callback. Returns an error if scheduling the load
// failed or the engine name is unknown.
func SetNativePatch(engine string) error {
	if engine == "" {
		return fmt.Errorf("audio-core set native patch: empty engine name")
	}
	c := C.CString(engine)
	defer C.free(unsafe.Pointer(c))
	rc := C.polyclav_audio_set_native_patch(c)
	if rc != 0 {
		return fmt.Errorf("audio-core set native patch %q failed: %d", engine, int(rc))
	}
	return nil
}

// SetMasterVolume in [0.0, 1.0]. Clamped in Rust. Takes effect on the next
// audio callback (~3 ms).
func SetMasterVolume(v float32) {
	C.polyclav_dsp_set_master_volume(C.float(v))
}

// SetCompressor amount in [0.0, 1.0]. 0 = bypass, 1 = aggressive. Clamped
// in Rust.
func SetCompressor(v float32) {
	C.polyclav_dsp_set_compressor(C.float(v))
}

// SetReverb mix in [0.0, 1.0]. 0 = dry, 1 = full wet. Clamped in Rust.
func SetReverb(v float32) {
	C.polyclav_dsp_set_reverb(C.float(v))
}

// SetPatchGain is a per-patch linear gain multiplier. Default 1.0; clamped
// to [0, 8] in Rust. Push on patch select.
func SetPatchGain(linear float32) {
	C.polyclav_dsp_set_patch_gain(C.float(linear))
}

// SetMasteringCompressor amount in [0.0, 1.0]. 0 = bypass. Clamped in Rust.
func SetMasteringCompressor(amount float32) {
	C.polyclav_dsp_set_mastering_compressor(C.float(amount))
}

// SetLimiterCeilingDB is a brick-wall ceiling in dBFS. Default -0.3,
// clamped to [-12, 0] in Rust.
func SetLimiterCeilingDB(db float32) {
	C.polyclav_dsp_set_limiter_ceiling_db(C.float(db))
}

// SetNativeCutoffHz pushes the active native synth's filter cutoff. The
// audio thread reads the atomic per block and applies it to the active
// SynthBackend::Native (no-op for other backends). Clamped to
// [20, 20000] in Rust. Phase 1 hardcoded knob-4 mapping.
func SetNativeCutoffHz(hz float32) {
	C.polyclav_dsp_set_native_cutoff_hz(C.float(hz))
}

// MIDIEvent is what Go pushes into the realtime audio thread's MIDI queue.
// Drops silently if the queue is full (audio takes priority).
type MIDIEvent struct {
	Channel byte
	Note    byte
	Vel     byte
	CC      byte
	Value   byte
	Bend    uint16 // PitchBend absolute 14-bit value, 0..16383 (8192 = centre)
	Kind    MIDIKind
}

type MIDIKind uint8

const (
	MIDINoteOn MIDIKind = iota
	MIDINoteOff
	MIDIControlChange
	MIDIPitchBend
)

func PushMIDI(ev MIDIEvent) {
	switch ev.Kind {
	case MIDINoteOn:
		C.polyclav_midi_note_on(C.uint8_t(ev.Channel), C.uint8_t(ev.Note), C.uint8_t(ev.Vel))
	case MIDINoteOff:
		C.polyclav_midi_note_off(C.uint8_t(ev.Channel), C.uint8_t(ev.Note), C.uint8_t(ev.Vel))
	case MIDIControlChange:
		C.polyclav_midi_cc(C.uint8_t(ev.Channel), C.uint8_t(ev.CC), C.uint8_t(ev.Value))
	case MIDIPitchBend:
		C.polyclav_midi_pitch_bend(C.uint8_t(ev.Channel), C.uint16_t(ev.Bend))
	}
}
