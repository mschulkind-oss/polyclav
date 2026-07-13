use std::ffi::CStr;
use std::fs::File;
use std::os::raw::c_char;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU32, AtomicU64, Ordering};
use std::sync::mpsc;
use std::sync::{Arc, Mutex, OnceLock};
use std::thread;
use std::time::Duration;

// PipeWire is the Linux audio backend; on macOS the CoreAudio backend
// (backend_macos, cpal) replaces all of this. Gated so a macOS build neither
// links pipewire nor trips unused-import errors.
#[cfg(target_os = "linux")]
use pipewire as pw;
#[cfg(target_os = "linux")]
use pw::{
    context::ContextRc,
    main_loop::MainLoopRc,
    properties::properties,
    spa::{
        param::audio::{AudioFormat, AudioInfoRaw},
        pod::{self, serialize::PodSerializer},
        sys,
        utils::Direction,
    },
    stream::{StreamBox, StreamFlags},
};

use crossbeam_queue::ArrayQueue;
use oxisynth::{
    MidiEvent as OxiMidiEvent, SoundFont as OxiSoundFont, Synth as OxiSynth, SynthDescriptor,
};

use crate::dsp::{AnalogDelay, Compressor, DrivePedal, Limiter, MasteringCompressor, Reverb};
#[cfg(target_os = "linux")]
use crate::plugin_clap::ClapInstance;
#[cfg(target_os = "linux")]
use crate::plugin_lv2::LvInstance;
use crate::synth::NativeSynth;

mod dsp;
// CoreAudio output backend (cpal); the macOS analog of the PipeWire path.
#[cfg(target_os = "macos")]
mod backend_macos;
#[cfg(target_os = "linux")]
mod plugin_clap;
#[cfg(target_os = "linux")]
mod plugin_lv2;
mod sfizz;
mod sfizz_sys;
mod synth;

pub(crate) const SAMPLE_RATE: f32 = 48000.0;
const MAX_QUANTUM: usize = 8192;
/// Absolute floor for the requested audio buffer size (frames). The real
/// minimum is whatever the audio graph (PipeWire on Linux) or output device
/// (CoreAudio on macOS) actually supports; this is only a sanity clamp so a
/// bogus config can't request a pathological quantum. See
/// `polyclav_audio_set_latency_frames`.
const MIN_QUANTUM: u32 = 16;
/// Default audio buffer size (frames) when none is configured — ~2.7 ms at
/// 48 kHz, the historical polyclav quantum.
const DEFAULT_QUANTUM: u32 = 128;

pub(crate) enum SynthBackend {
    Oxi(Box<OxiSynth>),
    Sfizz(sfizz::Sfizz),
    #[cfg(target_os = "linux")]
    Lv2(Box<LvInstance>),
    #[cfg(target_os = "linux")]
    Clap(Box<ClapInstance>),
    Native(Box<NativeSynth>),
}

impl SynthBackend {
    fn load(path: &Path) -> Result<Self, String> {
        let ext = path
            .extension()
            .and_then(|e| e.to_str())
            .map(|s| s.to_ascii_lowercase());
        match ext.as_deref() {
            Some("sfz") => {
                let s = sfizz::Sfizz::load(path, SAMPLE_RATE, MAX_QUANTUM)?;
                eprintln!("audio-core: soundfont loaded via sfizz ({path:?})");
                Ok(SynthBackend::Sfizz(s))
            }
            _ => {
                let settings = SynthDescriptor {
                    sample_rate: SAMPLE_RATE,
                    gain: 1.0,
                    ..Default::default()
                };
                let mut synth =
                    OxiSynth::new(settings).map_err(|e| format!("oxisynth init: {e:?}"))?;
                let mut file = File::open(path).map_err(|e| format!("open {path:?}: {e}"))?;
                let font =
                    OxiSoundFont::load(&mut file).map_err(|e| format!("oxisynth load: {e:?}"))?;
                synth.add_font(font, true);
                eprintln!("audio-core: soundfont loaded via oxisynth ({path:?})");
                Ok(SynthBackend::Oxi(Box::new(synth)))
            }
        }
    }

    #[cfg(target_os = "linux")]
    fn load_lv2(uri: &str) -> Result<Self, String> {
        let inst = LvInstance::load(uri, f64::from(SAMPLE_RATE), MAX_QUANTUM)?;
        eprintln!("audio-core: LV2 plugin loaded ({uri})");
        Ok(SynthBackend::Lv2(Box::new(inst)))
    }

    #[cfg(target_os = "linux")]
    fn load_clap(bundle_path: &Path, plugin_id: &str) -> Result<Self, String> {
        let inst = ClapInstance::load(bundle_path, plugin_id, f64::from(SAMPLE_RATE), MAX_QUANTUM)?;
        eprintln!(
            "audio-core: CLAP plugin loaded ({} :: {plugin_id})",
            bundle_path.display()
        );
        Ok(SynthBackend::Clap(Box::new(inst)))
    }

    fn load_native(engine: &str) -> Result<Self, String> {
        let synth = NativeSynth::new(engine, SAMPLE_RATE)?;
        eprintln!("audio-core: native synth loaded (engine={engine})");
        Ok(SynthBackend::Native(Box::new(synth)))
    }

    fn name(&self) -> &'static str {
        match self {
            SynthBackend::Oxi(_) => "oxisynth",
            SynthBackend::Sfizz(_) => "sfizz",
            #[cfg(target_os = "linux")]
            SynthBackend::Lv2(_) => "lv2",
            #[cfg(target_os = "linux")]
            SynthBackend::Clap(_) => "clap",
            SynthBackend::Native(_) => "native",
        }
    }
}

enum MidiEvent {
    NoteOn {
        channel: u8,
        note: u8,
        velocity: u8,
    },
    NoteOff {
        channel: u8,
        note: u8,
    },
    ControlChange {
        channel: u8,
        controller: u8,
        value: u8,
    },
    PitchBend {
        channel: u8,
        bend: u16,
    },
}

static MIDI_QUEUE: OnceLock<Arc<ArrayQueue<MidiEvent>>> = OnceLock::new();
fn midi_queue() -> &'static Arc<ArrayQueue<MidiEvent>> {
    MIDI_QUEUE.get_or_init(|| Arc::new(ArrayQueue::new(1024)))
}

static SOUNDFONT_PATH: OnceLock<Mutex<Option<PathBuf>>> = OnceLock::new();
fn soundfont_path_cell() -> &'static Mutex<Option<PathBuf>> {
    SOUNDFONT_PATH.get_or_init(|| Mutex::new(None))
}

/// Monotonic generation counter, bumped on each `polyclav_audio_reload_soundfont`
/// call. Workers capture the current value when they start; the audio thread
/// discards loads whose captured generation is older than the latest, so rapid
/// pad-spam can't cause the audio to swap to a stale soundfont.
static SOUNDFONT_GENERATION: AtomicU64 = AtomicU64::new(0);

/// Requested audio buffer size in frames — polyclav's own latency knob,
/// read once when the audio thread starts. `0` is normalized to
/// [`DEFAULT_QUANTUM`] by the FFI setter. On Linux it becomes the PipeWire
/// `node.latency` "<frames>/48000" hint; on macOS the CoreAudio backend
/// clamps it up to the device's minimum supported buffer size and sets
/// `kAudioDevicePropertyBufferFrameSize`. Set it before
/// `polyclav_audio_start`; changing it afterward has no effect until the
/// next start.
pub(crate) static LATENCY_FRAMES: AtomicU32 = AtomicU32::new(DEFAULT_QUANTUM);

/// Queue of preloaded synth backends ready to be swapped into the audio
/// thread. Capacity is small — we only ever expect one pending reload, but
/// allow a few to coalesce if the user spams reload.
static SYNTH_RELOAD_QUEUE: OnceLock<Arc<ArrayQueue<(u64, SynthBackend)>>> = OnceLock::new();
fn synth_reload_queue() -> &'static Arc<ArrayQueue<(u64, SynthBackend)>> {
    SYNTH_RELOAD_QUEUE.get_or_init(|| Arc::new(ArrayQueue::new(4)))
}

/// Lock-free DSP parameter struct, read by the audio thread every callback,
/// written by Go via the C ABI setters. All values are `f32::to_bits` in 0..=1.
pub(crate) struct DspParams {
    master_volume: AtomicU32,
    comp_amount: AtomicU32,
    reverb_mix: AtomicU32,
    patch_gain: AtomicU32,
    mastering_amount: AtomicU32,
    limiter_ceiling_db: AtomicU32,
    /// Drive-pedal amount in [0, 1], f32 bits. Default 0.0 — the stage
    /// is bypassed bit-exactly (regression guarantee), matching every
    /// other knob's default-off convention. Runs in the shared
    /// post-synth chain (`dsp::DrivePedal`), not inside any one synth
    /// backend, so it applies regardless of which backend is active.
    /// See docs/OPEN_SOUND_ENGINES.md §1.
    drive_pedal_amount: AtomicU32,
    /// Analog-delay time in milliseconds, f32 bits. Default 300 ms —
    /// only audible once `analog_delay_mix` is above 0. Runs in the
    /// shared post-synth chain (`dsp::AnalogDelay`), same lifecycle as
    /// `drive_pedal_amount` above.
    analog_delay_time_ms: AtomicU32,
    /// Analog-delay feedback (repeats) amount in [0, 0.9], f32 bits.
    /// Default 0.0 — a single echo, no repeats.
    analog_delay_feedback: AtomicU32,
    /// Analog-delay wet/dry mix in [0, 1], f32 bits. Default 0.0 — the
    /// stage is bypassed bit-exactly (regression guarantee), matching
    /// every other knob's default-off convention.
    analog_delay_mix: AtomicU32,
    /// Native synth filter cutoff in Hz, written by Go whenever the
    /// cutoff control (FILTER page knob 1) is adjusted — MAIN knob 4 was
    /// reassigned to the drive pedal above. The audio thread reads this
    /// once per block and pushes it to the
    /// `SynthBackend::Native` if loaded; harmless when other backends
    /// are active. Default 2 kHz matches the Minimoog factory patch.
    native_cutoff_hz: AtomicU32,
    /// Native synth filter resonance (Q), same read/push lifecycle as
    /// `native_cutoff_hz`. Default 0.3 matches the Minimoog factory
    /// patch; clamped to [0.0, 0.95] to keep headroom below the
    /// self-oscillation instability of the Stilson/Smith ladder.
    native_resonance: AtomicU32,
    /// Native synth filter-envelope (env 2) ADSR + amount, same
    /// read/push lifecycle as `native_cutoff_hz`. Times in seconds
    /// clamped to [0.0001, 10]; sustain and amount in [0, 1]. Defaults
    /// are the ROADMAP §1.4 filter ADSR (5 ms / 600 ms / 0.4 / 600 ms)
    /// with amount 0.0 — OFF — so the default render is bit-identical
    /// to the pre-env-2 engine (§1.4's factory "+30%" amount is a patch
    /// value, applied when the patch loader lands).
    native_filter_env_attack_s: AtomicU32,
    native_filter_env_decay_s: AtomicU32,
    native_filter_env_sustain: AtomicU32,
    native_filter_env_release_s: AtomicU32,
    native_filter_env_amount: AtomicU32,
    /// Native synth amp-envelope (env 1) ADSR, same read/push lifecycle
    /// as `native_cutoff_hz`. Times in seconds clamped to [0.0001, 10];
    /// sustain in [0, 1]. Defaults are the ROADMAP §1.4 amp ADSR (5 ms
    /// / 200 ms / 0.7 / 400 ms) — exactly the values the voice
    /// hardcoded before the amp env became a runtime parameter, so the
    /// default render is bit-identical (regression guarantee).
    native_amp_env_attack_s: AtomicU32,
    native_amp_env_decay_s: AtomicU32,
    native_amp_env_sustain: AtomicU32,
    native_amp_env_release_s: AtomicU32,
    /// Native synth oscillator bank (stage 3), same read/push lifecycle
    /// as `native_cutoff_hz`. Per oscillator: waveform as its 0/1/2
    /// wire code (saw/square/pulse), octave shift as i32 bits (via `as
    /// u32` cast, clamped [-2, 2]), detune in cents as f32 bits
    /// (clamped [-100, 100]), and mixer level as f32 bits (clamped
    /// [0, 1]). Defaults keep osc 2/3 silent (level 0) but pre-dial the
    /// Moog-ish offsets: osc 1 saw/0/0/1.0, osc 2 saw/0/-7¢/0.0, osc 3
    /// saw/-1 oct/+5¢/0.0 — so the default render stays bit-identical
    /// to the single-osc engine (regression guarantee) while turning a
    /// level up immediately sounds right.
    native_osc_wave: [AtomicU32; 3],
    native_osc_octave: [AtomicU32; 3],
    native_osc_detune_cents: [AtomicU32; 3],
    native_osc_level: [AtomicU32; 3],
    /// Native synth white-noise mixer level, f32 bits in [0, 1].
    /// Default 0.0 — silent (regression-safe).
    native_noise_level: AtomicU32,
    /// Native synth glide (portamento) time constant in seconds, f32
    /// bits, clamped [0, 5]. Same read/push lifecycle as
    /// `native_cutoff_hz`. Default 0.0 — no slew, pitch jumps
    /// instantly, so the default render stays bit-identical to the
    /// pre-glide engine (regression guarantee). When enabled, the
    /// voice's base frequency slews toward the note pitch with this
    /// exponential time constant on every transition of a
    /// continuously-sounding voice (legato AND retrigger — Minimoog
    /// behavior); a voice starting from silence begins at its target
    /// pitch.
    native_glide_s: AtomicU32,
    /// Native synth pulse-wave duty cycle, f32 bits, clamped
    /// [0.05, 0.95]. One global knob shared by all three oscillators;
    /// only audible while a pulse waveform is selected. Default 0.25 —
    /// exactly the old fixed stage-3 duty, so the default render is
    /// bit-identical (regression guarantee).
    native_pulse_width: AtomicU32,
    /// Native synth pre-filter tanh drive amount, f32 bits, clamped
    /// [0, 1]. Default 0.0 — the drive stage is bypassed bit-exactly
    /// (regression guarantee). When > 0 the post-mixer signal is shaped
    /// by `tanh(x * (1 + drive*4)) / (1 + drive*4)` before the ladder
    /// (unity gain at small signals; ROADMAP §1.1 "TANH/SOFTCLIP
    /// DRIVE"). §1.4's factory 0.3 is a patch value, applied when the
    /// patch loader lands.
    native_drive: AtomicU32,
    /// Native synth velocity → cutoff routing amount, f32 bits, clamped
    /// [0, 1]. Default 0.0 — bypass (bit-transparent). At 1 the
    /// effective cutoff swings up to ±1 octave around the knob cutoff,
    /// centered at velocity 64 (`2^(amt * (vel/127 - 0.5) * 2)`),
    /// captured per voice at note_on. See `synth::voice::Voice::tick`
    /// for the canonical effective-cutoff formula.
    native_vel_to_cutoff: AtomicU32,
    /// Native synth velocity → amp routing amount, f32 bits, clamped
    /// [0, 1]. Default 1.0 — exactly the classic `velocity / 127` amp
    /// scaling (regression guarantee); 0 ignores velocity (full
    /// amplitude for every note). The scale is
    /// `lerp(1.0, vel/127, amt)`, captured per voice at note_on.
    native_vel_to_amp: AtomicU32,
    /// Native synth keyboard-tracking amount, f32 bits, clamped [0, 1].
    /// Default 0.0 — bypass (bit-transparent). At 1 the effective
    /// cutoff tracks the keyboard at 100%
    /// (`2^(amt * (note - 60) / 12)`), following the sounding note.
    /// §1.4's factory 0.5 is a patch value, applied when the patch
    /// loader lands.
    native_kbd_track: AtomicU32,
    /// Native synth GLOBAL LFO (ROADMAP §1.1 GLOBAL block), same
    /// read/push lifecycle as `native_cutoff_hz`. `wave` is the raw
    /// 0/1/2/3 wire code (triangle/saw/square/S&H — validated at the
    /// FFI boundary); `rate_hz` is f32 bits clamped [0.05, 20]
    /// (default 5.0); the three depths are f32 bits — `to_pitch` in
    /// cents [0, 100] (vibrato, scaled live by MIDI CC 1; the synth
    /// boots with the wheel at 1.0 so a configured depth is audible
    /// without a wheel), `to_cutoff` in octaves [0, 2], `to_amp`
    /// (tremolo) in [0, 1]. All depths default 0.0 — every modulation
    /// factor is exactly 1.0 and the default render is bit-identical
    /// to the LFO-free engine (regression guarantee).
    native_lfo_wave: AtomicU32,
    native_lfo_rate_hz: AtomicU32,
    native_lfo_to_pitch_cents: AtomicU32,
    native_lfo_to_cutoff_oct: AtomicU32,
    native_lfo_to_amp: AtomicU32,
    /// Native synth pitch-bend range in semitones at full deflection,
    /// f32 bits, clamped [0, 12]. Default 2.0 (the MIDI convention).
    /// The bend factor `2^(range * norm / 12)` is exactly 1.0 with no
    /// bend event, so the default render is unchanged. Same read/push
    /// lifecycle as `native_cutoff_hz`.
    native_bend_range_semitones: AtomicU32,
    /// Native synth voice-allocation mode (ROADMAP §1.2 / §1.5) as the
    /// raw 0/1/2 wire code: 0 = mono_legato (DEFAULT — the historic
    /// behavior, bit for bit), 1 = mono_retrig, 2 = poly (8 voices,
    /// oldest-fired steal). Validated at the FFI boundary (capped as a
    /// backstop here). Same read/push lifecycle as `native_cutoff_hz`;
    /// the synth-side push is change-detected and releases all voices
    /// on an actual switch (see `synth::NativeSynth::set_voice_mode`).
    native_voice_mode: AtomicU32,
    /// Native synth 2× oversampling of the per-voice nonlinear section
    /// (tanh drive + Moog ladder) as a raw 0/1 wire code (ROADMAP §0.1
    /// / §1.6 / Appendix A pivot item (a)). 0 (the DEFAULT) keeps the
    /// base-rate path — bit-identical to the historic engine
    /// (regression guarantee). 1 routes the mixer output through a
    /// halfband up/down wrapper whose drive + ladder run (and are
    /// retuned) at sample_rate × 2, taming the tanh stages' fold-back
    /// aliasing when driven hard. Validated at the FFI boundary (capped
    /// as a backstop here). Same read/push lifecycle as
    /// `native_cutoff_hz`; the voice-side push is change-detected and
    /// an actual toggle swaps filter instances with a brief documented
    /// click risk (see `synth::voice::Voice::set_oversample`).
    native_oversample: AtomicU32,
}

impl DspParams {
    fn new() -> Self {
        Self {
            master_volume: AtomicU32::new(1.0_f32.to_bits()),
            comp_amount: AtomicU32::new(0.0_f32.to_bits()),
            reverb_mix: AtomicU32::new(0.0_f32.to_bits()),
            patch_gain: AtomicU32::new(1.0_f32.to_bits()),
            mastering_amount: AtomicU32::new(0.0_f32.to_bits()),
            limiter_ceiling_db: AtomicU32::new((-0.3_f32).to_bits()),
            drive_pedal_amount: AtomicU32::new(0.0_f32.to_bits()),
            analog_delay_time_ms: AtomicU32::new(300.0_f32.to_bits()),
            analog_delay_feedback: AtomicU32::new(0.0_f32.to_bits()),
            analog_delay_mix: AtomicU32::new(0.0_f32.to_bits()),
            native_cutoff_hz: AtomicU32::new(2_000.0_f32.to_bits()),
            native_resonance: AtomicU32::new(0.3_f32.to_bits()),
            native_filter_env_attack_s: AtomicU32::new(0.005_f32.to_bits()),
            native_filter_env_decay_s: AtomicU32::new(0.6_f32.to_bits()),
            native_filter_env_sustain: AtomicU32::new(0.4_f32.to_bits()),
            native_filter_env_release_s: AtomicU32::new(0.6_f32.to_bits()),
            native_filter_env_amount: AtomicU32::new(0.0_f32.to_bits()),
            native_amp_env_attack_s: AtomicU32::new(0.005_f32.to_bits()),
            native_amp_env_decay_s: AtomicU32::new(0.2_f32.to_bits()),
            native_amp_env_sustain: AtomicU32::new(0.7_f32.to_bits()),
            native_amp_env_release_s: AtomicU32::new(0.4_f32.to_bits()),
            native_osc_wave: [AtomicU32::new(0), AtomicU32::new(0), AtomicU32::new(0)],
            native_osc_octave: [
                AtomicU32::new(0i32 as u32),
                AtomicU32::new(0i32 as u32),
                AtomicU32::new((-1i32) as u32),
            ],
            native_osc_detune_cents: [
                AtomicU32::new(0.0_f32.to_bits()),
                AtomicU32::new((-7.0_f32).to_bits()),
                AtomicU32::new(5.0_f32.to_bits()),
            ],
            native_osc_level: [
                AtomicU32::new(1.0_f32.to_bits()),
                AtomicU32::new(0.0_f32.to_bits()),
                AtomicU32::new(0.0_f32.to_bits()),
            ],
            native_noise_level: AtomicU32::new(0.0_f32.to_bits()),
            native_glide_s: AtomicU32::new(0.0_f32.to_bits()),
            native_pulse_width: AtomicU32::new(0.25_f32.to_bits()),
            native_drive: AtomicU32::new(0.0_f32.to_bits()),
            native_vel_to_cutoff: AtomicU32::new(0.0_f32.to_bits()),
            native_vel_to_amp: AtomicU32::new(1.0_f32.to_bits()),
            native_kbd_track: AtomicU32::new(0.0_f32.to_bits()),
            native_lfo_wave: AtomicU32::new(0), // triangle
            native_lfo_rate_hz: AtomicU32::new(5.0_f32.to_bits()),
            native_lfo_to_pitch_cents: AtomicU32::new(0.0_f32.to_bits()),
            native_lfo_to_cutoff_oct: AtomicU32::new(0.0_f32.to_bits()),
            native_lfo_to_amp: AtomicU32::new(0.0_f32.to_bits()),
            native_bend_range_semitones: AtomicU32::new(2.0_f32.to_bits()),
            native_voice_mode: AtomicU32::new(0), // mono_legato
            native_oversample: AtomicU32::new(0), // off (regression-safe)
        }
    }

    /// Store `v` clamped to `[lo, hi]`. Non-finite inputs (NaN, ±inf)
    /// are rejected and the slot keeps its previous value. This is the
    /// single ingestion choke point for every f32 crossing the
    /// `polyclav_dsp_set_*` C ABI: `f32::clamp` propagates NaN, so one
    /// NaN through any FFI setter would otherwise permanently poison
    /// downstream DSP state (ladder/glide/mixer). ±inf is rejected
    /// rather than clamped by the same rule — a non-finite knob value
    /// is always a caller bug, and "ignore garbage, keep the last good
    /// value" is the least surprising recovery at an FFI boundary.
    fn store_clamped(slot: &AtomicU32, v: f32, lo: f32, hi: f32) {
        if !v.is_finite() {
            return;
        }
        let clamped = v.clamp(lo, hi);
        slot.store(clamped.to_bits(), Ordering::Relaxed);
    }

    fn store(slot: &AtomicU32, v: f32) {
        Self::store_clamped(slot, v, 0.0, 1.0);
    }

    fn load(slot: &AtomicU32) -> f32 {
        f32::from_bits(slot.load(Ordering::Relaxed))
    }

    pub fn master_volume(&self) -> f32 {
        Self::load(&self.master_volume)
    }
    pub fn comp_amount(&self) -> f32 {
        Self::load(&self.comp_amount)
    }
    pub fn reverb_mix(&self) -> f32 {
        Self::load(&self.reverb_mix)
    }
    pub fn patch_gain(&self) -> f32 {
        Self::load(&self.patch_gain)
    }
    pub fn mastering_amount(&self) -> f32 {
        Self::load(&self.mastering_amount)
    }
    pub fn limiter_ceiling_db(&self) -> f32 {
        Self::load(&self.limiter_ceiling_db)
    }
    pub fn drive_pedal_amount(&self) -> f32 {
        Self::load(&self.drive_pedal_amount)
    }
    pub fn analog_delay_time_ms(&self) -> f32 {
        Self::load(&self.analog_delay_time_ms)
    }
    pub fn analog_delay_feedback(&self) -> f32 {
        Self::load(&self.analog_delay_feedback)
    }
    pub fn analog_delay_mix(&self) -> f32 {
        Self::load(&self.analog_delay_mix)
    }
    pub fn native_cutoff_hz(&self) -> f32 {
        Self::load(&self.native_cutoff_hz)
    }
    pub fn native_resonance(&self) -> f32 {
        Self::load(&self.native_resonance)
    }
    /// Filter-envelope params as (attack_s, decay_s, sustain, release_s,
    /// amount). Read once per audio block. The five atomics are stored
    /// individually, so a concurrent setter can tear across fields for
    /// one block — harmless for advisory knob values.
    pub fn native_filter_env(&self) -> (f32, f32, f32, f32, f32) {
        (
            Self::load(&self.native_filter_env_attack_s),
            Self::load(&self.native_filter_env_decay_s),
            Self::load(&self.native_filter_env_sustain),
            Self::load(&self.native_filter_env_release_s),
            Self::load(&self.native_filter_env_amount),
        )
    }

    /// Amp-envelope params as (attack_s, decay_s, sustain, release_s).
    /// Read once per audio block. The four atomics are stored
    /// individually, so a concurrent setter can tear across fields for
    /// one block — harmless for advisory knob values.
    pub fn native_amp_env(&self) -> (f32, f32, f32, f32) {
        (
            Self::load(&self.native_amp_env_attack_s),
            Self::load(&self.native_amp_env_decay_s),
            Self::load(&self.native_amp_env_sustain),
            Self::load(&self.native_amp_env_release_s),
        )
    }

    /// One oscillator's params as (wave, octave, detune_cents, level).
    /// Read once per audio block. The four atomics are stored
    /// individually, so a concurrent setter can tear across fields for
    /// one block — harmless for advisory knob values. `idx` must be
    /// 0..=2 (validated at the FFI boundary; the audio thread iterates
    /// a constant range).
    pub fn native_osc(&self, idx: usize) -> (u32, i32, f32, f32) {
        (
            self.native_osc_wave[idx].load(Ordering::Relaxed),
            self.native_osc_octave[idx].load(Ordering::Relaxed) as i32,
            Self::load(&self.native_osc_detune_cents[idx]),
            Self::load(&self.native_osc_level[idx]),
        )
    }
    pub fn native_noise_level(&self) -> f32 {
        Self::load(&self.native_noise_level)
    }
    pub fn native_glide_s(&self) -> f32 {
        Self::load(&self.native_glide_s)
    }
    pub fn native_pulse_width(&self) -> f32 {
        Self::load(&self.native_pulse_width)
    }
    pub fn native_drive(&self) -> f32 {
        Self::load(&self.native_drive)
    }
    /// Velocity-routing amounts as (to_cutoff, to_amp). Read once per
    /// audio block. The two atomics are stored individually, so a
    /// concurrent setter can tear across fields for one block —
    /// harmless for advisory knob values.
    pub fn native_vel_routing(&self) -> (f32, f32) {
        (
            Self::load(&self.native_vel_to_cutoff),
            Self::load(&self.native_vel_to_amp),
        )
    }
    pub fn native_kbd_track(&self) -> f32 {
        Self::load(&self.native_kbd_track)
    }
    /// GLOBAL LFO params as (wave, rate_hz, to_pitch_cents,
    /// to_cutoff_oct, to_amp). Read once per audio block. The five
    /// atomics are stored individually, so a concurrent setter can tear
    /// across fields for one block — harmless for advisory knob values.
    pub fn native_lfo(&self) -> (u32, f32, f32, f32, f32) {
        (
            self.native_lfo_wave.load(Ordering::Relaxed),
            Self::load(&self.native_lfo_rate_hz),
            Self::load(&self.native_lfo_to_pitch_cents),
            Self::load(&self.native_lfo_to_cutoff_oct),
            Self::load(&self.native_lfo_to_amp),
        )
    }
    pub fn native_bend_range_semitones(&self) -> f32 {
        Self::load(&self.native_bend_range_semitones)
    }
    pub fn native_voice_mode(&self) -> u32 {
        self.native_voice_mode.load(Ordering::Relaxed)
    }
    pub fn native_oversample(&self) -> u32 {
        self.native_oversample.load(Ordering::Relaxed)
    }

    pub fn set_master_volume(&self, v: f32) {
        Self::store(&self.master_volume, v);
    }
    pub fn set_comp_amount(&self, v: f32) {
        Self::store(&self.comp_amount, v);
    }
    pub fn set_reverb_mix(&self, v: f32) {
        Self::store(&self.reverb_mix, v);
    }
    pub fn set_patch_gain(&self, v: f32) {
        Self::store_clamped(&self.patch_gain, v, 0.0, 8.0);
    }
    pub fn set_mastering_amount(&self, v: f32) {
        Self::store(&self.mastering_amount, v);
    }
    pub fn set_limiter_ceiling_db(&self, v: f32) {
        Self::store_clamped(&self.limiter_ceiling_db, v, -12.0, 0.0);
    }
    pub fn set_drive_pedal_amount(&self, v: f32) {
        Self::store(&self.drive_pedal_amount, v);
    }
    pub fn set_analog_delay_time_ms(&self, ms: f32) {
        Self::store_clamped(&self.analog_delay_time_ms, ms, 1.0, 1_000.0);
    }
    pub fn set_analog_delay_feedback(&self, v: f32) {
        Self::store_clamped(&self.analog_delay_feedback, v, 0.0, 0.9);
    }
    pub fn set_analog_delay_mix(&self, v: f32) {
        Self::store(&self.analog_delay_mix, v);
    }
    pub fn set_native_cutoff_hz(&self, hz: f32) {
        Self::store_clamped(&self.native_cutoff_hz, hz, 20.0, 20_000.0);
    }
    pub fn set_native_resonance(&self, v: f32) {
        Self::store_clamped(&self.native_resonance, v, 0.0, 0.95);
    }
    pub fn set_native_filter_env(
        &self,
        attack_s: f32,
        decay_s: f32,
        sustain: f32,
        release_s: f32,
        amount: f32,
    ) {
        Self::store_clamped(&self.native_filter_env_attack_s, attack_s, 1.0e-4, 10.0);
        Self::store_clamped(&self.native_filter_env_decay_s, decay_s, 1.0e-4, 10.0);
        Self::store_clamped(&self.native_filter_env_sustain, sustain, 0.0, 1.0);
        Self::store_clamped(&self.native_filter_env_release_s, release_s, 1.0e-4, 10.0);
        Self::store_clamped(&self.native_filter_env_amount, amount, 0.0, 1.0);
    }
    pub fn set_native_amp_env(&self, attack_s: f32, decay_s: f32, sustain: f32, release_s: f32) {
        Self::store_clamped(&self.native_amp_env_attack_s, attack_s, 1.0e-4, 10.0);
        Self::store_clamped(&self.native_amp_env_decay_s, decay_s, 1.0e-4, 10.0);
        Self::store_clamped(&self.native_amp_env_sustain, sustain, 0.0, 1.0);
        Self::store_clamped(&self.native_amp_env_release_s, release_s, 1.0e-4, 10.0);
    }
    /// Store one oscillator's params. `idx` must be 0..=2 and `wave`
    /// a valid 0/1/2 code (both validated at the FFI boundary); octave
    /// clamps to [-2, 2], detune to [-100, 100] cents, level to [0, 1].
    pub fn set_native_osc(
        &self,
        idx: usize,
        wave: u32,
        octave: i32,
        detune_cents: f32,
        level: f32,
    ) {
        self.native_osc_wave[idx].store(wave.min(2), Ordering::Relaxed);
        self.native_osc_octave[idx].store(octave.clamp(-2, 2) as u32, Ordering::Relaxed);
        Self::store_clamped(
            &self.native_osc_detune_cents[idx],
            detune_cents,
            -100.0,
            100.0,
        );
        Self::store_clamped(&self.native_osc_level[idx], level, 0.0, 1.0);
    }
    pub fn set_native_noise_level(&self, level: f32) {
        Self::store_clamped(&self.native_noise_level, level, 0.0, 1.0);
    }
    pub fn set_native_glide_s(&self, seconds: f32) {
        Self::store_clamped(&self.native_glide_s, seconds, 0.0, 5.0);
    }
    pub fn set_native_pulse_width(&self, width: f32) {
        Self::store_clamped(&self.native_pulse_width, width, 0.05, 0.95);
    }
    pub fn set_native_drive(&self, drive: f32) {
        Self::store_clamped(&self.native_drive, drive, 0.0, 1.0);
    }
    /// Store the velocity-routing amounts, both clamped to [0, 1].
    /// Non-finite rejection is per-field (see `store_clamped`).
    pub fn set_native_vel_routing(&self, to_cutoff: f32, to_amp: f32) {
        Self::store_clamped(&self.native_vel_to_cutoff, to_cutoff, 0.0, 1.0);
        Self::store_clamped(&self.native_vel_to_amp, to_amp, 0.0, 1.0);
    }
    pub fn set_native_kbd_track(&self, amt: f32) {
        Self::store_clamped(&self.native_kbd_track, amt, 0.0, 1.0);
    }
    /// Store the GLOBAL LFO params. `wave` must be a valid 0/1/2/3 code
    /// (validated at the FFI boundary; clamped here as a backstop).
    /// Rate clamps to [0.05, 20] Hz, pitch depth to [0, 100] cents,
    /// cutoff depth to [0, 2] octaves, amp depth to [0, 1]. Non-finite
    /// rejection is per-field (see `store_clamped`).
    pub fn set_native_lfo(
        &self,
        wave: u32,
        rate_hz: f32,
        to_pitch_cents: f32,
        to_cutoff_oct: f32,
        to_amp: f32,
    ) {
        self.native_lfo_wave.store(wave.min(3), Ordering::Relaxed);
        Self::store_clamped(&self.native_lfo_rate_hz, rate_hz, 0.05, 20.0);
        Self::store_clamped(&self.native_lfo_to_pitch_cents, to_pitch_cents, 0.0, 100.0);
        Self::store_clamped(&self.native_lfo_to_cutoff_oct, to_cutoff_oct, 0.0, 2.0);
        Self::store_clamped(&self.native_lfo_to_amp, to_amp, 0.0, 1.0);
    }
    pub fn set_native_bend_range(&self, semitones: f32) {
        Self::store_clamped(&self.native_bend_range_semitones, semitones, 0.0, 12.0);
    }
    /// Store the voice-mode wire code. Must be a valid 0/1/2 code
    /// (validated at the FFI boundary; capped here as a backstop,
    /// mirroring `set_native_lfo`'s wave handling).
    pub fn set_native_voice_mode(&self, mode: u32) {
        self.native_voice_mode.store(mode.min(2), Ordering::Relaxed);
    }
    /// Store the oversample wire code. Must be a valid 0/1 code
    /// (validated at the FFI boundary; capped here as a backstop,
    /// mirroring `set_native_voice_mode`).
    pub fn set_native_oversample(&self, on: u32) {
        self.native_oversample.store(on.min(1), Ordering::Relaxed);
    }
}

static DSP_PARAMS: OnceLock<Arc<DspParams>> = OnceLock::new();
fn dsp_params() -> &'static Arc<DspParams> {
    DSP_PARAMS.get_or_init(|| Arc::new(DspParams::new()))
}

struct State {
    thread: thread::JoinHandle<()>,
    quit_flag: Arc<AtomicBool>,
}

pub(crate) struct UserData {
    sine_phase: f32,
    // Only process_audio's (Linux/PipeWire) periodic debug logging reads
    // these; the macOS backend tracks its own local counters instead, so
    // these would otherwise be "never read" dead code there.
    #[cfg(target_os = "linux")]
    callback_count: u32,
    #[cfg(target_os = "linux")]
    last_frames: usize,
    synth: Option<SynthBackend>,
    midi_queue: Arc<ArrayQueue<MidiEvent>>,
    reload_queue: Arc<ArrayQueue<(u64, SynthBackend)>>,
    dsp_params: Arc<DspParams>,
    compressor: Compressor,
    reverb: Reverb,
    mastering: MasteringCompressor,
    limiter: Limiter,
    drive_pedal: DrivePedal,
    /// No last-value change-detection, unlike the effects above — its
    /// setters (`set_time_ms`/`set_feedback`/`set_mix`) are trivial
    /// clamp-and-store with no coefficient recomputation to skip,
    /// so they're pushed unconditionally each block (same rationale
    /// as the native-synth params below).
    analog_delay: AnalogDelay,
    last_comp_amount: f32,
    last_reverb_mix: f32,
    last_mastering_amount: f32,
    last_limiter_ceiling_db: f32,
    last_drive_pedal_amount: f32,
}

static STATE: OnceLock<Mutex<Option<State>>> = OnceLock::new();

fn state_cell() -> &'static Mutex<Option<State>> {
    STATE.get_or_init(|| Mutex::new(None))
}

#[no_mangle]
pub extern "C" fn polyclav_audio_start() -> i32 {
    eprintln!("audio-core: start");
    let mut state_guard = state_cell().lock().unwrap();
    if state_guard.is_some() {
        eprintln!("audio-core: start (already running)");
        return 0;
    }

    let quit_flag = Arc::new(AtomicBool::new(false));
    let quit_for_thread = Arc::clone(&quit_flag);
    let (ready_tx, ready_rx) = mpsc::sync_channel::<Result<(), String>>(1);

    let handle = thread::Builder::new()
        .name("polyclav-audio".into())
        .spawn(move || {
            // Platform dispatch: PipeWire on Linux, CoreAudio (cpal) on macOS.
            // Both share the same (quit_flag, ready_tx) contract: build the
            // stream, signal ready, then block until quit_flag flips.
            #[cfg(target_os = "linux")]
            let setup = run_audio(quit_for_thread, &ready_tx);
            #[cfg(target_os = "macos")]
            let setup = crate::backend_macos::run_audio(quit_for_thread, &ready_tx);
            if let Err(e) = setup {
                eprintln!("audio-core: setup failed: {e}");
                let _ = ready_tx.send(Err(e));
            }
            eprintln!("audio-core: audio thread exiting");
        })
        .expect("spawn audio thread");

    match ready_rx.recv_timeout(Duration::from_secs(2)) {
        Ok(Ok(())) => {
            *state_guard = Some(State {
                thread: handle,
                quit_flag,
            });
            0
        }
        Ok(Err(_)) | Err(_) => {
            quit_flag.store(true, Ordering::SeqCst);
            let _ = handle.join();
            eprintln!("audio-core: start failed");
            1
        }
    }
}

#[no_mangle]
pub extern "C" fn polyclav_audio_stop() {
    let mut state_guard = state_cell().lock().unwrap();
    if let Some(state) = state_guard.take() {
        state.quit_flag.store(true, Ordering::SeqCst);
        let _ = state.thread.join();
        eprintln!("audio-core: stopped");
    }
}

/// # Safety
/// `path` must be a valid NUL-terminated C string or NULL.
#[no_mangle]
pub unsafe extern "C" fn polyclav_audio_set_soundfont(path: *const c_char) -> i32 {
    let mut guard = soundfont_path_cell().lock().unwrap();
    if path.is_null() {
        *guard = None;
    } else {
        unsafe {
            let c_str = CStr::from_ptr(path);
            *guard = c_str.to_str().ok().map(PathBuf::from);
        }
    }
    0
}

/// Set the requested audio buffer size in frames — polyclav's own latency
/// knob. Clamped to `[MIN_QUANTUM, MAX_QUANTUM]`; `0` selects the default
/// ([`DEFAULT_QUANTUM`] = 128, ~2.7 ms at 48 kHz). This is a *request*: the
/// effective buffer never drops below what the platform supports (the
/// PipeWire graph quantum on Linux, the device's minimum buffer on macOS),
/// so the real latency is "this many frames, or the platform minimum,
/// whichever is larger". Set BEFORE `polyclav_audio_start` — the value is
/// read once when the audio thread starts.
#[no_mangle]
pub extern "C" fn polyclav_audio_set_latency_frames(frames: u32) {
    let clamped = if frames == 0 {
        DEFAULT_QUANTUM
    } else {
        frames.clamp(MIN_QUANTUM, MAX_QUANTUM as u32)
    };
    LATENCY_FRAMES.store(clamped, Ordering::Relaxed);
}

/// Reload the soundfont set by `polyclav_audio_set_soundfont`. Loads on a
/// background thread; the audio thread picks up the new backend on the
/// next callback. Returns 0 if reload was scheduled, 1 if no soundfont is
/// set or audio is not running.
#[no_mangle]
pub extern "C" fn polyclav_audio_reload_soundfont() -> i32 {
    let path: Option<PathBuf> = {
        let guard = soundfont_path_cell().lock().unwrap();
        guard.clone()
    };
    let path = match path {
        Some(p) => p,
        None => {
            eprintln!("audio-core: reload requested but no soundfont path set");
            return 1;
        }
    };
    if state_cell().lock().unwrap().is_none() {
        eprintln!("audio-core: reload requested but audio not running");
        return 1;
    }
    let queue = Arc::clone(synth_reload_queue());
    let generation = SOUNDFONT_GENERATION.fetch_add(1, Ordering::SeqCst) + 1;
    thread::Builder::new()
        .name("polyclav-sf-reload".into())
        .spawn(move || {
            eprintln!("audio-core: background reload of {path:?}");
            match SynthBackend::load(&path) {
                Ok(backend) => {
                    let name = backend.name();
                    if queue.push((generation, backend)).is_err() {
                        eprintln!("audio-core: reload queue full; dropping new backend");
                    } else {
                        eprintln!("audio-core: reload queued backend={name}");
                    }
                }
                Err(e) => {
                    eprintln!("audio-core: reload load failed: {e}");
                }
            }
        })
        .expect("spawn reload thread");
    0
}

/// Load and switch to an LV2 plugin identified by URI (e.g.
/// `http://geontime.com/dexed`). Discovery + instantiation happen on a
/// background worker; the audio thread picks up the new backend on the
/// next callback. Shares the generation counter with soundfont reloads
/// so rapid patch-switching still discards stale backends.
///
/// Returns 0 if load was scheduled, 1 if audio is not running, 2 if the
/// URI string is invalid UTF-8 or NULL.
///
/// # Safety
/// `uri` must be a valid NUL-terminated C string.
#[cfg(target_os = "linux")]
#[no_mangle]
pub unsafe extern "C" fn polyclav_audio_set_lv2_plugin(uri: *const c_char) -> i32 {
    if uri.is_null() {
        return 2;
    }
    let uri = match unsafe { CStr::from_ptr(uri) }.to_str() {
        Ok(s) => s.to_string(),
        Err(_) => return 2,
    };
    if state_cell().lock().unwrap().is_none() {
        eprintln!("audio-core: LV2 load requested but audio not running");
        return 1;
    }
    let queue = Arc::clone(synth_reload_queue());
    let generation = SOUNDFONT_GENERATION.fetch_add(1, Ordering::SeqCst) + 1;
    thread::Builder::new()
        .name("polyclav-lv2-load".into())
        .spawn(move || {
            eprintln!("audio-core: background load of LV2 plugin {uri:?}");
            match SynthBackend::load_lv2(&uri) {
                Ok(backend) => {
                    let name = backend.name();
                    if queue.push((generation, backend)).is_err() {
                        eprintln!("audio-core: reload queue full; dropping new backend");
                    } else {
                        eprintln!("audio-core: reload queued backend={name} gen={generation}");
                    }
                }
                Err(e) => eprintln!("audio-core: LV2 load failed: {e}"),
            }
        })
        .expect("spawn lv2 load thread");
    0
}

/// Load and switch to a CLAP plugin identified by `.clap` bundle path and
/// internal plugin id (e.g. `/nix/store/.../Dexed.clap` + `com.asb2m10.dexed`).
/// Same lifecycle as `polyclav_audio_set_lv2_plugin`.
///
/// Returns 0 if load was scheduled, 1 if audio is not running, 2 if either
/// argument is invalid UTF-8 or NULL.
///
/// # Safety
/// `bundle_path` and `plugin_id` must be valid NUL-terminated C strings.
#[cfg(target_os = "linux")]
#[no_mangle]
pub unsafe extern "C" fn polyclav_audio_set_clap_plugin(
    bundle_path: *const c_char,
    plugin_id: *const c_char,
) -> i32 {
    if bundle_path.is_null() || plugin_id.is_null() {
        return 2;
    }
    let bundle_path = match unsafe { CStr::from_ptr(bundle_path) }.to_str() {
        Ok(s) => PathBuf::from(s),
        Err(_) => return 2,
    };
    let plugin_id = match unsafe { CStr::from_ptr(plugin_id) }.to_str() {
        Ok(s) => s.to_string(),
        Err(_) => return 2,
    };
    if state_cell().lock().unwrap().is_none() {
        eprintln!("audio-core: CLAP load requested but audio not running");
        return 1;
    }
    let queue = Arc::clone(synth_reload_queue());
    let generation = SOUNDFONT_GENERATION.fetch_add(1, Ordering::SeqCst) + 1;
    thread::Builder::new()
        .name("polyclav-clap-load".into())
        .spawn(move || {
            eprintln!(
                "audio-core: background load of CLAP plugin {} :: {plugin_id:?}",
                bundle_path.display()
            );
            match SynthBackend::load_clap(&bundle_path, &plugin_id) {
                Ok(backend) => {
                    let name = backend.name();
                    if queue.push((generation, backend)).is_err() {
                        eprintln!("audio-core: reload queue full; dropping new backend");
                    } else {
                        eprintln!("audio-core: reload queued backend={name} gen={generation}");
                    }
                }
                Err(e) => eprintln!("audio-core: CLAP load failed: {e}"),
            }
        })
        .expect("spawn clap load thread");
    0
}

/// macOS stub: LV2 hosting is Linux-only (livi wraps the lilv C library,
/// which has no macOS build). Kept as a DEFINED exported symbol so the shared,
/// build-tag-free Go wrapper `audio.SetLv2Plugin` still links on darwin. Always
/// returns 1 ("not available / not running").
///
/// # Safety
/// `uri` must be a valid NUL-terminated C string or NULL.
#[cfg(target_os = "macos")]
#[no_mangle]
pub unsafe extern "C" fn polyclav_audio_set_lv2_plugin(_uri: *const c_char) -> i32 {
    eprintln!("audio-core: LV2 hosting is unavailable on macOS (native/SF2/SFZ only)");
    1
}

/// macOS stub: CLAP hosting is out of scope for the v1 macOS port. Kept as a
/// DEFINED exported symbol so the shared Go wrapper `audio.SetClapPlugin`
/// still links on darwin. Always returns 1 ("not available / not running").
///
/// # Safety
/// `bundle_path` and `plugin_id` must be valid NUL-terminated C strings or NULL.
#[cfg(target_os = "macos")]
#[no_mangle]
pub unsafe extern "C" fn polyclav_audio_set_clap_plugin(
    _bundle_path: *const c_char,
    _plugin_id: *const c_char,
) -> i32 {
    eprintln!("audio-core: CLAP hosting is unavailable on macOS in v1");
    1
}

/// Returns 1 if libsfizz is available (SFZ playback possible), else 0.
/// sfizz is dlopen'd lazily; this call triggers the load attempt. Used by
/// `polyclav doctor` and by startup to warn about SFZ patches that would be
/// silent. Safe to call before `polyclav_audio_start`.
#[no_mangle]
pub extern "C" fn polyclav_audio_sfizz_available() -> i32 {
    if sfizz::available() {
        1
    } else {
        0
    }
}

/// Load and switch to a native pure-Rust synth patch. `engine` is a
/// factory-preset name baked into the Rust source — Phase 1 only ships
/// `"minimoog"`. The load runs on a background worker thread; the audio
/// thread picks up the new backend on the next callback. Shares the
/// generation counter with the soundfont/LV2/CLAP loaders.
///
/// Returns 0 if load was scheduled, 1 if audio is not running, 2 if
/// `engine` is NULL or not valid UTF-8, 3 if the engine name is unknown.
///
/// # Safety
/// `engine` must be a valid NUL-terminated C string.
#[no_mangle]
pub unsafe extern "C" fn polyclav_audio_set_native_patch(engine: *const c_char) -> i32 {
    if engine.is_null() {
        return 2;
    }
    let engine = match unsafe { CStr::from_ptr(engine) }.to_str() {
        Ok(s) => s.to_string(),
        Err(_) => return 2,
    };
    if state_cell().lock().unwrap().is_none() {
        eprintln!("audio-core: native load requested but audio not running");
        return 1;
    }
    // Validate engine name synchronously so a typo at config-load time
    // surfaces as a clear error rather than a silent no-op.
    if let Err(e) = NativeSynth::new(&engine, SAMPLE_RATE).map(drop) {
        eprintln!("audio-core: native synth init failed: {e}");
        return 3;
    }
    let queue = Arc::clone(synth_reload_queue());
    let generation = SOUNDFONT_GENERATION.fetch_add(1, Ordering::SeqCst) + 1;
    thread::Builder::new()
        .name("polyclav-native-load".into())
        .spawn(move || {
            eprintln!("audio-core: background load of native synth engine={engine:?}");
            match SynthBackend::load_native(&engine) {
                Ok(backend) => {
                    let name = backend.name();
                    if queue.push((generation, backend)).is_err() {
                        eprintln!("audio-core: reload queue full; dropping new backend");
                    } else {
                        eprintln!("audio-core: reload queued backend={name} gen={generation}");
                    }
                }
                Err(e) => eprintln!("audio-core: native load failed: {e}"),
            }
        })
        .expect("spawn native load thread");
    0
}

#[no_mangle]
pub extern "C" fn polyclav_midi_note_on(channel: u8, note: u8, velocity: u8) {
    let _ = midi_queue().push(MidiEvent::NoteOn {
        channel,
        note,
        velocity,
    });
}

#[no_mangle]
pub extern "C" fn polyclav_midi_note_off(channel: u8, note: u8, _velocity: u8) {
    let _ = midi_queue().push(MidiEvent::NoteOff { channel, note });
}

#[no_mangle]
pub extern "C" fn polyclav_midi_cc(channel: u8, controller: u8, value: u8) {
    let _ = midi_queue().push(MidiEvent::ControlChange {
        channel,
        controller,
        value,
    });
}

#[no_mangle]
pub extern "C" fn polyclav_midi_pitch_bend(channel: u8, bend: u16) {
    let _ = midi_queue().push(MidiEvent::PitchBend { channel, bend });
}

#[no_mangle]
pub extern "C" fn polyclav_dsp_set_master_volume(v: f32) {
    dsp_params().set_master_volume(v);
}

#[no_mangle]
pub extern "C" fn polyclav_dsp_set_compressor(v: f32) {
    dsp_params().set_comp_amount(v);
}

#[no_mangle]
pub extern "C" fn polyclav_dsp_set_reverb(v: f32) {
    dsp_params().set_reverb_mix(v);
}

#[no_mangle]
pub extern "C" fn polyclav_dsp_set_patch_gain(linear: f32) {
    dsp_params().set_patch_gain(linear);
}

#[no_mangle]
pub extern "C" fn polyclav_dsp_set_mastering_compressor(amount: f32) {
    dsp_params().set_mastering_amount(amount);
}

#[no_mangle]
pub extern "C" fn polyclav_dsp_set_limiter_ceiling_db(db: f32) {
    dsp_params().set_limiter_ceiling_db(db);
}

/// Set the drive-pedal amount in [0, 1]. 0.0 (default) bypasses the
/// stage bit-exactly. Runs in the shared post-synth chain, not inside
/// any one synth backend, so it applies to every patch type. See
/// `dsp::DrivePedal` and docs/OPEN_SOUND_ENGINES.md §1.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_drive_pedal(v: f32) {
    dsp_params().set_drive_pedal_amount(v);
}

/// Set the analog-delay time in milliseconds, clamped to [1, 1000] in
/// Rust. See `dsp::AnalogDelay` and its module doc comment for the
/// signal-flow design (feedback-loop-only saturation/warmth).
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_analog_delay_time_ms(ms: f32) {
    dsp_params().set_analog_delay_time_ms(ms);
}

/// Set the analog-delay feedback (repeats) amount in [0, 0.9] —
/// capped below unity so the pedal stays a delay, not a deliberate
/// self-oscillator. Clamped in Rust.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_analog_delay_feedback(v: f32) {
    dsp_params().set_analog_delay_feedback(v);
}

/// Set the analog-delay wet/dry mix in [0, 1]. 0.0 (default) bypasses
/// the stage bit-exactly. Runs in the shared post-synth chain, after
/// the drive pedal, so it applies to every patch type.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_analog_delay_mix(v: f32) {
    dsp_params().set_analog_delay_mix(v);
}

/// Set the native synth's filter cutoff in Hz, pushed from the FILTER
/// page's Cutoff knob (MAIN knob 4 now drives the drive pedal instead;
/// see `polyclav_dsp_set_drive_pedal`). The audio thread reads the
/// atomic each block and applies it to the active `SynthBackend::Native`;
/// harmless when other backends are active. Clamped to [20, 20000] in Rust.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_cutoff_hz(hz: f32) {
    dsp_params().set_native_cutoff_hz(hz);
}

/// Set the native synth's filter resonance (Q). Same lifecycle as
/// `polyclav_dsp_set_native_cutoff_hz`: the audio thread reads the
/// atomic each block and applies it to the active `SynthBackend::Native`;
/// harmless when other backends are active. Clamped to [0.0, 0.95] in
/// Rust — headroom below the self-oscillation instability of the
/// Stilson/Smith ladder.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_resonance(v: f32) {
    dsp_params().set_native_resonance(v);
}

/// Set the native synth's filter-envelope (env 2) ADSR + env→cutoff
/// amount. Same lifecycle as `polyclav_dsp_set_native_cutoff_hz`: the
/// audio thread reads the atomics each block and applies them to the
/// active `SynthBackend::Native`; harmless when other backends are
/// active. Times clamped to [0.0001, 10] s, sustain and amount to
/// [0, 1] in Rust. Non-finite values (NaN, ±inf) are rejected
/// per-field — that field keeps its previous value while finite
/// arguments still apply (see `DspParams::store_clamped`). Amount 0
/// (the default) disables the modulation — the render is then
/// identical to the pre-env-2 engine.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_filter_env(
    attack_s: f32,
    decay_s: f32,
    sustain: f32,
    release_s: f32,
    amount: f32,
) {
    dsp_params().set_native_filter_env(attack_s, decay_s, sustain, release_s, amount);
}

/// Set one native-synth oscillator's parameters (stage 3). `idx` is
/// 0..=2; `wave` is 0 = saw, 1 = square, 2 = pulse (pulse runs a fixed
/// 25% duty for this stage); `octave` clamps to [-2, 2]; `detune_cents`
/// to [-100, 100]; `level` to [0, 1]. Out-of-range `idx` or `wave` is
/// ignored with an eprintln; a non-finite `detune_cents` or `level`
/// (NaN, ±inf) is rejected per-field — that field keeps its previous
/// value (see `DspParams::store_clamped`). Same lifecycle as
/// `polyclav_dsp_set_native_cutoff_hz`: the audio thread reads the
/// atomics each block and applies them to the active
/// `SynthBackend::Native`; harmless when other backends are active.
/// Defaults (osc 2/3 + noise levels 0) keep the render bit-identical
/// to the single-osc engine.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_osc(
    idx: i32,
    wave: i32,
    octave: i32,
    detune_cents: f32,
    level: f32,
) {
    if !(0..=2).contains(&idx) {
        eprintln!("audio-core: set_native_osc: idx {idx} out of range 0..=2; ignored");
        return;
    }
    if !(0..=2).contains(&wave) {
        eprintln!(
            "audio-core: set_native_osc: wave {wave} out of range 0..=2 \
             (0=saw 1=square 2=pulse); ignored"
        );
        return;
    }
    dsp_params().set_native_osc(idx as usize, wave as u32, octave, detune_cents, level);
}

/// Set the native synth's white-noise mixer level in [0, 1] (clamped
/// in Rust; default 0.0 = silent). Same lifecycle as
/// `polyclav_dsp_set_native_cutoff_hz`.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_noise(level: f32) {
    dsp_params().set_native_noise_level(level);
}

/// Set the native synth's glide (portamento) time constant in seconds,
/// clamped to [0, 5] in Rust. 0 (the default) disables the pitch slew —
/// the render is then identical to the pre-glide engine. When enabled,
/// the voice's base frequency slews exponentially toward the note pitch
/// (per-osc octave/detune multipliers apply after the slew); glide
/// applies to legato hand-offs AND retriggered notes of a
/// still-sounding voice (Minimoog behavior), while a voice starting
/// from silence begins at its target pitch. Same lifecycle as
/// `polyclav_dsp_set_native_cutoff_hz`: the audio thread reads the
/// atomic each block and applies it to the active
/// `SynthBackend::Native`; harmless when other backends are active.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_glide(seconds: f32) {
    dsp_params().set_native_glide_s(seconds);
}

/// Set the native synth's amp-envelope (env 1) ADSR. Same lifecycle as
/// `polyclav_dsp_set_native_cutoff_hz`: the audio thread reads the
/// atomics each block and applies them to the active
/// `SynthBackend::Native`; harmless when other backends are active.
/// Times clamped to [0.0001, 10] s, sustain to [0, 1] in Rust.
/// Non-finite values (NaN, ±inf) are rejected per-field — that field
/// keeps its previous value while finite arguments still apply (see
/// `DspParams::store_clamped`). Updating params does not disturb a
/// running envelope; the defaults (5 ms / 200 ms / 0.7 / 400 ms) are
/// exactly the previously-hardcoded values, so the default render is
/// identical to the pre-change engine.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_amp_env(
    attack_s: f32,
    decay_s: f32,
    sustain: f32,
    release_s: f32,
) {
    dsp_params().set_native_amp_env(attack_s, decay_s, sustain, release_s);
}

/// Set the native synth's pulse-wave duty cycle, clamped to
/// [0.05, 0.95] in Rust. One global knob shared by all three
/// oscillators; only audible while a pulse waveform is selected. The
/// default 0.25 is exactly the old fixed stage-3 duty, so the default
/// render is identical to the pre-change engine. Same lifecycle as
/// `polyclav_dsp_set_native_cutoff_hz`: the audio thread reads the
/// atomic each block and applies it to the active
/// `SynthBackend::Native`; harmless when other backends are active.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_pulse_width(width: f32) {
    dsp_params().set_native_pulse_width(width);
}

/// Set the native synth's pre-filter tanh drive amount, clamped to
/// [0, 1] in Rust. 0 (the default) bypasses the saturator bit-exactly —
/// the render is then identical to the pre-drive engine. When > 0 the
/// post-mixer signal is shaped by `tanh(x * (1 + drive*4)) /
/// (1 + drive*4)` before the ladder filter (ROADMAP §1.1
/// "TANH/SOFTCLIP DRIVE"): normalizing by the pre-gain keeps unity gain
/// for small signals while peaks compress toward ±1/(1 + drive*4). Same
/// lifecycle as `polyclav_dsp_set_native_cutoff_hz`: the audio thread
/// reads the atomic each block and applies it to the active
/// `SynthBackend::Native`; harmless when other backends are active.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_drive(drive: f32) {
    dsp_params().set_native_drive(drive);
}

/// Set the native synth's velocity-routing amounts (ROADMAP §1.1 mod
/// input "vel"), both clamped to [0, 1] in Rust. `to_amp` = 1 (the
/// default) keeps the classic `velocity / 127` amp scaling — the render
/// is then identical to the pre-routing engine; 0 ignores velocity
/// (`scale = lerp(1.0, vel/127, to_amp)`). `to_cutoff` = 0 (the
/// default) is bit-transparent; 1 swings the effective cutoff up to
/// ±1 octave around the knob cutoff, centered at velocity 64
/// (`× 2^(to_cutoff * (vel/127 - 0.5) * 2)`). Both are captured per
/// voice at note-on — knob turns mid-note affect the next note.
/// Non-finite values (NaN, ±inf) are rejected per-field (see
/// `DspParams::store_clamped`). Same lifecycle as
/// `polyclav_dsp_set_native_cutoff_hz`: the audio thread reads the
/// atomics each block and applies them to the active
/// `SynthBackend::Native`; harmless when other backends are active.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_vel_routing(to_cutoff: f32, to_amp: f32) {
    dsp_params().set_native_vel_routing(to_cutoff, to_amp);
}

/// Set the native synth's keyboard-tracking amount (ROADMAP §1.1 mod
/// input "kbd"), clamped to [0, 1] in Rust. 0 (the default) is
/// bit-transparent; 1 makes the effective cutoff track the keyboard at
/// 100% (`× 2^(amt * (note - 60) / 12)` — 2× per octave above middle C,
/// ÷2 per octave below), following the sounding note (legato hand-offs
/// included). The final effective cutoff is clamped to [20, 20000] Hz.
/// Same lifecycle as `polyclav_dsp_set_native_cutoff_hz`: the audio
/// thread reads the atomic each block and applies it to the active
/// `SynthBackend::Native`; harmless when other backends are active.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_kbd_track(amt: f32) {
    dsp_params().set_native_kbd_track(amt);
}

/// Set the native synth's GLOBAL LFO (ROADMAP §1.1 GLOBAL block).
/// `wave` is 0 = triangle, 1 = saw, 2 = square, 3 = sample-and-hold
/// (deterministic xorshift stepped once per cycle) — out-of-range
/// codes are ignored with an eprintln, mirroring the osc setter.
/// `rate_hz` clamps to [0.05, 20] (default 5.0). Depths (all default
/// 0 = bit-transparent): `to_pitch_cents` in [0, 100] — vibrato, the
/// voice frequency is multiplied by `2^(lfo * cents / 1200)`, and the
/// depth heard is scaled LIVE by MIDI CC 1 (mod wheel). The synth
/// boots with the wheel at 1.0, so a configured depth is audible with
/// no wheel attached; the first CC 1 event takes over (wheel 0 then
/// silences vibrato — classic vibrato-on-wheel). `to_cutoff_oct` in
/// [0, 2] — the effective cutoff is multiplied by `2^(lfo * oct)`.
/// `to_amp` in [0, 1] — tremolo, the output is multiplied by
/// `1 - depth * (lfo*0.5 + 0.5)`. Non-finite values (NaN, ±inf) are
/// rejected per-field (see `DspParams::store_clamped`). Same lifecycle
/// as `polyclav_dsp_set_native_cutoff_hz`: the audio thread reads the
/// atomics each block and applies them to the active
/// `SynthBackend::Native`; harmless when other backends are active.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_lfo(
    wave: u32,
    rate_hz: f32,
    to_pitch_cents: f32,
    to_cutoff_oct: f32,
    to_amp: f32,
) {
    if wave > 3 {
        eprintln!(
            "audio-core: set_native_lfo: wave {wave} out of range 0..=3 \
             (0=triangle 1=saw 2=square 3=sh); ignored"
        );
        return;
    }
    dsp_params().set_native_lfo(wave, rate_hz, to_pitch_cents, to_cutoff_oct, to_amp);
}

/// Set the native synth's pitch-bend range in semitones at full wheel
/// deflection, clamped to [0, 12] in Rust. Default 2.0 (the MIDI
/// convention). Incoming `polyclav_midi_pitch_bend` events (14-bit
/// wire value, 8192 = center) scale the voice frequency by
/// `2^(range * (bend - 8192)/8192 / 12)`; with no bend event the
/// factor is exactly 1.0 — the default render is unchanged. Non-finite
/// values are rejected (see `DspParams::store_clamped`). Same
/// lifecycle as `polyclav_dsp_set_native_cutoff_hz`: the audio thread
/// reads the atomic each block and applies it to the active
/// `SynthBackend::Native`; harmless when other backends are active.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_bend_range(st: f32) {
    dsp_params().set_native_bend_range(st);
}

/// Set the native synth's voice-allocation mode (ROADMAP §1.2 / §1.5):
/// 0 = mono_legato (the default — 1 voice, last-note priority,
/// envelopes only retrigger when no other key is held; the render at
/// this default is bit-identical to the pre-poly engine), 1 =
/// mono_retrig (1 voice, envelopes ALWAYS retrigger on note-on), 2 =
/// poly (8 voices; a note-on takes a free voice — amp env idle — or
/// steals the oldest-fired sounding voice; a note-off releases exactly
/// the voice(s) sounding that note). Out-of-range codes are ignored
/// with an eprintln, mirroring the osc/LFO setters. Switching modes
/// while notes sound releases every voice and clears the held-notes
/// stack (no stuck notes — keys already down fade through their
/// release tails and must be re-pressed). Same lifecycle as
/// `polyclav_dsp_set_native_cutoff_hz`: the audio thread reads the
/// atomic each block and applies it to the active
/// `SynthBackend::Native`; harmless when other backends are active.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_voice_mode(mode: u32) {
    if mode > 2 {
        eprintln!(
            "audio-core: set_native_voice_mode: mode {mode} out of range 0..=2 \
             (0=mono_legato 1=mono_retrig 2=poly); ignored"
        );
        return;
    }
    dsp_params().set_native_voice_mode(mode);
}

/// Enable/disable 2× oversampling of the native synth's per-voice
/// nonlinear section — the tanh drive + Moog ladder (ROADMAP §0.1 /
/// §1.6 / Appendix A pivot item (a)): 0 = off (the DEFAULT — the
/// base-rate path, bit-identical to the pre-oversampling engine), 1 =
/// on (the mixer output is upsampled 2× through a minimum-phase
/// halfband, the drive + ladder run — retuned — at sample_rate × 2,
/// and the same halfband decimates back, removing the tanh stages'
/// fold-back aliasing under hard drive). Out-of-range codes are ignored
/// with an eprintln, mirroring the voice-mode setter. Toggling while
/// notes sound swaps per-voice filter instances (the newly-active one
/// is reset + retuned) — a brief click may be audible; the toggle is a
/// setup switch, not a performance control. Same lifecycle as
/// `polyclav_dsp_set_native_cutoff_hz`: the audio thread reads the
/// atomic each block and applies it to the active
/// `SynthBackend::Native`; harmless when other backends are active.
#[no_mangle]
pub extern "C" fn polyclav_dsp_set_native_oversample(on: u32) {
    if on > 1 {
        eprintln!("audio-core: set_native_oversample: value {on} out of range 0..=1 (0=off 1=on); ignored");
        return;
    }
    dsp_params().set_native_oversample(on);
}

/// Build the initial [`UserData`]: soundfont/native load, a DSP-param snapshot,
/// and DSP-stage construction. Portable (no OS audio API), so it is shared by
/// the PipeWire `run_audio` (Linux) and the CoreAudio backend (macOS).
pub(crate) fn build_user_data() -> UserData {
    let synth: Option<SynthBackend> = {
        let path_guard = soundfont_path_cell().lock().unwrap();
        path_guard.as_ref().and_then(|path| {
            if !path.exists() {
                eprintln!("audio-core: soundfont path does not exist: {path:?}");
                return None;
            }
            match SynthBackend::load(path) {
                Ok(b) => Some(b),
                Err(e) => {
                    eprintln!("audio-core: soundfont load failed: {e}");
                    None
                }
            }
        })
    };

    let params = Arc::clone(dsp_params());
    let initial_comp = params.comp_amount();
    let initial_mix = params.reverb_mix();
    let initial_mastering = params.mastering_amount();
    let initial_ceiling_db = params.limiter_ceiling_db();
    let initial_drive_pedal = params.drive_pedal_amount();
    let mut compressor = Compressor::new();
    let mut reverb = Reverb::new();
    let mut mastering = MasteringCompressor::new(SAMPLE_RATE);
    let mut limiter = Limiter::new(SAMPLE_RATE);
    let mut drive_pedal = DrivePedal::new(SAMPLE_RATE);
    let mut analog_delay = AnalogDelay::new(SAMPLE_RATE);
    compressor.set_amount(initial_comp);
    reverb.set_mix(initial_mix);
    mastering.set_amount(initial_mastering);
    limiter.set_ceiling_db(initial_ceiling_db);
    drive_pedal.set_amount(initial_drive_pedal);
    analog_delay.set_time_ms(params.analog_delay_time_ms());
    analog_delay.set_feedback(params.analog_delay_feedback());
    analog_delay.set_mix(params.analog_delay_mix());

    UserData {
        sine_phase: 0.0,
        #[cfg(target_os = "linux")]
        callback_count: 0,
        #[cfg(target_os = "linux")]
        last_frames: 0,
        synth,
        midi_queue: Arc::clone(midi_queue()),
        reload_queue: Arc::clone(synth_reload_queue()),
        dsp_params: params,
        compressor,
        reverb,
        mastering,
        limiter,
        drive_pedal,
        analog_delay,
        last_comp_amount: initial_comp,
        last_reverb_mix: initial_mix,
        last_mastering_amount: initial_mastering,
        last_limiter_ceiling_db: initial_ceiling_db,
        last_drive_pedal_amount: initial_drive_pedal,
    }
}

/// Build a [`UserData`] around `synth` with FRESH (non-global) lock-free
/// queues — isolated from the live audio thread's globals. Portable; used by
/// the offline renderer (`polyclav_render_offline`) and the offline tests.
pub(crate) fn build_offline_user_data(synth: Option<SynthBackend>) -> UserData {
    let params = Arc::new(DspParams::new());
    let mut compressor = Compressor::new();
    let mut reverb = Reverb::new();
    let mut mastering = MasteringCompressor::new(SAMPLE_RATE);
    let mut limiter = Limiter::new(SAMPLE_RATE);
    let mut drive_pedal = DrivePedal::new(SAMPLE_RATE);
    let mut analog_delay = AnalogDelay::new(SAMPLE_RATE);
    compressor.set_amount(params.comp_amount());
    reverb.set_mix(params.reverb_mix());
    mastering.set_amount(params.mastering_amount());
    limiter.set_ceiling_db(params.limiter_ceiling_db());
    drive_pedal.set_amount(params.drive_pedal_amount());
    analog_delay.set_time_ms(params.analog_delay_time_ms());
    analog_delay.set_feedback(params.analog_delay_feedback());
    analog_delay.set_mix(params.analog_delay_mix());
    UserData {
        sine_phase: 0.0,
        #[cfg(target_os = "linux")]
        callback_count: 0,
        #[cfg(target_os = "linux")]
        last_frames: 0,
        synth,
        midi_queue: Arc::new(ArrayQueue::new(64)),
        reload_queue: Arc::new(ArrayQueue::new(4)),
        last_comp_amount: params.comp_amount(),
        last_reverb_mix: params.reverb_mix(),
        last_mastering_amount: params.mastering_amount(),
        last_limiter_ceiling_db: params.limiter_ceiling_db(),
        last_drive_pedal_amount: params.drive_pedal_amount(),
        dsp_params: params,
        compressor,
        reverb,
        mastering,
        limiter,
        drive_pedal,
        analog_delay,
    }
}

/// One MIDI event for the offline-render event sequence FFI, timed by
/// absolute frame offset from the start of the render (not a delta).
/// `kind`: 0 = NoteOn, 1 = NoteOff, 2 = ControlChange, 3 = PitchBend.
/// For NoteOn/NoteOff, `data1` is the note number and `data2` the
/// velocity (NoteOff ignores `data2`). For ControlChange, `data1` is
/// the controller and `data2` the value. For PitchBend, `data2` is the
/// 14-bit bend value (`data1` unused). An unrecognized `kind` is
/// silently skipped — see `render_offline_core`.
#[repr(C)]
pub struct FfiMidiEvent {
    pub frame: u32,
    pub kind: u8,
    pub channel: u8,
    pub data1: u8,
    pub data2: u16,
}

/// Optional overrides for the shared, backend-agnostic post-synth chain
/// params (docs/VISION.md's measurement/calibration tooling) — every
/// field an f32 with `NaN` meaning "leave at the engine default,"
/// checked via `is_finite()` rather than a parallel "has_X" flag per
/// field (simpler wire shape, and NaN/±inf are never legitimate values
/// for any of these params anyway — every one of them already rejects
/// non-finite input at its normal setter). Covers exactly the fields
/// that are global-chain-level rather than native-synth-voice-level —
/// the native synth's own knobs (cutoff, oscillators, ...) are a
/// separate, backend-specific concern this struct deliberately doesn't
/// reach into.
#[repr(C)]
pub struct FfiChainParams {
    pub master_volume: f32,
    pub comp_amount: f32,
    pub reverb_mix: f32,
    pub patch_gain: f32,
    pub mastering_amount: f32,
    pub limiter_ceiling_db: f32,
    pub drive_pedal_amount: f32,
    pub analog_delay_time_ms: f32,
    pub analog_delay_feedback: f32,
    pub analog_delay_mix: f32,
}

/// Apply every finite (i.e. explicitly set) field of `cp` onto `ud`'s
/// fresh `DspParams`. Called once, right after `build_offline_user_data`
/// and before the render loop — `render_block` re-reads `dsp_params`
/// every block regardless (some effects via last-value change
/// detection, the analog delay unconditionally), so setting these
/// before the first `render_block` call is sufficient for them to take
/// effect from frame 0.
fn apply_chain_params(ud: &UserData, cp: &FfiChainParams) {
    if cp.master_volume.is_finite() {
        ud.dsp_params.set_master_volume(cp.master_volume);
    }
    if cp.comp_amount.is_finite() {
        ud.dsp_params.set_comp_amount(cp.comp_amount);
    }
    if cp.reverb_mix.is_finite() {
        ud.dsp_params.set_reverb_mix(cp.reverb_mix);
    }
    if cp.patch_gain.is_finite() {
        ud.dsp_params.set_patch_gain(cp.patch_gain);
    }
    if cp.mastering_amount.is_finite() {
        ud.dsp_params.set_mastering_amount(cp.mastering_amount);
    }
    if cp.limiter_ceiling_db.is_finite() {
        ud.dsp_params.set_limiter_ceiling_db(cp.limiter_ceiling_db);
    }
    if cp.drive_pedal_amount.is_finite() {
        ud.dsp_params.set_drive_pedal_amount(cp.drive_pedal_amount);
    }
    if cp.analog_delay_time_ms.is_finite() {
        ud.dsp_params
            .set_analog_delay_time_ms(cp.analog_delay_time_ms);
    }
    if cp.analog_delay_feedback.is_finite() {
        ud.dsp_params
            .set_analog_delay_feedback(cp.analog_delay_feedback);
    }
    if cp.analog_delay_mix.is_finite() {
        ud.dsp_params.set_analog_delay_mix(cp.analog_delay_mix);
    }
}

/// Shared core behind both offline-render FFI entry points below: walk
/// `events` (REQUIRED pre-sorted by `frame`, ascending — this is a
/// device-free analysis tool, not the real-time callback, so the
/// simplicity of trusting the caller's ordering outweighs defensively
/// sorting on every call) pushing each into the synth's MIDI queue at
/// the right frame, rendering the gaps between event frames through the
/// full DSP chain via `render_block` — the exact seam the real audio
/// thread uses, just device-free. `samples` is interleaved stereo f32,
/// `len = 2 * n_frames`. `chain_params`, if present, is applied before
/// the first block renders — see `apply_chain_params`.
fn render_offline_core(
    synth: SynthBackend,
    chain_params: Option<&FfiChainParams>,
    events: &[FfiMidiEvent],
    samples: &mut [f32],
) {
    let mut ud = build_offline_user_data(Some(synth));
    if let Some(cp) = chain_params {
        apply_chain_params(&ud, cp);
    }
    let total = samples.len() / 2;
    let mut done = 0usize;
    let mut idx = 0usize;
    while done < total {
        while idx < events.len() && (events[idx].frame as usize) <= done {
            let e = &events[idx];
            let ev = match e.kind {
                0 => Some(MidiEvent::NoteOn {
                    channel: e.channel,
                    note: e.data1,
                    velocity: e.data2 as u8,
                }),
                1 => Some(MidiEvent::NoteOff {
                    channel: e.channel,
                    note: e.data1,
                }),
                2 => Some(MidiEvent::ControlChange {
                    channel: e.channel,
                    controller: e.data1,
                    value: e.data2 as u8,
                }),
                3 => Some(MidiEvent::PitchBend {
                    channel: e.channel,
                    bend: e.data2,
                }),
                _ => None,
            };
            if let Some(ev) = ev {
                let _ = ud.midi_queue.push(ev);
            }
            idx += 1;
        }
        drain_midi(&mut ud);
        // Invariant: after the inner loop, every remaining event has
        // frame > done (all <= done were just consumed), so `next` is
        // always > done — render_block always gets a non-empty slice
        // and `done` always advances.
        let next = events
            .get(idx)
            .map(|e| (e.frame as usize).min(total))
            .unwrap_or(total);
        render_block(&mut ud, &mut samples[done * 2..next * 2]);
        done = next;
    }
}

/// Load a `SynthBackend` by the generic `(patch_type, patch_ref,
/// plugin_id)` triple the multi-patch-type offline-render FFI uses.
/// `patch_type` is one of "soundfont" (dispatches on file extension,
/// same as the live `SetSoundfont` path), "native" (`patch_ref` is the
/// engine name), "lv2" (`patch_ref` is the URI, Linux only), or "clap"
/// (`patch_ref` is the bundle path, `plugin_id` required, Linux only).
#[cfg(target_os = "linux")]
fn load_synth_by_type(
    patch_type: &str,
    patch_ref: &str,
    plugin_id: Option<&str>,
) -> Result<SynthBackend, String> {
    match patch_type {
        "soundfont" => SynthBackend::load(Path::new(patch_ref)),
        "native" => SynthBackend::load_native(patch_ref),
        "lv2" => SynthBackend::load_lv2(patch_ref),
        "clap" => {
            let id = plugin_id.ok_or_else(|| "clap patch_type requires plugin_id".to_string())?;
            SynthBackend::load_clap(Path::new(patch_ref), id)
        }
        other => Err(format!("unknown patch_type {other:?}")),
    }
}

/// macOS variant: LV2/CLAP offline hosting is unavailable there (same
/// boundary as `polyclav_audio_set_lv2_plugin`/`set_clap_plugin`'s
/// stubs) — "soundfont" and "native" only.
#[cfg(target_os = "macos")]
fn load_synth_by_type(
    patch_type: &str,
    patch_ref: &str,
    _plugin_id: Option<&str>,
) -> Result<SynthBackend, String> {
    match patch_type {
        "soundfont" => SynthBackend::load(Path::new(patch_ref)),
        "native" => SynthBackend::load_native(patch_ref),
        other => Err(format!("patch_type {other:?} unavailable on macOS")),
    }
}

/// Offline (no-device) render: the native `engine` synth playing
/// `note`/`velocity` held from t=0, through the full DSP chain, written to
/// `out` as interleaved stereo f32. No audio device, no global state —
/// powers `polyclav render` and the CI offline-render gate, on every
/// platform. A thin wrapper over `render_offline_core` with a single
/// synthetic NoteOn at frame 0.
///
/// Returns 0 on success, 2 on a bad/unknown `engine` string, 3 if `out` is
/// NULL or `n_frames` is 0.
///
/// # Safety
/// `engine` must be a valid NUL-terminated C string; `out` must point to at
/// least `n_frames * 2` writable `f32`s.
#[no_mangle]
pub unsafe extern "C" fn polyclav_render_offline(
    engine: *const c_char,
    note: u8,
    velocity: u8,
    out: *mut f32,
    n_frames: u32,
) -> i32 {
    if out.is_null() || n_frames == 0 {
        return 3;
    }
    let engine = match unsafe { CStr::from_ptr(engine) }.to_str() {
        Ok(s) => s,
        Err(_) => return 2,
    };
    let synth = match SynthBackend::load_native(engine) {
        Ok(s) => s,
        Err(e) => {
            eprintln!("audio-core: render_offline load failed: {e}");
            return 2;
        }
    };
    let samples = unsafe { std::slice::from_raw_parts_mut(out, n_frames as usize * 2) };
    let events = [FfiMidiEvent {
        frame: 0,
        kind: 0,
        channel: 0,
        data1: note,
        data2: velocity as u16,
    }];
    render_offline_core(synth, None, &events, samples);
    0
}

/// Offline (no-device) render of an arbitrary timed MIDI event sequence
/// (e.g. a parsed Standard MIDI File) through ANY patch type — not just
/// native. This is the general form behind measurement/calibration
/// tooling (docs/VISION.md's patch-loudness-normalization initiative):
/// render the same short performance through several patches and
/// compare, or sweep a parameter and check the loudness/peak
/// progression, all without opening an audio device.
///
/// `events` must be sorted by `frame` ascending — see
/// `render_offline_core`'s doc comment. Pass NULL/0 for no events (a
/// patch's idle render). `chain_params`, if non-NULL, overrides the
/// backend-agnostic post-synth chain defaults (drive pedal, analog
/// delay, master volume, ...) before rendering — see
/// [`FfiChainParams`]; pass NULL for every chain param at its engine
/// default. This is what lets calibration tooling sweep an effect's own
/// knob (not just which patch renders) through the same offline path.
///
/// Returns 0 on success, 2 on an unknown/unavailable `patch_type`, a
/// bad string, or a load failure, 3 if `out` is NULL or `n_frames` is 0.
///
/// # Safety
/// `patch_type` and `patch_ref` must be valid NUL-terminated C strings;
/// `plugin_id` must be a valid NUL-terminated C string or NULL;
/// `chain_params` must point to a valid [`FfiChainParams`] or be NULL;
/// `events` must point to at least `n_events` valid [`FfiMidiEvent`]s,
/// or be NULL iff `n_events` is 0; `out` must point to at least
/// `n_frames * 2` writable `f32`s.
#[no_mangle]
pub unsafe extern "C" fn polyclav_render_offline_events(
    patch_type: *const c_char,
    patch_ref: *const c_char,
    plugin_id: *const c_char,
    chain_params: *const FfiChainParams,
    events: *const FfiMidiEvent,
    n_events: u32,
    out: *mut f32,
    n_frames: u32,
) -> i32 {
    if out.is_null() || n_frames == 0 {
        return 3;
    }
    let patch_type = match unsafe { CStr::from_ptr(patch_type) }.to_str() {
        Ok(s) => s,
        Err(_) => return 2,
    };
    let patch_ref = match unsafe { CStr::from_ptr(patch_ref) }.to_str() {
        Ok(s) => s,
        Err(_) => return 2,
    };
    let plugin_id = if plugin_id.is_null() {
        None
    } else {
        match unsafe { CStr::from_ptr(plugin_id) }.to_str() {
            Ok(s) => Some(s),
            Err(_) => return 2,
        }
    };
    let chain_params = unsafe { chain_params.as_ref() };

    let synth = match load_synth_by_type(patch_type, patch_ref, plugin_id) {
        Ok(s) => s,
        Err(e) => {
            eprintln!("audio-core: render_offline_events load failed: {e}");
            return 2;
        }
    };

    let events_slice: &[FfiMidiEvent] = if n_events == 0 || events.is_null() {
        &[]
    } else {
        unsafe { std::slice::from_raw_parts(events, n_events as usize) }
    };
    let samples = unsafe { std::slice::from_raw_parts_mut(out, n_frames as usize * 2) };
    render_offline_core(synth, chain_params, events_slice, samples);
    0
}

/// Measure the integrated (ungated) LUFS loudness of an interleaved
/// stereo f32 buffer at 48 kHz — see `dsp::loudness` for exactly what
/// this does and does not measure. Meant for offline analysis of a
/// buffer produced by `polyclav_render_offline` (or any other
/// interleaved-stereo capture), not for use in the real-time audio
/// callback. Returns `-inf` for a NULL pointer, zero length, or true
/// silence.
///
/// # Safety
/// `samples` must point to at least `len` valid, readable `f32`s.
#[no_mangle]
pub unsafe extern "C" fn polyclav_measure_lufs(samples: *const f32, len: u32) -> f32 {
    if samples.is_null() || len == 0 {
        return f32::NEG_INFINITY;
    }
    let slice = unsafe { std::slice::from_raw_parts(samples, len as usize) };
    dsp::loudness::measure_lufs(slice)
}

/// Measure the peak level (dBFS) of an interleaved stereo f32 buffer.
/// Same lifecycle and NULL/empty/silence handling as
/// `polyclav_measure_lufs`.
///
/// # Safety
/// `samples` must point to at least `len` valid, readable `f32`s.
#[no_mangle]
pub unsafe extern "C" fn polyclav_measure_peak_dbfs(samples: *const f32, len: u32) -> f32 {
    if samples.is_null() || len == 0 {
        return f32::NEG_INFINITY;
    }
    let slice = unsafe { std::slice::from_raw_parts(samples, len as usize) };
    dsp::loudness::measure_peak_dbfs(slice)
}

#[cfg(target_os = "linux")]
fn run_audio(
    quit_flag: Arc<AtomicBool>,
    ready_tx: &mpsc::SyncSender<Result<(), String>>,
) -> Result<(), String> {
    pw::init();
    let main_loop = MainLoopRc::new(None).map_err(|e| format!("MainLoop::new: {e}"))?;
    let context = ContextRc::new(&main_loop, None).map_err(|e| format!("Context::new: {e}"))?;
    let core = context
        .connect_rc(None)
        .map_err(|e| format!("Core::connect: {e}"))?;

    // Requested quantum (buffer size). PipeWire treats node.latency as a
    // hint the graph may honor or clamp to its own min/max quantum, so the
    // effective latency is this-or-the-graph-minimum. See
    // `polyclav_audio_set_latency_frames`.
    let latency = LATENCY_FRAMES.load(Ordering::Relaxed);
    let node_latency = format!("{latency}/48000");
    eprintln!(
        "audio-core: requesting node.latency={node_latency} (~{:.2} ms)",
        latency as f32 / SAMPLE_RATE * 1000.0
    );
    let props = properties! {
        *pw::keys::MEDIA_TYPE => "Audio",
        *pw::keys::MEDIA_CATEGORY => "Playback",
        *pw::keys::MEDIA_ROLE => "Music",
        *pw::keys::NODE_NAME => "polyclav",
        *pw::keys::NODE_DESCRIPTION => "polyclav audio core",
        *pw::keys::APP_NAME => "polyclav",
        *pw::keys::NODE_LATENCY => node_latency.as_str(),
    };

    let stream =
        StreamBox::new(&core, "polyclav-output", props).map_err(|e| format!("Stream::new: {e}"))?;

    let user_data = build_user_data();

    let _listener = stream
        .add_local_listener_with_user_data(user_data)
        .process(process_audio)
        .register()
        .map_err(|e| format!("listener register: {e}"))?;

    let mut audio_info = AudioInfoRaw::new();
    audio_info.set_format(AudioFormat::F32LE);
    audio_info.set_rate(48000);
    audio_info.set_channels(2);

    let values: Vec<u8> = PodSerializer::serialize(
        std::io::Cursor::new(Vec::new()),
        &pod::Value::Object(pod::Object {
            type_: sys::SPA_TYPE_OBJECT_Format,
            id: sys::SPA_PARAM_EnumFormat,
            properties: audio_info.into(),
        }),
    )
    .unwrap()
    .0
    .into_inner();

    let mut params = [pod::Pod::from_bytes(&values).unwrap()];

    let flags = StreamFlags::AUTOCONNECT | StreamFlags::MAP_BUFFERS | StreamFlags::RT_PROCESS;
    stream
        .connect(Direction::Output, None, flags, &mut params)
        .map_err(|e| format!("stream connect: {e}"))?;

    let weak_loop = main_loop.downgrade();
    let timer = main_loop.loop_().add_timer(move |_| {
        if quit_flag.load(Ordering::Relaxed) {
            if let Some(ml) = weak_loop.upgrade() {
                ml.quit();
            }
        }
    });
    let _ = timer.update_timer(
        Some(Duration::from_millis(100)),
        Some(Duration::from_millis(100)),
    );

    eprintln!("audio-core: pipewire stream connected");
    let _ = ready_tx.send(Ok(()));

    main_loop.run();
    drop(timer);
    Ok(())
}

/// Swap in a freshly loaded synth backend if one is pending on the reload
/// queue. Generation-checked so rapid patch switches discard stale loads.
/// Portable — no OS audio API; shared by the PipeWire callback (Linux) and
/// the CoreAudio backend (macOS).
pub(crate) fn swap_pending_backend(user_data: &mut UserData) {
    if let Some((gen, new_backend)) = user_data.reload_queue.pop() {
        let current_gen = SOUNDFONT_GENERATION.load(Ordering::SeqCst);
        if gen == current_gen {
            let name = new_backend.name();
            user_data.synth = Some(new_backend);
            eprintln!("audio-core: synth swapped to backend={name} gen={gen}");
        } else {
            let name = new_backend.name();
            eprintln!(
                "audio-core: dropping stale backend={name} gen={gen} current_gen={current_gen}"
            );
            // new_backend dropped here — Rust will free underlying resources
        }
    }
}

/// Drain queued MIDI events into the active backend. Portable — no OS audio
/// API; shared by the PipeWire callback (Linux) and the CoreAudio backend
/// (macOS).
pub(crate) fn drain_midi(user_data: &mut UserData) {
    while let Some(event) = user_data.midi_queue.pop() {
        match user_data.synth {
            Some(SynthBackend::Oxi(ref mut s)) => {
                let oxi = match event {
                    MidiEvent::NoteOn {
                        channel,
                        note,
                        velocity,
                    } => OxiMidiEvent::NoteOn {
                        channel,
                        key: note,
                        vel: velocity,
                    },
                    MidiEvent::NoteOff { channel, note } => {
                        OxiMidiEvent::NoteOff { channel, key: note }
                    }
                    MidiEvent::ControlChange {
                        channel,
                        controller,
                        value,
                    } => OxiMidiEvent::ControlChange {
                        channel,
                        ctrl: controller,
                        value,
                    },
                    MidiEvent::PitchBend { channel, bend } => OxiMidiEvent::PitchBend {
                        channel,
                        value: bend,
                    },
                };
                let _ = s.send_event(oxi);
            }
            Some(SynthBackend::Sfizz(ref mut s)) => match event {
                MidiEvent::NoteOn { note, velocity, .. } => s.note_on(note, velocity),
                MidiEvent::NoteOff { note, .. } => s.note_off(note),
                MidiEvent::ControlChange {
                    controller, value, ..
                } => s.cc(controller, value),
                MidiEvent::PitchBend { bend, .. } => s.pitch_bend(bend),
            },
            #[cfg(target_os = "linux")]
            Some(SynthBackend::Lv2(ref mut s)) => s.push_midi(&event),
            #[cfg(target_os = "linux")]
            Some(SynthBackend::Clap(ref mut s)) => s.push_midi(&event),
            Some(SynthBackend::Native(ref mut s)) => s.handle_event(&event),
            None => {}
        }
    }
}

/// Render one block of audio: push the live native-synth params, dispatch the
/// active backend into `samples` (interleaved stereo f32, `len = frames * 2`,
/// 48 kHz), then run the DSP chain (patch gain → compressor → reverb →
/// mastering → limiter → master volume). Pure portable buffer math — no OS
/// audio API — so it is shared verbatim by the PipeWire callback (Linux) and
/// the CoreAudio backend (macOS), and is exercised directly by the offline
/// render tests.
pub(crate) fn render_block(user_data: &mut UserData, samples: &mut [f32]) {
    let n_frames = samples.len() / 2;

    // Phase 1 native-synth knob override: push the latest cutoff +
    // resonance atomics into the active native synth before
    // rendering. This is a no-op for other backends (the match arm
    // doesn't fire).
    if let Some(SynthBackend::Native(ref mut s)) = user_data.synth {
        s.set_cutoff_hz(user_data.dsp_params.native_cutoff_hz());
        s.set_resonance(user_data.dsp_params.native_resonance());
        let (aa, ad, asu, ar) = user_data.dsp_params.native_amp_env();
        s.set_amp_env(aa, ad, asu, ar);
        let (fa, fd, fs, fr, famt) = user_data.dsp_params.native_filter_env();
        s.set_filter_env(fa, fd, fs, fr, famt);
        for idx in 0..3 {
            let (wave, octave, detune_cents, level) = user_data.dsp_params.native_osc(idx);
            s.set_osc(idx, wave, octave, detune_cents, level);
        }
        s.set_noise_level(user_data.dsp_params.native_noise_level());
        s.set_glide(user_data.dsp_params.native_glide_s());
        s.set_pulse_width(user_data.dsp_params.native_pulse_width());
        s.set_drive(user_data.dsp_params.native_drive());
        let (vel_to_cutoff, vel_to_amp) = user_data.dsp_params.native_vel_routing();
        s.set_vel_routing(vel_to_cutoff, vel_to_amp);
        s.set_kbd_track(user_data.dsp_params.native_kbd_track());
        let (lfo_wave, lfo_rate, lfo_pitch, lfo_cutoff, lfo_amp) =
            user_data.dsp_params.native_lfo();
        s.set_lfo(lfo_wave, lfo_rate, lfo_pitch, lfo_cutoff, lfo_amp);
        s.set_bend_range(user_data.dsp_params.native_bend_range_semitones());
        s.set_voice_mode(user_data.dsp_params.native_voice_mode());
        s.set_oversample(user_data.dsp_params.native_oversample() != 0);
    }

    match user_data.synth {
        Some(SynthBackend::Oxi(ref mut s)) => s.write(&mut *samples),
        Some(SynthBackend::Sfizz(ref mut s)) => s.render(samples),
        #[cfg(target_os = "linux")]
        Some(SynthBackend::Lv2(ref mut s)) => s.render(samples),
        #[cfg(target_os = "linux")]
        Some(SynthBackend::Clap(ref mut s)) => s.render(samples),
        Some(SynthBackend::Native(ref mut s)) => s.render(samples),
        None => {
            for i in 0..n_frames {
                let s = 0.1 * (2.0 * std::f32::consts::PI * 440.0 * user_data.sine_phase).sin();
                samples[i * 2] = s;
                samples[i * 2 + 1] = s;
                user_data.sine_phase += 1.0 / 48000.0;
                if user_data.sine_phase >= 1.0 {
                    user_data.sine_phase -= 1.0;
                }
            }
        }
    }

    // 2.5. Drive pedal — runs on the raw synth output, before patch gain
    // and the rest of the dynamics chain, so its character is
    // independent of per-patch gain staging (matches an analog pedal
    // sitting in front of the amp) and applies identically regardless
    // of which synth backend produced `samples`.
    let drive_pedal_amount = user_data.dsp_params.drive_pedal_amount();
    if (drive_pedal_amount - user_data.last_drive_pedal_amount).abs() > f32::EPSILON {
        user_data.drive_pedal.set_amount(drive_pedal_amount);
        user_data.last_drive_pedal_amount = drive_pedal_amount;
    }
    if drive_pedal_amount > 0.0 {
        user_data.drive_pedal.process(samples);
    }

    // 2.6. Analog delay — pedalboard-order after drive (a delay
    // catches a drive pedal's output ahead of it, not the reverse),
    // before patch gain/dynamics for the same reason as the drive
    // pedal above. Params are pushed unconditionally each block — see
    // UserData's `analog_delay` field doc comment for why that's fine
    // here (trivial setters, no coefficient recomputation to skip).
    user_data
        .analog_delay
        .set_time_ms(user_data.dsp_params.analog_delay_time_ms());
    user_data
        .analog_delay
        .set_feedback(user_data.dsp_params.analog_delay_feedback());
    let analog_delay_mix = user_data.dsp_params.analog_delay_mix();
    user_data.analog_delay.set_mix(analog_delay_mix);
    if analog_delay_mix > 0.0 {
        user_data.analog_delay.process(samples);
    }

    // 3. DSP chain. Read parameters once per callback.
    let master = user_data.dsp_params.master_volume();
    let comp_amount = user_data.dsp_params.comp_amount();
    let reverb_mix = user_data.dsp_params.reverb_mix();
    let patch_gain = user_data.dsp_params.patch_gain();
    let mastering_amount = user_data.dsp_params.mastering_amount();
    let limiter_ceiling_db = user_data.dsp_params.limiter_ceiling_db();

    if (comp_amount - user_data.last_comp_amount).abs() > f32::EPSILON {
        user_data.compressor.set_amount(comp_amount);
        user_data.last_comp_amount = comp_amount;
    }
    if (reverb_mix - user_data.last_reverb_mix).abs() > f32::EPSILON {
        user_data.reverb.set_mix(reverb_mix);
        user_data.last_reverb_mix = reverb_mix;
    }
    if (mastering_amount - user_data.last_mastering_amount).abs() > f32::EPSILON {
        user_data.mastering.set_amount(mastering_amount);
        user_data.last_mastering_amount = mastering_amount;
    }
    if (limiter_ceiling_db - user_data.last_limiter_ceiling_db).abs() > f32::EPSILON {
        user_data.limiter.set_ceiling_db(limiter_ceiling_db);
        user_data.last_limiter_ceiling_db = limiter_ceiling_db;
    }

    // Patch gain first — direct multiply on every sample.
    if (patch_gain - 1.0).abs() > f32::EPSILON {
        for s in samples.iter_mut() {
            *s *= patch_gain;
        }
    }
    if comp_amount > 0.0 {
        user_data.compressor.process(samples);
    }
    if reverb_mix > 0.0 {
        user_data.reverb.process(samples);
    }
    if mastering_amount > 0.0 {
        user_data.mastering.process(samples);
    }
    // Limiter always runs — it is brick-wall safety, default ceiling -0.3 dBFS.
    user_data.limiter.process(samples);
    if (master - 1.0).abs() > f32::EPSILON {
        for s in samples.iter_mut() {
            *s *= master;
        }
    }
}

#[cfg(target_os = "linux")]
fn process_audio(stream: &pw::stream::Stream, user_data: &mut UserData) {
    // 1. Hot-swap soundfont if a freshly loaded backend is pending.
    swap_pending_backend(user_data);

    // 2. Drain MIDI events into the current synth.
    drain_midi(user_data);

    let raw = unsafe { stream.dequeue_raw_buffer() };
    if raw.is_null() {
        return;
    }
    let pw_buf = unsafe { &mut *raw };
    let spa_buf = unsafe { &mut *pw_buf.buffer };
    if spa_buf.n_datas == 0 {
        unsafe { stream.queue_raw_buffer(raw) };
        return;
    }
    let data = unsafe { &mut *spa_buf.datas };

    let stride = std::mem::size_of::<f32>() * 2;
    let max_frames = (data.maxsize as usize) / stride;
    let requested = pw_buf.requested as usize;
    let n_frames = if requested == 0 {
        max_frames.min(MAX_QUANTUM)
    } else {
        requested.min(max_frames)
    };

    if !data.data.is_null() && n_frames > 0 {
        let samples =
            unsafe { std::slice::from_raw_parts_mut(data.data.cast::<f32>(), n_frames * 2) };
        render_block(user_data, samples);
    }

    if !data.chunk.is_null() {
        let chunk = unsafe { &mut *data.chunk };
        chunk.offset = 0;
        chunk.stride = stride as i32;
        chunk.size = (n_frames * stride) as u32;
    }
    pw_buf.size = n_frames as u64;

    let n = user_data.callback_count;
    let frames_changed = n_frames != user_data.last_frames;
    if n < 10 || frames_changed {
        let backend = match user_data.synth {
            Some(SynthBackend::Oxi(_)) => "oxisynth",
            Some(SynthBackend::Sfizz(_)) => "sfizz",
            Some(SynthBackend::Lv2(_)) => "lv2",
            Some(SynthBackend::Clap(_)) => "clap",
            Some(SynthBackend::Native(_)) => "native",
            None => "sine-fallback",
        };
        eprintln!("audio-core: callback#{n} frames={n_frames} backend={backend}");
    }
    user_data.callback_count = n + 1;
    user_data.last_frames = n_frames;

    unsafe { stream.queue_raw_buffer(raw) };
}

#[cfg(test)]
mod tests {
    use super::*;

    /// `polyclav_audio_set_latency_frames` clamps to [MIN_QUANTUM,
    /// MAX_QUANTUM] and maps 0 to the default quantum. This is the config
    /// "buffer size / latency" knob; the value flows into PipeWire's
    /// node.latency on Linux and the CoreAudio buffer size on macOS.
    #[test]
    fn latency_frames_ffi_setter_clamps() {
        polyclav_audio_set_latency_frames(0);
        assert_eq!(LATENCY_FRAMES.load(Ordering::Relaxed), DEFAULT_QUANTUM);
        polyclav_audio_set_latency_frames(1);
        assert_eq!(LATENCY_FRAMES.load(Ordering::Relaxed), MIN_QUANTUM);
        polyclav_audio_set_latency_frames(1_000_000);
        assert_eq!(LATENCY_FRAMES.load(Ordering::Relaxed), MAX_QUANTUM as u32);
        polyclav_audio_set_latency_frames(256);
        assert_eq!(LATENCY_FRAMES.load(Ordering::Relaxed), 256);
        // Restore the default so a subsequent real start uses 128.
        polyclav_audio_set_latency_frames(0);
    }

    /// Build an isolated `UserData` (no globals, no PipeWire) around a synth
    /// backend, mirroring `run_audio`'s initialization. This is the offline
    /// render harness: it lets tests drive the exact `render_block` seam the
    /// real audio thread uses, on any platform, with no audio device.
    fn offline_user_data(synth: SynthBackend) -> UserData {
        build_offline_user_data(Some(synth))
    }

    /// The FFI wrapper is a thin, exact pass-through to
    /// `dsp::loudness::measure_lufs`/`measure_peak_dbfs` — pins that
    /// (no accidental rounding/rescaling at the boundary) plus the
    /// NULL/zero-length guards.
    #[test]
    fn measure_lufs_and_peak_ffi_match_direct_calls() {
        let samples: Vec<f32> = (0..4800)
            .map(|i| (2.0 * std::f32::consts::PI * 440.0 * i as f32 / 48000.0).sin() * 0.4)
            .collect();

        let direct_lufs = dsp::loudness::measure_lufs(&samples);
        let ffi_lufs = unsafe { polyclav_measure_lufs(samples.as_ptr(), samples.len() as u32) };
        assert_eq!(direct_lufs, ffi_lufs);

        let direct_peak = dsp::loudness::measure_peak_dbfs(&samples);
        let ffi_peak =
            unsafe { polyclav_measure_peak_dbfs(samples.as_ptr(), samples.len() as u32) };
        assert_eq!(direct_peak, ffi_peak);

        assert_eq!(
            unsafe { polyclav_measure_lufs(std::ptr::null(), 10) },
            f32::NEG_INFINITY
        );
        assert_eq!(
            unsafe { polyclav_measure_lufs(samples.as_ptr(), 0) },
            f32::NEG_INFINITY
        );
        assert_eq!(
            unsafe { polyclav_measure_peak_dbfs(std::ptr::null(), 10) },
            f32::NEG_INFINITY
        );
        assert_eq!(
            unsafe { polyclav_measure_peak_dbfs(samples.as_ptr(), 0) },
            f32::NEG_INFINITY
        );
    }

    /// `polyclav_render_offline` fills the caller's buffer with audible, finite
    /// audio from the native synth — the device-free path behind `polyclav
    /// render` and the CI offline-render gate. Also pins the null / zero-length
    /// guards and the bad-engine error code.
    #[test]
    fn render_offline_ffi_fills_buffer() {
        let engine = std::ffi::CString::new("minimoog").unwrap();
        let n_frames: u32 = 4800; // 0.1 s at 48 kHz
        let mut buf = vec![0.0f32; n_frames as usize * 2];
        let rc = unsafe {
            polyclav_render_offline(engine.as_ptr(), 60, 100, buf.as_mut_ptr(), n_frames)
        };
        assert_eq!(rc, 0, "render_offline returned error {rc}");
        assert!(buf.iter().all(|s| s.is_finite()), "non-finite sample");
        let rms = (buf
            .iter()
            .map(|s| f64::from(*s) * f64::from(*s))
            .sum::<f64>()
            / buf.len() as f64)
            .sqrt();
        assert!(
            rms > 0.001,
            "expected audible offline render, got rms={rms}"
        );

        // Guards: null out or zero frames -> 3; unknown engine -> 2.
        assert_eq!(
            unsafe {
                polyclav_render_offline(engine.as_ptr(), 60, 100, std::ptr::null_mut(), n_frames)
            },
            3
        );
        assert_eq!(
            unsafe { polyclav_render_offline(engine.as_ptr(), 60, 100, buf.as_mut_ptr(), 0) },
            3
        );
        let bad = std::ffi::CString::new("nope").unwrap();
        assert_eq!(
            unsafe { polyclav_render_offline(bad.as_ptr(), 60, 100, buf.as_mut_ptr(), n_frames) },
            2
        );
    }

    /// `polyclav_render_offline_events` is the general multi-event,
    /// any-patch-type sibling of `polyclav_render_offline` (measurement/
    /// calibration tooling — docs/VISION.md). Pins a real multi-note
    /// sequence (two overlapping-free notes back to back) through the
    /// native backend, plus the error-code guards.
    #[test]
    fn render_offline_events_ffi_handles_multi_note_sequence() {
        let patch_type = std::ffi::CString::new("native").unwrap();
        let patch_ref = std::ffi::CString::new("minimoog").unwrap();
        let n_frames: u32 = 9600; // 0.2 s at 48 kHz
        let events = [
            FfiMidiEvent {
                frame: 0,
                kind: 0,
                channel: 0,
                data1: 60,
                data2: 100,
            },
            FfiMidiEvent {
                frame: 2400,
                kind: 1,
                channel: 0,
                data1: 60,
                data2: 0,
            },
            FfiMidiEvent {
                frame: 2400,
                kind: 0,
                channel: 0,
                data1: 64,
                data2: 100,
            },
            FfiMidiEvent {
                frame: 4800,
                kind: 1,
                channel: 0,
                data1: 64,
                data2: 0,
            },
        ];
        let mut buf = vec![0.0f32; n_frames as usize * 2];
        let rc = unsafe {
            polyclav_render_offline_events(
                patch_type.as_ptr(),
                patch_ref.as_ptr(),
                std::ptr::null(),
                std::ptr::null(),
                events.as_ptr(),
                events.len() as u32,
                buf.as_mut_ptr(),
                n_frames,
            )
        };
        assert_eq!(rc, 0, "render_offline_events returned error {rc}");
        assert!(buf.iter().all(|s| s.is_finite()), "non-finite sample");
        let rms = (buf
            .iter()
            .map(|s| f64::from(*s) * f64::from(*s))
            .sum::<f64>()
            / buf.len() as f64)
            .sqrt();
        assert!(rms > 0.001, "expected audible output, got rms={rms}");

        // Guards: null out or zero frames -> 3; unknown patch_type -> 2.
        assert_eq!(
            unsafe {
                polyclav_render_offline_events(
                    patch_type.as_ptr(),
                    patch_ref.as_ptr(),
                    std::ptr::null(),
                    std::ptr::null(),
                    events.as_ptr(),
                    events.len() as u32,
                    std::ptr::null_mut(),
                    n_frames,
                )
            },
            3
        );
        assert_eq!(
            unsafe {
                polyclav_render_offline_events(
                    patch_type.as_ptr(),
                    patch_ref.as_ptr(),
                    std::ptr::null(),
                    std::ptr::null(),
                    events.as_ptr(),
                    events.len() as u32,
                    buf.as_mut_ptr(),
                    0,
                )
            },
            3
        );
        let bad_type = std::ffi::CString::new("bogus").unwrap();
        assert_eq!(
            unsafe {
                polyclav_render_offline_events(
                    bad_type.as_ptr(),
                    patch_ref.as_ptr(),
                    std::ptr::null(),
                    std::ptr::null(),
                    std::ptr::null(),
                    0,
                    buf.as_mut_ptr(),
                    n_frames,
                )
            },
            2
        );
    }

    /// A patch with no events at all renders its idle state for the
    /// whole buffer (silence for the native synth) — the "empty
    /// events" path must not panic or skip rendering.
    #[test]
    fn render_offline_events_ffi_handles_zero_events() {
        let patch_type = std::ffi::CString::new("native").unwrap();
        let patch_ref = std::ffi::CString::new("minimoog").unwrap();
        let n_frames: u32 = 4800;
        let mut buf = vec![0.0f32; n_frames as usize * 2];
        let rc = unsafe {
            polyclav_render_offline_events(
                patch_type.as_ptr(),
                patch_ref.as_ptr(),
                std::ptr::null(),
                std::ptr::null(),
                std::ptr::null(),
                0,
                buf.as_mut_ptr(),
                n_frames,
            )
        };
        assert_eq!(rc, 0);
        assert!(buf.iter().all(|s| s.is_finite()));
    }

    /// `chain_params` overrides actually reach the render: the same
    /// event sequence rendered once at defaults (drive pedal off) and
    /// once with `drive_pedal_amount = 1.0` must differ — the whole
    /// point of threading this through the FFI boundary
    /// (docs/VISION.md's calibration-tooling initiative).
    #[test]
    fn render_offline_events_ffi_applies_chain_params() {
        let patch_type = std::ffi::CString::new("native").unwrap();
        let patch_ref = std::ffi::CString::new("minimoog").unwrap();
        let n_frames: u32 = 24_000; // 0.5 s
        let events = [FfiMidiEvent {
            frame: 0,
            kind: 0,
            channel: 0,
            data1: 60,
            data2: 100,
        }];

        let render = |chain_params: *const FfiChainParams| -> Vec<f32> {
            let mut buf = vec![0.0f32; n_frames as usize * 2];
            let rc = unsafe {
                polyclav_render_offline_events(
                    patch_type.as_ptr(),
                    patch_ref.as_ptr(),
                    std::ptr::null(),
                    chain_params,
                    events.as_ptr(),
                    events.len() as u32,
                    buf.as_mut_ptr(),
                    n_frames,
                )
            };
            assert_eq!(rc, 0, "render_offline_events returned error {rc}");
            buf
        };

        let nan = f32::NAN;
        let default_buf = render(std::ptr::null());
        let driven_params = FfiChainParams {
            master_volume: nan,
            comp_amount: nan,
            reverb_mix: nan,
            patch_gain: nan,
            mastering_amount: nan,
            limiter_ceiling_db: nan,
            drive_pedal_amount: 1.0,
            analog_delay_time_ms: nan,
            analog_delay_feedback: nan,
            analog_delay_mix: nan,
        };
        let driven_buf = render(&driven_params as *const FfiChainParams);

        assert!(default_buf.iter().all(|s| s.is_finite()));
        assert!(driven_buf.iter().all(|s| s.is_finite()));
        assert_ne!(
            default_buf, driven_buf,
            "drive_pedal_amount override via chain_params had no effect"
        );

        // Full drive is loudness-matched to dry by design (v2's whole
        // calibration goal — see drive_pedal_full_wet_is_loudness_
        // matched_to_dry), so LUFS is the wrong signal for "did the
        // override really apply." Mean absolute sample difference over
        // the settled tail is: a saturated waveform is shaped very
        // differently sample-by-sample even at matched loudness.
        let settled = 12_000 * 2;
        let mean_abs_diff: f32 = default_buf[settled..]
            .iter()
            .zip(driven_buf[settled..].iter())
            .map(|(a, b)| (a - b).abs())
            .sum::<f32>()
            / (n_frames as usize - 12_000) as f32;
        assert!(
            mean_abs_diff > 0.01,
            "expected a real waveform-shape difference from the override, \
             got mean_abs_diff={mean_abs_diff:.4}"
        );
    }

    /// End-to-end offline render through the portable `render_block` seam:
    /// silence before any note, audible output after a NoteOn, and every
    /// sample finite. This is the device-free coverage that runs identically
    /// on Linux and macOS CI — it exercises native-param push, backend
    /// dispatch, and the full DSP chain without PipeWire or CoreAudio.
    #[test]
    fn render_block_native_offline_produces_finite_audio() {
        let synth = SynthBackend::load_native("minimoog").expect("native synth load");
        let mut ud = offline_user_data(synth);

        // Before any note: render a block and confirm it stays finite (and
        // essentially silent — the native synth idles).
        let mut warm = vec![0.0f32; 128 * 2];
        render_block(&mut ud, &mut warm);
        assert!(warm.iter().all(|s| s.is_finite()), "idle render not finite");

        // Play middle C, drain it into the synth, then render ~107 ms.
        // Queue capacity (64) comfortably holds one event; the queue returns
        // the value on overflow (MidiEvent has no Debug), so ignore the Result
        // exactly as the FFI push sites do.
        let _ = ud.midi_queue.push(MidiEvent::NoteOn {
            channel: 0,
            note: 60,
            velocity: 100,
        });
        drain_midi(&mut ud);

        let mut sumsq = 0.0f64;
        let mut count = 0usize;
        for _ in 0..40 {
            let mut block = vec![0.0f32; 128 * 2];
            render_block(&mut ud, &mut block);
            for &s in &block {
                assert!(s.is_finite(), "non-finite sample in rendered block");
                sumsq += f64::from(s) * f64::from(s);
                count += 1;
            }
        }
        let rms = (sumsq / count as f64).sqrt();
        assert!(
            rms > 0.001,
            "expected audible output through render_block, got rms={rms}"
        );
    }

    /// Render `n_frames` of a held middle-C minimoog note with the
    /// drive pedal set to `drive_pedal_amount`, returning the
    /// interleaved stereo buffer. Shared harness for the
    /// loudness-invariant tests below — reuses the exact offline-render
    /// path `polyclav_render_offline` exercises (`build_offline_user_data`
    /// and `render_block` in 128-frame blocks), so a clip generated here
    /// matches what a real render would produce.
    fn render_drive_pedal_clip(drive_pedal_amount: f32, n_frames: usize) -> Vec<f32> {
        let synth = SynthBackend::load_native("minimoog").expect("native synth load");
        let mut ud = offline_user_data(synth);
        ud.dsp_params.set_drive_pedal_amount(drive_pedal_amount);
        let _ = ud.midi_queue.push(MidiEvent::NoteOn {
            channel: 0,
            note: 60,
            velocity: 100,
        });
        drain_midi(&mut ud);

        let mut buf = vec![0.0f32; n_frames * 2];
        let mut done = 0usize;
        while done < n_frames {
            let this = 128usize.min(n_frames - done);
            render_block(&mut ud, &mut buf[done * 2..(done + this) * 2]);
            done += this;
        }
        buf
    }

    /// LUFS-based regression harness for the drive-pedal wet/dry mix
    /// (docs/OPEN_SOUND_ENGINES.md §1, docs/VISION.md's invariant-testing
    /// initiative). The bug this pins: a first version made 1% drive
    /// already sound maximally distorted — a discontinuous jump right
    /// off the bottom of the knob. This sweeps `amount` and checks the
    /// *loudness* progression is smooth (small steps between adjacent
    /// low settings, bounded steps everywhere) using
    /// `dsp::loudness::measure_lufs` on the settled portion of a real,
    /// full-chain rendered note — not just the pedal in isolation
    /// (`dsp::drive_pedal`'s own unit tests cover that in isolation).
    #[test]
    fn drive_pedal_loudness_sweep_is_smooth() {
        const N_FRAMES: usize = 24_000; // 0.5 s
        const SETTLE_FRAMES: usize = 12_000; // measure the back half only

        let amounts = [0.0, 0.01, 0.02, 0.05, 0.1, 0.2, 0.4, 0.6, 0.8, 1.0];
        let lufs: Vec<f32> = amounts
            .iter()
            .map(|&a| {
                let clip = render_drive_pedal_clip(a, N_FRAMES);
                dsp::loudness::measure_lufs(&clip[SETTLE_FRAMES * 2..])
            })
            .collect();

        // The regression this test exists for: going from off to a tiny
        // amount must NOT already sound maximally driven.
        let jump_from_zero = lufs[1] - lufs[0];
        assert!(
            jump_from_zero < 3.0,
            "1% drive jumped {jump_from_zero:.2} LU from dry \
             (amounts={amounts:?}, lufs={lufs:?})"
        );

        // No step anywhere in the sweep should be a cliff either.
        for w in lufs.windows(2) {
            let step = (w[1] - w[0]).abs();
            assert!(
                step < 4.0,
                "loudness step of {step:.2} LU between adjacent settings \
                 (amounts={amounts:?}, lufs={lufs:?})"
            );
        }
    }

    /// Cranking the drive should change character, not just get louder:
    /// fully wet should land close to the dry note's loudness
    /// (calibrated via `OUTPUT_MAKEUP` in `dsp/drive_pedal.rs`).
    #[test]
    fn drive_pedal_full_wet_is_loudness_matched_to_dry() {
        const N_FRAMES: usize = 24_000;
        const SETTLE_FRAMES: usize = 12_000;

        let dry = render_drive_pedal_clip(0.0, N_FRAMES);
        let wet = render_drive_pedal_clip(1.0, N_FRAMES);

        let dry_lufs = dsp::loudness::measure_lufs(&dry[SETTLE_FRAMES * 2..]);
        let wet_lufs = dsp::loudness::measure_lufs(&wet[SETTLE_FRAMES * 2..]);

        let delta = (wet_lufs - dry_lufs).abs();
        assert!(
            delta < 3.0,
            "full-wet drive should be loudness-matched to dry within 3 LU, \
             got dry={dry_lufs:.2} wet={wet_lufs:.2} delta={delta:.2}"
        );
    }

    /// Render `n_frames` of a held middle-C minimoog note with the
    /// analog delay set to the given feedback/mix (fixed 150ms time —
    /// short enough that several repeats land inside the render
    /// window), returning the interleaved stereo buffer. Same harness
    /// shape as `render_drive_pedal_clip`.
    fn render_analog_delay_clip(feedback: f32, mix: f32, n_frames: usize) -> Vec<f32> {
        let synth = SynthBackend::load_native("minimoog").expect("native synth load");
        let mut ud = offline_user_data(synth);
        ud.dsp_params.set_analog_delay_time_ms(150.0);
        ud.dsp_params.set_analog_delay_feedback(feedback);
        ud.dsp_params.set_analog_delay_mix(mix);
        let _ = ud.midi_queue.push(MidiEvent::NoteOn {
            channel: 0,
            note: 60,
            velocity: 100,
        });
        drain_midi(&mut ud);

        let mut buf = vec![0.0f32; n_frames * 2];
        let mut done = 0usize;
        while done < n_frames {
            let this = 128usize.min(n_frames - done);
            render_block(&mut ud, &mut buf[done * 2..(done + this) * 2]);
            done += this;
        }
        buf
    }

    /// Loudness-invariant sweep for the delay's feedback (repeats)
    /// knob, the same shape as `drive_pedal_loudness_sweep_is_smooth`:
    /// no discontinuous jump near 0, no cliff anywhere across the
    /// sweep. Measures over the FULL render (not just a settled tail —
    /// unlike a held note's steady state, a delay's character is in
    /// its build-up of repeats over time) at a fixed mix=1.0 so
    /// feedback is the only thing varying.
    #[test]
    fn analog_delay_feedback_sweep_is_smooth() {
        const N_FRAMES: usize = 48_000; // 1s — several 150ms repeats

        let feedbacks = [0.0, 0.01, 0.05, 0.1, 0.2, 0.4, 0.6, 0.9];
        let lufs: Vec<f32> = feedbacks
            .iter()
            .map(|&fb| {
                let clip = render_analog_delay_clip(fb, 1.0, N_FRAMES);
                dsp::loudness::measure_lufs(&clip)
            })
            .collect();

        for w in lufs.windows(2) {
            let step = (w[1] - w[0]).abs();
            assert!(
                step < 4.0,
                "loudness step of {step:.2} LU between adjacent feedback settings \
                 (feedbacks={feedbacks:?}, lufs={lufs:?})"
            );
        }
    }

    /// Loudness-invariant sweep for the delay's mix knob (feedback
    /// fixed at a high, repeat-heavy setting) — same smoothness
    /// invariant, different parameter.
    #[test]
    fn analog_delay_mix_sweep_is_smooth() {
        const N_FRAMES: usize = 48_000;

        let mixes = [0.0, 0.01, 0.05, 0.1, 0.25, 0.5, 0.75, 1.0];
        let lufs: Vec<f32> = mixes
            .iter()
            .map(|&mix| {
                let clip = render_analog_delay_clip(0.7, mix, N_FRAMES);
                dsp::loudness::measure_lufs(&clip)
            })
            .collect();

        for w in lufs.windows(2) {
            let step = (w[1] - w[0]).abs();
            assert!(
                step < 4.0,
                "loudness step of {step:.2} LU between adjacent mix settings \
                 (mixes={mixes:?}, lufs={lufs:?})"
            );
        }
    }

    /// End-to-end version of `dsp::analog_delay`'s own
    /// `analog_delay_repeats_stay_bounded` unit test, through the full
    /// chain (synth -> drive -> delay -> ... -> limiter) rather than
    /// the effect in isolation: peak stays under a sane bound even at
    /// max feedback over several seconds of held-note repeats.
    #[test]
    fn analog_delay_full_chain_peak_stays_bounded() {
        const N_FRAMES: usize = 48_000 * 4; // 4s

        let clip = render_analog_delay_clip(0.9, 1.0, N_FRAMES);
        assert!(clip.iter().all(|s| s.is_finite()), "non-finite sample");
        let peak_dbfs = dsp::loudness::measure_peak_dbfs(&clip);
        // The chain's limiter is brick-wall at -0.3 dBFS by default, so
        // this is generous headroom, not a tight calibration — it's
        // here to catch a genuine runaway, not to pin an exact number.
        assert!(
            peak_dbfs < 6.0,
            "peak {peak_dbfs:.2} dBFS suggests the delay's feedback loop \
             isn't staying bounded through the full chain"
        );
    }

    /// `native_resonance` defaults to the Minimoog factory 0.3 and
    /// clamps to [0.0, 0.95] (headroom below the Stilson/Smith ladder's
    /// self-oscillation instability).
    #[test]
    fn native_resonance_default_and_clamp() {
        let p = DspParams::new();
        assert_eq!(p.native_resonance(), 0.3);

        p.set_native_resonance(2.0);
        assert_eq!(p.native_resonance(), 0.95);

        p.set_native_resonance(-1.0);
        assert_eq!(p.native_resonance(), 0.0);

        p.set_native_resonance(0.5);
        assert_eq!(p.native_resonance(), 0.5);
    }

    /// The C-ABI setter routes through the same clamp into the global
    /// params the audio thread reads.
    #[test]
    fn native_resonance_ffi_setter_clamps() {
        polyclav_dsp_set_native_resonance(7.0);
        assert_eq!(dsp_params().native_resonance(), 0.95);
        polyclav_dsp_set_native_resonance(-0.25);
        assert_eq!(dsp_params().native_resonance(), 0.0);
        // Restore the default so other tests / later asserts see the
        // documented boot value.
        polyclav_dsp_set_native_resonance(0.3);
        assert_eq!(dsp_params().native_resonance(), 0.3);
    }

    /// Filter-env atomics boot at the §1.4 filter ADSR (5/600/0.4/600)
    /// with amount 0 (OFF — regression-safe), and clamp: times to
    /// [0.0001, 10] s, sustain and amount to [0, 1].
    #[test]
    fn native_filter_env_default_and_clamp() {
        let p = DspParams::new();
        assert_eq!(p.native_filter_env(), (0.005, 0.6, 0.4, 0.6, 0.0));

        p.set_native_filter_env(-1.0, 20.0, 1.5, 0.0, 2.0);
        assert_eq!(p.native_filter_env(), (1.0e-4, 10.0, 1.0, 1.0e-4, 1.0));

        p.set_native_filter_env(0.01, 0.2, 0.5, 0.3, 0.25);
        assert_eq!(p.native_filter_env(), (0.01, 0.2, 0.5, 0.3, 0.25));
    }

    /// The C ABI filter-env setter reaches the same global params (and
    /// clamps identically).
    #[test]
    fn native_filter_env_ffi_setter_clamps() {
        polyclav_dsp_set_native_filter_env(100.0, -5.0, -1.0, 100.0, -3.0);
        assert_eq!(
            dsp_params().native_filter_env(),
            (10.0, 1.0e-4, 0.0, 10.0, 0.0)
        );
        // Restore the defaults so other tests / later asserts see the
        // documented boot values.
        polyclav_dsp_set_native_filter_env(0.005, 0.6, 0.4, 0.6, 0.0);
        assert_eq!(
            dsp_params().native_filter_env(),
            (0.005, 0.6, 0.4, 0.6, 0.0)
        );
    }

    /// Oscillator-bank atomics boot at the stage-3 defaults (osc 1
    /// saw/0/0¢/1.0, osc 2 saw/0/-7¢/0.0, osc 3 saw/-1/+5¢/0.0, noise
    /// 0.0) and clamp octave to [-2, 2], detune to [-100, 100], level
    /// and noise to [0, 1].
    #[test]
    fn native_osc_defaults_and_clamp() {
        let p = DspParams::new();
        assert_eq!(p.native_osc(0), (0, 0, 0.0, 1.0));
        assert_eq!(p.native_osc(1), (0, 0, -7.0, 0.0));
        assert_eq!(p.native_osc(2), (0, -1, 5.0, 0.0));
        assert_eq!(p.native_noise_level(), 0.0);

        p.set_native_osc(1, 2, -5, -500.0, 3.0);
        assert_eq!(p.native_osc(1), (2, -2, -100.0, 1.0));

        p.set_native_osc(1, 1, 1, 12.5, 0.25);
        assert_eq!(p.native_osc(1), (1, 1, 12.5, 0.25));

        p.set_native_noise_level(2.0);
        assert_eq!(p.native_noise_level(), 1.0);
        p.set_native_noise_level(-1.0);
        assert_eq!(p.native_noise_level(), 0.0);
    }

    /// The C-ABI osc setter routes through the same clamps into the
    /// global params, and ignores out-of-range idx / wave codes.
    #[test]
    fn native_osc_ffi_setter_clamps_and_ignores_bad_input() {
        polyclav_dsp_set_native_osc(2, 1, 5, 250.0, -1.0);
        assert_eq!(dsp_params().native_osc(2), (1, 2, 100.0, 0.0));

        // Out-of-range idx and wave are ignored (values unchanged).
        polyclav_dsp_set_native_osc(3, 0, 0, 0.0, 1.0);
        polyclav_dsp_set_native_osc(-1, 0, 0, 0.0, 1.0);
        polyclav_dsp_set_native_osc(2, 7, 0, 0.0, 1.0);
        polyclav_dsp_set_native_osc(2, -1, 0, 0.0, 1.0);
        assert_eq!(dsp_params().native_osc(2), (1, 2, 100.0, 0.0));

        // Restore the defaults so other tests / later asserts see the
        // documented boot values.
        polyclav_dsp_set_native_osc(2, 0, -1, 5.0, 0.0);
        assert_eq!(dsp_params().native_osc(2), (0, -1, 5.0, 0.0));
    }

    /// The C-ABI noise setter clamps into the global params.
    #[test]
    fn native_noise_ffi_setter_clamps() {
        polyclav_dsp_set_native_noise(9.0);
        assert_eq!(dsp_params().native_noise_level(), 1.0);
        // Restore the default.
        polyclav_dsp_set_native_noise(0.0);
        assert_eq!(dsp_params().native_noise_level(), 0.0);
    }

    /// Glide atomic boots at 0.0 s (no slew — regression-safe) and
    /// clamps to [0, 5] seconds.
    #[test]
    fn native_glide_default_and_clamp() {
        let p = DspParams::new();
        assert_eq!(p.native_glide_s(), 0.0);

        p.set_native_glide_s(9.0);
        assert_eq!(p.native_glide_s(), 5.0);

        p.set_native_glide_s(-1.0);
        assert_eq!(p.native_glide_s(), 0.0);

        p.set_native_glide_s(0.25);
        assert_eq!(p.native_glide_s(), 0.25);
    }

    /// The C-ABI glide setter routes through the same clamp into the
    /// global params the audio thread reads.
    #[test]
    fn native_glide_ffi_setter_clamps() {
        polyclav_dsp_set_native_glide(100.0);
        assert_eq!(dsp_params().native_glide_s(), 5.0);
        polyclav_dsp_set_native_glide(-3.0);
        assert_eq!(dsp_params().native_glide_s(), 0.0);
        // Restore the default so other tests / later asserts see the
        // documented boot value.
        polyclav_dsp_set_native_glide(0.0);
        assert_eq!(dsp_params().native_glide_s(), 0.0);
    }

    /// Amp-env atomics boot at the §1.4 amp ADSR (5/200/0.7/400 — the
    /// previously-hardcoded voice values, regression-safe) and clamp:
    /// times to [0.0001, 10] s, sustain to [0, 1].
    #[test]
    fn native_amp_env_default_and_clamp() {
        let p = DspParams::new();
        assert_eq!(p.native_amp_env(), (0.005, 0.2, 0.7, 0.4));

        p.set_native_amp_env(-1.0, 20.0, 1.5, 0.0);
        assert_eq!(p.native_amp_env(), (1.0e-4, 10.0, 1.0, 1.0e-4));

        p.set_native_amp_env(0.01, 0.2, 0.5, 0.3);
        assert_eq!(p.native_amp_env(), (0.01, 0.2, 0.5, 0.3));
    }

    /// The C ABI amp-env setter reaches the same global params (and
    /// clamps identically).
    #[test]
    fn native_amp_env_ffi_setter_clamps() {
        polyclav_dsp_set_native_amp_env(100.0, -5.0, -1.0, 100.0);
        assert_eq!(dsp_params().native_amp_env(), (10.0, 1.0e-4, 0.0, 10.0));
        // Restore the defaults so other tests / later asserts see the
        // documented boot values.
        polyclav_dsp_set_native_amp_env(0.005, 0.2, 0.7, 0.4);
        assert_eq!(dsp_params().native_amp_env(), (0.005, 0.2, 0.7, 0.4));
    }

    /// Pulse-width atomic boots at 0.25 (the old fixed stage-3 duty —
    /// regression-safe) and clamps to [0.05, 0.95].
    #[test]
    fn native_pulse_width_default_and_clamp() {
        let p = DspParams::new();
        assert_eq!(p.native_pulse_width(), 0.25);

        p.set_native_pulse_width(2.0);
        assert_eq!(p.native_pulse_width(), 0.95);

        p.set_native_pulse_width(-1.0);
        assert_eq!(p.native_pulse_width(), 0.05);

        p.set_native_pulse_width(0.5);
        assert_eq!(p.native_pulse_width(), 0.5);
    }

    /// The C-ABI pulse-width setter routes through the same clamp into
    /// the global params the audio thread reads.
    #[test]
    fn native_pulse_width_ffi_setter_clamps() {
        polyclav_dsp_set_native_pulse_width(7.0);
        assert_eq!(dsp_params().native_pulse_width(), 0.95);
        polyclav_dsp_set_native_pulse_width(-0.25);
        assert_eq!(dsp_params().native_pulse_width(), 0.05);
        // Restore the default so other tests / later asserts see the
        // documented boot value.
        polyclav_dsp_set_native_pulse_width(0.25);
        assert_eq!(dsp_params().native_pulse_width(), 0.25);
    }

    /// Drive atomic boots at 0.0 (bit-exact bypass — regression-safe)
    /// and clamps to [0, 1].
    #[test]
    fn native_drive_default_and_clamp() {
        let p = DspParams::new();
        assert_eq!(p.native_drive(), 0.0);

        p.set_native_drive(2.0);
        assert_eq!(p.native_drive(), 1.0);

        p.set_native_drive(-1.0);
        assert_eq!(p.native_drive(), 0.0);

        p.set_native_drive(0.3);
        assert_eq!(p.native_drive(), 0.3);
    }

    /// The C-ABI drive setter routes through the same clamp into the
    /// global params the audio thread reads.
    #[test]
    fn native_drive_ffi_setter_clamps() {
        polyclav_dsp_set_native_drive(7.0);
        assert_eq!(dsp_params().native_drive(), 1.0);
        polyclav_dsp_set_native_drive(-0.25);
        assert_eq!(dsp_params().native_drive(), 0.0);
        // Restore the default so other tests / later asserts see the
        // documented boot value.
        polyclav_dsp_set_native_drive(0.0);
        assert_eq!(dsp_params().native_drive(), 0.0);
    }

    /// Velocity-routing atomics boot at (to_cutoff 0.0, to_amp 1.0) —
    /// the bit-transparent defaults (to_amp 1 is exactly the classic
    /// vel/127 scaling) — and clamp both to [0, 1].
    #[test]
    fn native_vel_routing_default_and_clamp() {
        let p = DspParams::new();
        assert_eq!(p.native_vel_routing(), (0.0, 1.0));

        p.set_native_vel_routing(2.0, -1.0);
        assert_eq!(p.native_vel_routing(), (1.0, 0.0));

        p.set_native_vel_routing(-1.0, 2.0);
        assert_eq!(p.native_vel_routing(), (0.0, 1.0));

        p.set_native_vel_routing(0.25, 0.5);
        assert_eq!(p.native_vel_routing(), (0.25, 0.5));
    }

    /// The C-ABI vel-routing setter routes through the same clamps into
    /// the global params the audio thread reads.
    #[test]
    fn native_vel_routing_ffi_setter_clamps() {
        polyclav_dsp_set_native_vel_routing(7.0, -2.0);
        assert_eq!(dsp_params().native_vel_routing(), (1.0, 0.0));
        // Restore the defaults so other tests / later asserts see the
        // documented boot values.
        polyclav_dsp_set_native_vel_routing(0.0, 1.0);
        assert_eq!(dsp_params().native_vel_routing(), (0.0, 1.0));
    }

    /// Keyboard-tracking atomic boots at 0.0 (bypass — regression-safe)
    /// and clamps to [0, 1].
    #[test]
    fn native_kbd_track_default_and_clamp() {
        let p = DspParams::new();
        assert_eq!(p.native_kbd_track(), 0.0);

        p.set_native_kbd_track(2.0);
        assert_eq!(p.native_kbd_track(), 1.0);

        p.set_native_kbd_track(-1.0);
        assert_eq!(p.native_kbd_track(), 0.0);

        p.set_native_kbd_track(0.5);
        assert_eq!(p.native_kbd_track(), 0.5);
    }

    /// The C-ABI kbd-track setter routes through the same clamp into
    /// the global params the audio thread reads.
    #[test]
    fn native_kbd_track_ffi_setter_clamps() {
        polyclav_dsp_set_native_kbd_track(7.0);
        assert_eq!(dsp_params().native_kbd_track(), 1.0);
        polyclav_dsp_set_native_kbd_track(-0.25);
        assert_eq!(dsp_params().native_kbd_track(), 0.0);
        // Restore the default so other tests / later asserts see the
        // documented boot value.
        polyclav_dsp_set_native_kbd_track(0.0);
        assert_eq!(dsp_params().native_kbd_track(), 0.0);
    }

    /// GLOBAL LFO atomics boot at (triangle, 5 Hz, all depths 0 —
    /// bit-transparent) and clamp: rate to [0.05, 20] Hz, pitch depth
    /// to [0, 100] cents, cutoff depth to [0, 2] octaves, amp depth to
    /// [0, 1]; the wave code is capped at 3 as a backstop.
    #[test]
    fn native_lfo_default_and_clamp() {
        let p = DspParams::new();
        assert_eq!(p.native_lfo(), (0, 5.0, 0.0, 0.0, 0.0));

        p.set_native_lfo(9, 100.0, 500.0, 7.0, 3.0);
        assert_eq!(p.native_lfo(), (3, 20.0, 100.0, 2.0, 1.0));

        p.set_native_lfo(1, 0.0, -5.0, -1.0, -1.0);
        assert_eq!(p.native_lfo(), (1, 0.05, 0.0, 0.0, 0.0));

        p.set_native_lfo(2, 4.0, 30.0, 0.5, 0.25);
        assert_eq!(p.native_lfo(), (2, 4.0, 30.0, 0.5, 0.25));
    }

    /// The C-ABI LFO setter routes through the same clamps into the
    /// global params, and ignores out-of-range wave codes entirely.
    #[test]
    fn native_lfo_ffi_setter_clamps_and_ignores_bad_wave() {
        polyclav_dsp_set_native_lfo(3, 100.0, 500.0, 7.0, 3.0);
        assert_eq!(dsp_params().native_lfo(), (3, 20.0, 100.0, 2.0, 1.0));

        // Out-of-range wave is ignored (values unchanged).
        polyclav_dsp_set_native_lfo(4, 5.0, 0.0, 0.0, 0.0);
        assert_eq!(dsp_params().native_lfo(), (3, 20.0, 100.0, 2.0, 1.0));

        // Restore the defaults so other tests / later asserts see the
        // documented boot values.
        polyclav_dsp_set_native_lfo(0, 5.0, 0.0, 0.0, 0.0);
        assert_eq!(dsp_params().native_lfo(), (0, 5.0, 0.0, 0.0, 0.0));
    }

    /// Bend-range atomic boots at 2.0 semitones (the MIDI convention)
    /// and clamps to [0, 12].
    #[test]
    fn native_bend_range_default_and_clamp() {
        let p = DspParams::new();
        assert_eq!(p.native_bend_range_semitones(), 2.0);

        p.set_native_bend_range(100.0);
        assert_eq!(p.native_bend_range_semitones(), 12.0);

        p.set_native_bend_range(-1.0);
        assert_eq!(p.native_bend_range_semitones(), 0.0);

        p.set_native_bend_range(7.0);
        assert_eq!(p.native_bend_range_semitones(), 7.0);
    }

    /// The C-ABI bend-range setter routes through the same clamp into
    /// the global params the audio thread reads.
    #[test]
    fn native_bend_range_ffi_setter_clamps() {
        polyclav_dsp_set_native_bend_range(100.0);
        assert_eq!(dsp_params().native_bend_range_semitones(), 12.0);
        polyclav_dsp_set_native_bend_range(-3.0);
        assert_eq!(dsp_params().native_bend_range_semitones(), 0.0);
        // Restore the default so other tests / later asserts see the
        // documented boot value.
        polyclav_dsp_set_native_bend_range(2.0);
        assert_eq!(dsp_params().native_bend_range_semitones(), 2.0);
    }

    /// Voice-mode atomic boots at 0 (mono_legato — regression-safe)
    /// and caps the wire code at 2 as a backstop.
    #[test]
    fn native_voice_mode_default_and_backstop() {
        let p = DspParams::new();
        assert_eq!(p.native_voice_mode(), 0);

        p.set_native_voice_mode(2);
        assert_eq!(p.native_voice_mode(), 2);

        p.set_native_voice_mode(9);
        assert_eq!(p.native_voice_mode(), 2);

        p.set_native_voice_mode(1);
        assert_eq!(p.native_voice_mode(), 1);
    }

    /// The C-ABI voice-mode setter reaches the global params and
    /// ignores out-of-range codes entirely (values unchanged).
    #[test]
    fn native_voice_mode_ffi_setter_validates() {
        polyclav_dsp_set_native_voice_mode(2);
        assert_eq!(dsp_params().native_voice_mode(), 2);

        // Out-of-range mode is ignored (value unchanged).
        polyclav_dsp_set_native_voice_mode(3);
        assert_eq!(dsp_params().native_voice_mode(), 2);

        // Restore the default so other tests / later asserts see the
        // documented boot value.
        polyclav_dsp_set_native_voice_mode(0);
        assert_eq!(dsp_params().native_voice_mode(), 0);
    }

    /// Oversample atomic boots at 0 (off — regression-safe) and caps
    /// the wire code at 1 as a backstop.
    #[test]
    fn native_oversample_default_and_backstop() {
        let p = DspParams::new();
        assert_eq!(p.native_oversample(), 0);

        p.set_native_oversample(1);
        assert_eq!(p.native_oversample(), 1);

        p.set_native_oversample(9);
        assert_eq!(p.native_oversample(), 1);

        p.set_native_oversample(0);
        assert_eq!(p.native_oversample(), 0);
    }

    /// The C-ABI oversample setter reaches the global params and
    /// ignores out-of-range codes entirely (values unchanged).
    #[test]
    fn native_oversample_ffi_setter_validates() {
        polyclav_dsp_set_native_oversample(1);
        assert_eq!(dsp_params().native_oversample(), 1);

        // Out-of-range code is ignored (value unchanged).
        polyclav_dsp_set_native_oversample(2);
        assert_eq!(dsp_params().native_oversample(), 1);

        // Restore the default so other tests / later asserts see the
        // documented boot value.
        polyclav_dsp_set_native_oversample(0);
        assert_eq!(dsp_params().native_oversample(), 0);
    }

    /// The three non-finite f32 values every setter must reject.
    const NON_FINITE: [f32; 3] = [f32::NAN, f32::INFINITY, f32::NEG_INFINITY];

    /// Every f32-taking setter rejects NaN/±inf: the stored value is
    /// unchanged (previous good value kept), never poisoned. This pins
    /// the `store_clamped` non-finite guard — before it, one NaN
    /// through any `polyclav_dsp_set_native_*` FFI setter permanently
    /// poisoned DSP state (`f32::clamp` propagates NaN).
    #[test]
    fn setters_reject_non_finite_values() {
        let p = DspParams::new();
        // Move every field off its default so "unchanged" is a real
        // assertion (not just "still the boot value").
        p.set_master_volume(0.5);
        p.set_comp_amount(0.25);
        p.set_reverb_mix(0.75);
        p.set_patch_gain(2.0);
        p.set_mastering_amount(0.5);
        p.set_limiter_ceiling_db(-6.0);
        p.set_drive_pedal_amount(0.5);
        p.set_analog_delay_time_ms(250.0);
        p.set_analog_delay_feedback(0.4);
        p.set_analog_delay_mix(0.6);
        p.set_native_cutoff_hz(1_234.0);
        p.set_native_resonance(0.5);
        p.set_native_filter_env(0.01, 0.2, 0.5, 0.3, 0.25);
        p.set_native_amp_env(0.02, 0.3, 0.6, 0.5);
        p.set_native_osc(1, 1, 1, 12.5, 0.25);
        p.set_native_noise_level(0.5);
        p.set_native_glide_s(0.25);
        p.set_native_pulse_width(0.5);
        p.set_native_drive(0.5);
        p.set_native_vel_routing(0.25, 0.5);
        p.set_native_kbd_track(0.75);
        p.set_native_lfo(2, 4.0, 30.0, 0.5, 0.25);
        p.set_native_bend_range(7.0);

        for bad in NON_FINITE {
            p.set_master_volume(bad);
            p.set_comp_amount(bad);
            p.set_reverb_mix(bad);
            p.set_patch_gain(bad);
            p.set_mastering_amount(bad);
            p.set_limiter_ceiling_db(bad);
            p.set_drive_pedal_amount(bad);
            p.set_analog_delay_time_ms(bad);
            p.set_analog_delay_feedback(bad);
            p.set_analog_delay_mix(bad);
            p.set_native_cutoff_hz(bad);
            p.set_native_resonance(bad);
            p.set_native_filter_env(bad, bad, bad, bad, bad);
            p.set_native_amp_env(bad, bad, bad, bad);
            p.set_native_osc(1, 1, 1, bad, bad);
            p.set_native_noise_level(bad);
            p.set_native_glide_s(bad);
            p.set_native_pulse_width(bad);
            p.set_native_drive(bad);
            p.set_native_vel_routing(bad, bad);
            p.set_native_kbd_track(bad);
            p.set_native_lfo(2, bad, bad, bad, bad);
            p.set_native_bend_range(bad);

            assert_eq!(p.master_volume(), 0.5, "master_volume poisoned by {bad}");
            assert_eq!(p.comp_amount(), 0.25, "comp_amount poisoned by {bad}");
            assert_eq!(p.reverb_mix(), 0.75, "reverb_mix poisoned by {bad}");
            assert_eq!(p.patch_gain(), 2.0, "patch_gain poisoned by {bad}");
            assert_eq!(
                p.mastering_amount(),
                0.5,
                "mastering_amount poisoned by {bad}"
            );
            assert_eq!(
                p.limiter_ceiling_db(),
                -6.0,
                "limiter_ceiling_db poisoned by {bad}"
            );
            assert_eq!(
                p.drive_pedal_amount(),
                0.5,
                "drive_pedal_amount poisoned by {bad}"
            );
            assert_eq!(
                p.analog_delay_time_ms(),
                250.0,
                "analog_delay_time_ms poisoned by {bad}"
            );
            assert_eq!(
                p.analog_delay_feedback(),
                0.4,
                "analog_delay_feedback poisoned by {bad}"
            );
            assert_eq!(
                p.analog_delay_mix(),
                0.6,
                "analog_delay_mix poisoned by {bad}"
            );
            assert_eq!(
                p.native_cutoff_hz(),
                1_234.0,
                "native_cutoff_hz poisoned by {bad}"
            );
            assert_eq!(
                p.native_resonance(),
                0.5,
                "native_resonance poisoned by {bad}"
            );
            assert_eq!(
                p.native_filter_env(),
                (0.01, 0.2, 0.5, 0.3, 0.25),
                "native_filter_env poisoned by {bad}"
            );
            assert_eq!(
                p.native_amp_env(),
                (0.02, 0.3, 0.6, 0.5),
                "native_amp_env poisoned by {bad}"
            );
            assert_eq!(
                p.native_osc(1),
                (1, 1, 12.5, 0.25),
                "native_osc poisoned by {bad}"
            );
            assert_eq!(
                p.native_noise_level(),
                0.5,
                "native_noise_level poisoned by {bad}"
            );
            assert_eq!(p.native_glide_s(), 0.25, "native_glide_s poisoned by {bad}");
            assert_eq!(
                p.native_pulse_width(),
                0.5,
                "native_pulse_width poisoned by {bad}"
            );
            assert_eq!(p.native_drive(), 0.5, "native_drive poisoned by {bad}");
            assert_eq!(
                p.native_vel_routing(),
                (0.25, 0.5),
                "native_vel_routing poisoned by {bad}"
            );
            assert_eq!(
                p.native_kbd_track(),
                0.75,
                "native_kbd_track poisoned by {bad}"
            );
            assert_eq!(
                p.native_lfo(),
                (2, 4.0, 30.0, 0.5, 0.25),
                "native_lfo poisoned by {bad}"
            );
            assert_eq!(
                p.native_bend_range_semitones(),
                7.0,
                "native_bend_range poisoned by {bad}"
            );
        }
    }

    /// Non-finite rejection is per-field on the multi-arg setters: a
    /// NaN argument keeps that field's previous value while the finite
    /// arguments in the same call still apply.
    #[test]
    fn multi_arg_setters_reject_non_finite_per_field() {
        let p = DspParams::new();
        p.set_native_filter_env(0.01, 0.2, 0.5, 0.3, 0.25);
        p.set_native_filter_env(f32::NAN, 0.3, f32::INFINITY, 0.4, 0.5);
        assert_eq!(p.native_filter_env(), (0.01, 0.3, 0.5, 0.4, 0.5));

        p.set_native_osc(1, 1, 1, 12.5, 0.25);
        p.set_native_osc(1, 2, -1, f32::NAN, 0.75);
        assert_eq!(p.native_osc(1), (2, -1, 12.5, 0.75));
        p.set_native_osc(1, 0, 0, -7.0, f32::NEG_INFINITY);
        assert_eq!(p.native_osc(1), (0, 0, -7.0, 0.75));

        p.set_native_amp_env(0.02, 0.3, 0.6, 0.5);
        p.set_native_amp_env(f32::NAN, 0.4, f32::INFINITY, 0.6);
        assert_eq!(p.native_amp_env(), (0.02, 0.4, 0.6, 0.6));

        p.set_native_vel_routing(0.25, 0.5);
        p.set_native_vel_routing(f32::NAN, 0.75);
        assert_eq!(p.native_vel_routing(), (0.25, 0.75));
        p.set_native_vel_routing(0.5, f32::NEG_INFINITY);
        assert_eq!(p.native_vel_routing(), (0.5, 0.75));

        p.set_native_lfo(2, 4.0, 30.0, 0.5, 0.25);
        p.set_native_lfo(1, f32::NAN, 60.0, f32::INFINITY, 0.75);
        assert_eq!(p.native_lfo(), (1, 4.0, 60.0, 0.5, 0.75));
    }

    /// The C ABI setters route through the same non-finite rejection.
    /// This test only touches the ten generic DSP globals (volume /
    /// compressor / reverb / patch gain / mastering / limiter / drive
    /// pedal / analog delay time+feedback+mix) — the native_* globals
    /// are owned by the other FFI tests, and tests run in parallel in
    /// one process.
    #[test]
    fn ffi_setters_reject_non_finite_values() {
        polyclav_dsp_set_master_volume(0.5);
        polyclav_dsp_set_compressor(0.25);
        polyclav_dsp_set_reverb(0.75);
        polyclav_dsp_set_patch_gain(2.0);
        polyclav_dsp_set_mastering_compressor(0.5);
        polyclav_dsp_set_limiter_ceiling_db(-6.0);
        polyclav_dsp_set_drive_pedal(0.5);
        polyclav_dsp_set_analog_delay_time_ms(250.0);
        polyclav_dsp_set_analog_delay_feedback(0.4);
        polyclav_dsp_set_analog_delay_mix(0.6);

        for bad in NON_FINITE {
            polyclav_dsp_set_master_volume(bad);
            polyclav_dsp_set_compressor(bad);
            polyclav_dsp_set_reverb(bad);
            polyclav_dsp_set_patch_gain(bad);
            polyclav_dsp_set_mastering_compressor(bad);
            polyclav_dsp_set_limiter_ceiling_db(bad);
            polyclav_dsp_set_drive_pedal(bad);
            polyclav_dsp_set_analog_delay_time_ms(bad);
            polyclav_dsp_set_analog_delay_feedback(bad);
            polyclav_dsp_set_analog_delay_mix(bad);

            let p = dsp_params();
            assert_eq!(p.master_volume(), 0.5, "master_volume poisoned by {bad}");
            assert_eq!(p.comp_amount(), 0.25, "comp_amount poisoned by {bad}");
            assert_eq!(p.reverb_mix(), 0.75, "reverb_mix poisoned by {bad}");
            assert_eq!(p.patch_gain(), 2.0, "patch_gain poisoned by {bad}");
            assert_eq!(
                p.mastering_amount(),
                0.5,
                "mastering_amount poisoned by {bad}"
            );
            assert_eq!(
                p.limiter_ceiling_db(),
                -6.0,
                "limiter_ceiling_db poisoned by {bad}"
            );
            assert_eq!(
                p.drive_pedal_amount(),
                0.5,
                "drive_pedal_amount poisoned by {bad}"
            );
            assert_eq!(
                p.analog_delay_time_ms(),
                250.0,
                "analog_delay_time_ms poisoned by {bad}"
            );
            assert_eq!(
                p.analog_delay_feedback(),
                0.4,
                "analog_delay_feedback poisoned by {bad}"
            );
            assert_eq!(
                p.analog_delay_mix(),
                0.6,
                "analog_delay_mix poisoned by {bad}"
            );
        }

        // Restore the documented boot values.
        polyclav_dsp_set_master_volume(1.0);
        polyclav_dsp_set_compressor(0.0);
        polyclav_dsp_set_reverb(0.0);
        polyclav_dsp_set_patch_gain(1.0);
        polyclav_dsp_set_mastering_compressor(0.0);
        polyclav_dsp_set_limiter_ceiling_db(-0.3);
        polyclav_dsp_set_drive_pedal(0.0);
        polyclav_dsp_set_analog_delay_time_ms(300.0);
        polyclav_dsp_set_analog_delay_feedback(0.0);
        polyclav_dsp_set_analog_delay_mix(0.0);
    }

    /// End-to-end poisoning attempt: push NaN/±inf through every
    /// native_* param, then mirror the audio thread's per-block push
    /// (`process_audio`) into a real `NativeSynth` and render a note.
    /// Every param the audio thread reads must still be finite, and the
    /// rendered audio must be finite (and audible).
    #[test]
    fn non_finite_ffi_values_keep_render_finite() {
        let p = DspParams::new();
        p.set_native_cutoff_hz(f32::NAN);
        p.set_native_resonance(f32::INFINITY);
        p.set_native_filter_env(f32::NAN, f32::NAN, f32::NAN, f32::NAN, f32::NAN);
        for idx in 0..3 {
            p.set_native_osc(idx, 0, 0, f32::NAN, f32::NEG_INFINITY);
        }
        p.set_native_noise_level(f32::NAN);
        p.set_native_glide_s(f32::NAN);
        p.set_native_vel_routing(f32::NAN, f32::NEG_INFINITY);
        p.set_native_kbd_track(f32::INFINITY);

        assert!(p.native_cutoff_hz().is_finite());
        assert!(p.native_resonance().is_finite());
        let (fa, fd, fs, fr, famt) = p.native_filter_env();
        assert!(fa.is_finite() && fd.is_finite() && fs.is_finite());
        assert!(fr.is_finite() && famt.is_finite());
        for idx in 0..3 {
            let (_, _, detune, level) = p.native_osc(idx);
            assert!(detune.is_finite() && level.is_finite());
        }
        assert!(p.native_noise_level().is_finite());
        assert!(p.native_glide_s().is_finite());
        let (vtc, vta) = p.native_vel_routing();
        assert!(vtc.is_finite() && vta.is_finite());
        assert!(p.native_kbd_track().is_finite());

        // Mirror process_audio's per-block param push, then render.
        let mut synth = NativeSynth::new("minimoog", 48_000.0).unwrap();
        synth.set_cutoff_hz(p.native_cutoff_hz());
        synth.set_resonance(p.native_resonance());
        synth.set_filter_env(fa, fd, fs, fr, famt);
        for idx in 0..3 {
            let (wave, octave, detune, level) = p.native_osc(idx);
            synth.set_osc(idx, wave, octave, detune, level);
        }
        synth.set_noise_level(p.native_noise_level());
        synth.set_glide(p.native_glide_s());
        synth.set_vel_routing(vtc, vta);
        synth.set_kbd_track(p.native_kbd_track());

        synth.handle_event(&MidiEvent::NoteOn {
            channel: 0,
            note: 60,
            velocity: 100,
        });
        let mut samples = vec![0.0f32; 48 * 100 * 2]; // 100 ms stereo
        for chunk in samples.chunks_mut(256) {
            synth.render(chunk);
        }
        assert!(
            samples.iter().all(|s| s.is_finite()),
            "render must stay finite after a NaN poisoning attempt"
        );
        let rms = (samples.iter().map(|s| s * s).sum::<f32>() / samples.len() as f32).sqrt();
        assert!(rms > 0.05, "render should still be audible, rms={rms}");
    }
}
