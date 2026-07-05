package audio

// #cgo CFLAGS: -I${SRCDIR}/../../audio-core/include
// #cgo LDFLAGS: -L${SRCDIR}/../../target/release -lpolyclav_audio_core -lpthread -ldl -lm
// // sfizz (SFZ player) is OPTIONAL and dlopen'd at runtime by the Rust
// // audio-core — deliberately NOT linked here, so the build needs no sfizz
// // and `.sfz` patches degrade gracefully when libsfizz is absent.
// // Phase 1 LV2 plugin host: livi -> lilv -> serd/sord/sratom. The Rust
// // staticlib swallows their `cargo:rustc-link-lib` directives, so we
// // re-state them at the cgo link step. The matching -L / -Wl,-rpath
// // entries come from CGO_LDFLAGS in mise.toml. (zix is linked separately
// // for nixpkgs lilv 0.26+ in zix_link.go; distro lilv 0.24 vendors it.)
// #cgo LDFLAGS: -llilv-0 -lserd-0 -lsord-0 -lsratom-0
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
// void    polyclav_dsp_set_native_resonance(float v);
// void    polyclav_dsp_set_native_filter_env(float attack_s, float decay_s, float sustain, float release_s, float amount);
// void    polyclav_dsp_set_native_osc(int32_t idx, int32_t wave, int32_t octave, float detune_cents, float level);
// void    polyclav_dsp_set_native_noise(float level);
// void    polyclav_dsp_set_native_glide(float seconds);
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

// SfizzAvailable reports whether libsfizz could be loaded (dlopen), i.e.
// whether SFZ (.sfz) patches can play. Safe to call without Start; it only
// triggers the lazy dlopen probe. When false, .sfz patches are silent but
// SF2/SF3 (oxisynth), the native synth, and LV2/CLAP plugins are unaffected.
func SfizzAvailable() bool {
	return C.polyclav_audio_sfizz_available() == 1
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

// SetNativeResonance pushes the active native synth's filter resonance
// (Q). Same lifecycle as SetNativeCutoffHz: the audio thread reads the
// atomic per block and applies it to the active SynthBackend::Native
// (no-op for other backends). Default 0.3; clamped to [0.0, 0.95] in
// Rust to keep headroom below the Stilson/Smith ladder's
// self-oscillation instability.
func SetNativeResonance(v float32) {
	C.polyclav_dsp_set_native_resonance(C.float(v))
}

// SetNativeFilterEnv pushes the active native synth's filter-envelope
// (env 2) ADSR plus the env->cutoff amount. Same lifecycle as
// SetNativeCutoffHz: the audio thread reads the atomics per block and
// applies them to the active SynthBackend::Native (no-op for other
// backends). The modulation model is
// effective_cutoff = base_cutoff * 2^(amount*env*4), so amount in [0,1]
// sweeps up to +4 octaves above the knob cutoff (clamped to
// [20, 20000] Hz). Times are clamped to [0.0001, 10] s, sustain and
// amount to [0, 1] in Rust. Defaults: 5 ms / 600 ms / 0.4 / 600 ms with
// amount 0 (modulation off).
func SetNativeFilterEnv(a, d, s, r, amount float32) {
	C.polyclav_dsp_set_native_filter_env(C.float(a), C.float(d), C.float(s), C.float(r), C.float(amount))
}

// SetNativeOsc pushes one of the active native synth's three oscillators
// (idx 0..2). wave is "saw", "square", or "pulse" (pulse runs a fixed 25%
// duty for this stage); octave is clamped to [-2, 2] in Rust, detuneCents
// to [-100, 100], level to [0, 1]. Same lifecycle as SetNativeCutoffHz:
// the audio thread reads the atomics per block and applies them to the
// active SynthBackend::Native (no-op for other backends). Defaults keep
// osc 2/3 silent (level 0) with Moog-ish offsets pre-dialed: osc 1
// saw/0/0c/1.0, osc 2 saw/0/-7c/0.0, osc 3 saw/-1oct/+5c/0.0 — so the
// default render is unchanged and turning a level up immediately sounds
// right. Returns an error for an unknown wave name or idx outside 0..2.
func SetNativeOsc(idx int, wave string, octave int, detuneCents, level float32) error {
	var w int32
	switch wave {
	case "saw":
		w = 0
	case "square":
		w = 1
	case "pulse":
		w = 2
	default:
		return fmt.Errorf("audio-core set native osc: unknown wave %q (valid: saw, square, pulse)", wave)
	}
	if idx < 0 || idx > 2 {
		return fmt.Errorf("audio-core set native osc: idx %d out of range 0..2", idx)
	}
	C.polyclav_dsp_set_native_osc(C.int32_t(idx), C.int32_t(w), C.int32_t(octave), C.float(detuneCents), C.float(level))
	return nil
}

// SetNativeNoise pushes the active native synth's white-noise mixer level.
// Clamped to [0, 1] in Rust; default 0 = silent. Same lifecycle as
// SetNativeCutoffHz (no-op for other backends).
func SetNativeNoise(level float32) {
	C.polyclav_dsp_set_native_noise(C.float(level))
}

// SetNativeGlide pushes the active native synth's glide (portamento) time
// constant in seconds. Clamped to [0, 5] in Rust; default 0 = no slew
// (pitch jumps instantly — the render is identical to the glide-free
// engine). When enabled, the voice's base frequency slews exponentially
// toward the note pitch with this time constant; glide applies to legato
// hand-offs AND retriggered notes of a still-sounding voice (Minimoog
// behavior), while a voice starting from silence begins directly at its
// target pitch. Same lifecycle as SetNativeCutoffHz (no-op for other
// backends).
func SetNativeGlide(s float32) {
	C.polyclav_dsp_set_native_glide(C.float(s))
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
