package audio

// Cgo bindings for the polyclav audio-core Rust staticlib.
//
// OS split: the -I include path and the shared FFI prototypes are OS-agnostic
// and live in the cgo preamble below. The OS-specific LINK flags (the Rust
// staticlib + its per-backend libs, pkg-config, frameworks) live in
// audio_linux.go / audio_darwin.go; the nixpkgs-only -lzix-0 lives in
// zix_link.go. cgo concatenates every file's #cgo directives in the package,
// so this resolves to one link line per OS. The LV2/CLAP setters wrapped below
// are DEFINED symbols on macOS too (the Rust staticlib ships thin error stubs
// there), so they link on darwin without a per-OS Go split.

// #cgo CFLAGS: -I${SRCDIR}/../../audio-core/include
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
// void    polyclav_dsp_set_native_amp_env(float attack_s, float decay_s, float sustain, float release_s);
// void    polyclav_dsp_set_native_pulse_width(float width);
// void    polyclav_dsp_set_native_drive(float drive);
// void    polyclav_dsp_set_native_vel_routing(float to_cutoff, float to_amp);
// void    polyclav_dsp_set_native_kbd_track(float amt);
// void    polyclav_dsp_set_native_lfo(uint32_t wave, float rate_hz, float to_pitch_cents, float to_cutoff_oct, float to_amp);
// void    polyclav_dsp_set_native_bend_range(float st);
// void    polyclav_dsp_set_native_voice_mode(uint32_t mode);
// void    polyclav_dsp_set_native_oversample(uint32_t on);
import "C"

import (
	"fmt"
	"math"
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

// RenderOffline renders the native `engine` synth playing note/velocity held
// from t=0, through the full DSP chain, into an interleaved-stereo f32 buffer
// (48 kHz), opening NO audio device. Returns the samples (len nFrames*2) or an
// error. This is the device-free path behind `polyclav render` and the CI
// offline-render gate; it works identically on Linux and macOS.
func RenderOffline(engine string, note, velocity byte, nFrames int) ([]float32, error) {
	if nFrames <= 0 {
		return nil, fmt.Errorf("render offline: nFrames must be positive, got %d", nFrames)
	}
	buf := make([]float32, nFrames*2)
	cEngine := C.CString(engine)
	defer C.free(unsafe.Pointer(cEngine))
	rc := C.polyclav_render_offline(
		cEngine,
		C.uint8_t(note),
		C.uint8_t(velocity),
		(*C.float)(unsafe.Pointer(&buf[0])),
		C.uint32_t(nFrames),
	)
	switch rc {
	case 0:
		return buf, nil
	case 2:
		return nil, fmt.Errorf("render offline: unknown engine %q", engine)
	default:
		return nil, fmt.Errorf("render offline: audio-core error code %d", int(rc))
	}
}

// OfflineMIDIEventKind is the wire-format discriminator for
// OfflineMIDIEvent.Kind, matching audio-core's PolyclavMidiEvent.kind
// (polyclav_audio.h). Distinct from MIDIKind/PushMIDI's live-queue
// vocabulary below — this one is timed by absolute frame, not pushed
// one at a time in real time.
type OfflineMIDIEventKind uint8

const (
	OfflineNoteOn OfflineMIDIEventKind = iota
	OfflineNoteOff
	OfflineControlChange
	OfflinePitchBend
)

// OfflineMIDIEvent is one event for RenderOfflineEvents, timed by
// absolute frame offset from the start of the render (not a delta).
// For NoteOn/NoteOff, Data1 is the note number and Data2 the velocity
// (NoteOff ignores Data2). For ControlChange, Data1 is the controller
// and Data2 the value. For PitchBend, Data2 is the 14-bit bend value
// (Data1 unused).
type OfflineMIDIEvent struct {
	Frame   uint32
	Kind    OfflineMIDIEventKind
	Channel uint8
	Data1   uint8
	Data2   uint16
}

// RenderOfflineEvents renders an arbitrary timed MIDI event sequence
// (e.g. a parsed Standard MIDI File — see internal/measure) through any
// patch type, into an interleaved-stereo f32 buffer (48 kHz), opening
// NO audio device. events must be sorted by Frame ascending (not
// re-sorted here — see polyclav_render_offline_events's doc comment).
// patchType is one of "soundfont" (patchRef = file path, dispatches on
// extension), "native" (patchRef = engine name), "lv2" (patchRef =
// URI, Linux only), or "clap" (patchRef = bundle path, pluginID
// required, Linux only); pluginID is ignored for every other type.
func RenderOfflineEvents(patchType, patchRef, pluginID string, events []OfflineMIDIEvent, nFrames int) ([]float32, error) {
	if nFrames <= 0 {
		return nil, fmt.Errorf("render offline events: nFrames must be positive, got %d", nFrames)
	}
	buf := make([]float32, nFrames*2)

	cType := C.CString(patchType)
	defer C.free(unsafe.Pointer(cType))
	cRef := C.CString(patchRef)
	defer C.free(unsafe.Pointer(cRef))

	var cPluginID *C.char
	if pluginID != "" {
		cPluginID = C.CString(pluginID)
		defer C.free(unsafe.Pointer(cPluginID))
	}

	var cEvents *C.PolyclavMidiEvent
	if len(events) > 0 {
		cSlice := make([]C.PolyclavMidiEvent, len(events))
		for i, e := range events {
			cSlice[i] = C.PolyclavMidiEvent{
				frame:   C.uint32_t(e.Frame),
				kind:    C.uint8_t(e.Kind),
				channel: C.uint8_t(e.Channel),
				data1:   C.uint8_t(e.Data1),
				data2:   C.uint16_t(e.Data2),
			}
		}
		cEvents = &cSlice[0]
	}

	rc := C.polyclav_render_offline_events(
		cType, cRef, cPluginID,
		cEvents, C.uint32_t(len(events)),
		(*C.float)(unsafe.Pointer(&buf[0])),
		C.uint32_t(nFrames),
	)
	switch rc {
	case 0:
		return buf, nil
	case 2:
		return nil, fmt.Errorf("render offline events: unknown/unavailable patch_type %q or load failure for %q", patchType, patchRef)
	default:
		return nil, fmt.Errorf("render offline events: audio-core error code %d", int(rc))
	}
}

// MeasureLUFS returns the integrated (ungated) LUFS loudness of an
// interleaved-stereo f32 buffer at 48 kHz (ITU-R BS.1770-4 K-weighting;
// see dsp::loudness in the Rust source for exactly what this does and
// does not measure). Meant for offline analysis of a buffer from
// RenderOffline, not the real-time callback. Returns negative infinity
// for an empty buffer or true silence.
func MeasureLUFS(samples []float32) float32 {
	if len(samples) == 0 {
		return float32(math.Inf(-1))
	}
	return float32(C.polyclav_measure_lufs((*C.float)(unsafe.Pointer(&samples[0])), C.uint32_t(len(samples))))
}

// MeasurePeakDBFS returns the peak level (dBFS) of an interleaved-stereo
// f32 buffer. Same empty/silence handling as MeasureLUFS.
func MeasurePeakDBFS(samples []float32) float32 {
	if len(samples) == 0 {
		return float32(math.Inf(-1))
	}
	return float32(C.polyclav_measure_peak_dbfs((*C.float)(unsafe.Pointer(&samples[0])), C.uint32_t(len(samples))))
}

// SetLatencyFrames requests the audio buffer size in frames — polyclav's
// own latency knob. Clamped to [16, 8192] in Rust; 0 selects the default
// (128, ~2.7 ms at 48 kHz). It is a request, not a guarantee: the effective
// buffer never drops below what the platform supports (the PipeWire graph
// quantum on Linux, the device's minimum buffer on macOS), so the real
// latency is "this many frames, or the platform minimum, whichever is
// larger". Must be called before Start; the value is read once when the
// audio thread starts.
func SetLatencyFrames(frames int) {
	if frames < 0 {
		frames = 0
	}
	C.polyclav_audio_set_latency_frames(C.uint32_t(frames))
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

// SetDrivePedal amount in [0.0, 1.0]. 0 = bypass, 1 = maximum drive.
// Clamped in Rust. Runs in the shared post-synth DSP chain, so it
// applies to every synth backend (soundfont, sfizz, LV2, CLAP, native).
func SetDrivePedal(v float32) {
	C.polyclav_dsp_set_drive_pedal(C.float(v))
}

// SetAnalogDelayTimeMs sets the analog-delay time in milliseconds.
// Clamped to [1, 1000] in Rust.
func SetAnalogDelayTimeMs(ms float32) {
	C.polyclav_dsp_set_analog_delay_time_ms(C.float(ms))
}

// SetAnalogDelayFeedback sets the analog-delay feedback (repeats)
// amount. Clamped to [0.0, 0.9] in Rust — capped below unity so the
// pedal stays a delay, not a deliberate self-oscillator.
func SetAnalogDelayFeedback(v float32) {
	C.polyclav_dsp_set_analog_delay_feedback(C.float(v))
}

// SetAnalogDelayMix sets the analog-delay wet/dry mix in [0.0, 1.0].
// 0 = bypass, 1 = fully wet. Clamped in Rust. Runs in the shared
// post-synth DSP chain, so it applies to every synth backend.
func SetAnalogDelayMix(v float32) {
	C.polyclav_dsp_set_analog_delay_mix(C.float(v))
}

// SetNativeCutoffHz pushes the active native synth's filter cutoff. The
// audio thread reads the atomic per block and applies it to the active
// SynthBackend::Native (no-op for other backends). Clamped to
// [20, 20000] in Rust. Pushed from the FILTER page's Cutoff knob (MAIN
// knob 4 now drives the drive pedal instead).
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

// SetNativeAmpEnv pushes the active native synth's amp-envelope (env 1)
// ADSR. Same lifecycle as SetNativeCutoffHz: the audio thread reads the
// atomics per block and applies them to the active SynthBackend::Native
// (no-op for other backends). Times are clamped to [0.0001, 10] s,
// sustain to [0, 1] in Rust. Updating params does not disturb a running
// envelope. Defaults: 5 ms / 200 ms / 0.7 / 400 ms — exactly the
// previously-hardcoded values, so the default render is unchanged.
func SetNativeAmpEnv(a, d, s, r float32) {
	C.polyclav_dsp_set_native_amp_env(C.float(a), C.float(d), C.float(s), C.float(r))
}

// SetNativePulseWidth pushes the active native synth's pulse-wave duty
// cycle. One global knob shared by all three oscillators; only audible
// while a pulse waveform is selected. Clamped to [0.05, 0.95] in Rust;
// default 0.25 (the old fixed duty — render unchanged at the default).
// Same lifecycle as SetNativeCutoffHz (no-op for other backends).
func SetNativePulseWidth(w float32) {
	C.polyclav_dsp_set_native_pulse_width(C.float(w))
}

// SetNativeDrive pushes the active native synth's pre-filter tanh drive
// amount. Clamped to [0, 1] in Rust; default 0 = bit-exact bypass. When
// > 0 the post-mixer signal is shaped by tanh(x*g) / tanh(g) with
// g = 1+drive*4 before the ladder filter — peak-referenced
// normalization: unity at |x| = 1, small-signal gain g/tanh(g) >= 1
// (drive adds loudness + compression instead of dropping the level).
// Same lifecycle as SetNativeCutoffHz (no-op for other backends).
func SetNativeDrive(drive float32) {
	C.polyclav_dsp_set_native_drive(C.float(drive))
}

// SetNativeVelRouting pushes the active native synth's velocity-routing
// amounts, both clamped to [0, 1] in Rust. toAmp scales the per-note
// amplitude: scale = lerp(1.0, vel/127, toAmp) — default 1 is exactly
// the classic vel/127 (render unchanged); 0 ignores velocity. toCutoff
// modulates the effective filter cutoff by
// 2^(toCutoff*(vel/127-0.5)*2) — up to +/-1 octave around the knob
// cutoff, centered at velocity 64; default 0 = bypass. Both are
// captured per voice at note-on (knob turns mid-note affect the next
// note). Composes multiplicatively with the filter-env and
// keyboard-tracking cutoff modulation; the final effective cutoff is
// clamped to [20, 20000] Hz. Same lifecycle as SetNativeCutoffHz
// (no-op for other backends).
func SetNativeVelRouting(toCutoff, toAmp float32) {
	C.polyclav_dsp_set_native_vel_routing(C.float(toCutoff), C.float(toAmp))
}

// SetNativeKbdTrack pushes the active native synth's keyboard-tracking
// amount, clamped to [0, 1] in Rust. The effective cutoff is multiplied
// by 2^(amt*(note-60)/12) — at 1 it tracks the keyboard 100% (2x per
// octave above middle C, /2 per octave below), following the sounding
// note (legato hand-offs included). Default 0 = bypass. Composes
// multiplicatively with the filter-env and velocity cutoff modulation;
// the final effective cutoff is clamped to [20, 20000] Hz. Same
// lifecycle as SetNativeCutoffHz (no-op for other backends).
func SetNativeKbdTrack(amt float32) {
	C.polyclav_dsp_set_native_kbd_track(C.float(amt))
}

// SetNativeLFO pushes the active native synth's GLOBAL LFO (one LFO
// shared across voices, advanced once per sample). wave is "triangle",
// "saw", "square", or "sh" (sample-and-hold: a deterministic xorshift
// stepped once per LFO cycle). rateHz is clamped to [0.05, 20] in Rust
// (default 5). The three depths all default to 0 = bit-transparent
// bypass:
//
//   - toPitchCents [0, 100]: vibrato — the voice frequency is scaled by
//     2^(lfo*cents/1200). The depth heard is additionally scaled LIVE
//     by MIDI CC 1 (mod wheel): wheel and configured depth MULTIPLY,
//     and the synth boots with the wheel at 1.0 so a configured depth
//     is audible without a physical wheel; the first CC 1 event then
//     takes over (wheel 0 silences vibrato — classic vibrato-on-wheel).
//   - toCutoffOct [0, 2]: the effective filter cutoff is scaled by
//     2^(lfo*oct), composing multiplicatively with the env/vel/kbd
//     cutoff modulation (final cutoff clamped to [20, 20000] Hz).
//   - toAmp [0, 1]: tremolo — output * (1 - depth*(lfo*0.5+0.5)).
//
// Same lifecycle as SetNativeCutoffHz (no-op for other backends).
// Returns an error for an unknown wave name.
func SetNativeLFO(wave string, rateHz, toPitchCents, toCutoffOct, toAmp float32) error {
	var w uint32
	switch wave {
	case "triangle":
		w = 0
	case "saw":
		w = 1
	case "square":
		w = 2
	case "sh":
		w = 3
	default:
		return fmt.Errorf("audio-core set native lfo: unknown wave %q (valid: triangle, saw, square, sh)", wave)
	}
	C.polyclav_dsp_set_native_lfo(C.uint32_t(w), C.float(rateHz), C.float(toPitchCents), C.float(toCutoffOct), C.float(toAmp))
	return nil
}

// SetNativeBendRange pushes the active native synth's pitch-bend range
// in semitones at full wheel deflection, clamped to [0, 12] in Rust;
// default 2 (the MIDI convention). Incoming PushMIDI pitch-bend events
// (14-bit Bend value, 8192 = centre) scale the voice frequency by
// 2^(range * (bend-8192)/8192 / 12); with no bend event the factor is
// exactly 1 and the render is unchanged. Same lifecycle as
// SetNativeCutoffHz (no-op for other backends).
func SetNativeBendRange(st float32) {
	C.polyclav_dsp_set_native_bend_range(C.float(st))
}

// SetNativeVoiceMode selects the native synth's voice-allocation mode
// (ROADMAP §1.2 / §1.5):
//
//   - "mono_legato" (the default): 1 voice, last-note priority,
//     envelopes only retrigger when no other key is held. The render at
//     this default is bit-identical to the pre-poly engine.
//   - "mono_retrig": 1 voice, last-note priority, envelopes ALWAYS
//     retrigger on note-on.
//   - "poly": 8 voices. A note-on takes a free voice (amp envelope
//     idle), else steals the oldest voice already in its release tail,
//     else the oldest held voice; a note-off releases exactly the
//     voice(s) sounding that note.
//
// Switching modes while notes sound releases every voice and clears the
// held-notes bookkeeping (no stuck notes — keys already down fade out
// through their release tails and must be re-pressed). Same lifecycle
// as SetNativeCutoffHz (no-op for other backends). Returns an error for
// an unknown mode name.
func SetNativeVoiceMode(mode string) error {
	var m uint32
	switch mode {
	case "mono_legato":
		m = 0
	case "mono_retrig":
		m = 1
	case "poly":
		m = 2
	default:
		return fmt.Errorf("audio-core set native voice mode: unknown mode %q (valid: mono_legato, mono_retrig, poly)", mode)
	}
	C.polyclav_dsp_set_native_voice_mode(C.uint32_t(m))
	return nil
}

// SetNativeOversample enables/disables 2x oversampling of the native
// synth's per-voice nonlinear section (tanh drive + Moog ladder). Off
// (the default) is the base-rate path, bit-identical to the
// pre-oversampling engine. On, the mixer output is upsampled 2x through
// a minimum-phase halfband, the drive + ladder run (retuned) at twice
// the sample rate, and the same halfband decimates back — removing the
// tanh stages' fold-back aliasing under hard drive. Toggling while
// notes sound swaps per-voice filter instances (reset + retuned): a
// brief click may be audible, so treat it as a setup switch, not a
// performance control. Same lifecycle as SetNativeCutoffHz (no-op for
// other backends).
func SetNativeOversample(on bool) {
	var v C.uint32_t
	if on {
		v = 1
	}
	C.polyclav_dsp_set_native_oversample(v)
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
