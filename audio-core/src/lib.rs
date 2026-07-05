use std::ffi::CStr;
use std::fs::File;
use std::os::raw::c_char;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU32, AtomicU64, Ordering};
use std::sync::mpsc;
use std::sync::{Arc, Mutex, OnceLock};
use std::thread;
use std::time::Duration;

use pipewire as pw;
use pw::context::ContextRc;
use pw::main_loop::MainLoopRc;
use pw::properties::properties;
use pw::spa::param::audio::{AudioFormat, AudioInfoRaw};
use pw::spa::pod;
use pw::spa::pod::serialize::PodSerializer;
use pw::spa::sys;
use pw::spa::utils::Direction;
use pw::stream::{StreamBox, StreamFlags};

use crossbeam_queue::ArrayQueue;
use oxisynth::{
    MidiEvent as OxiMidiEvent, SoundFont as OxiSoundFont, Synth as OxiSynth, SynthDescriptor,
};

use crate::dsp::{Compressor, Limiter, MasteringCompressor, Reverb};
use crate::plugin_clap::ClapInstance;
use crate::plugin_lv2::LvInstance;
use crate::synth::NativeSynth;

mod dsp;
mod plugin_clap;
mod plugin_lv2;
mod sfizz;
mod sfizz_sys;
mod synth;

const SAMPLE_RATE: f32 = 48000.0;
const MAX_QUANTUM: usize = 8192;

enum SynthBackend {
    Oxi(Box<OxiSynth>),
    Sfizz(sfizz::Sfizz),
    Lv2(Box<LvInstance>),
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

    fn load_lv2(uri: &str) -> Result<Self, String> {
        let inst = LvInstance::load(uri, f64::from(SAMPLE_RATE), MAX_QUANTUM)?;
        eprintln!("audio-core: LV2 plugin loaded ({uri})");
        Ok(SynthBackend::Lv2(Box::new(inst)))
    }

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
            SynthBackend::Lv2(_) => "lv2",
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
    /// Native synth filter cutoff in Hz, written by Go on knob 4 turns.
    /// The audio thread reads this once per block and pushes it to the
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
            native_cutoff_hz: AtomicU32::new(2_000.0_f32.to_bits()),
            native_resonance: AtomicU32::new(0.3_f32.to_bits()),
            native_filter_env_attack_s: AtomicU32::new(0.005_f32.to_bits()),
            native_filter_env_decay_s: AtomicU32::new(0.6_f32.to_bits()),
            native_filter_env_sustain: AtomicU32::new(0.4_f32.to_bits()),
            native_filter_env_release_s: AtomicU32::new(0.6_f32.to_bits()),
            native_filter_env_amount: AtomicU32::new(0.0_f32.to_bits()),
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
}

static DSP_PARAMS: OnceLock<Arc<DspParams>> = OnceLock::new();
fn dsp_params() -> &'static Arc<DspParams> {
    DSP_PARAMS.get_or_init(|| Arc::new(DspParams::new()))
}

struct State {
    thread: thread::JoinHandle<()>,
    quit_flag: Arc<AtomicBool>,
}

struct UserData {
    sine_phase: f32,
    callback_count: u32,
    last_frames: usize,
    synth: Option<SynthBackend>,
    midi_queue: Arc<ArrayQueue<MidiEvent>>,
    reload_queue: Arc<ArrayQueue<(u64, SynthBackend)>>,
    dsp_params: Arc<DspParams>,
    compressor: Compressor,
    reverb: Reverb,
    mastering: MasteringCompressor,
    limiter: Limiter,
    last_comp_amount: f32,
    last_reverb_mix: f32,
    last_mastering_amount: f32,
    last_limiter_ceiling_db: f32,
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
            if let Err(e) = run_audio(quit_for_thread, &ready_tx) {
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

/// Set the native synth's filter cutoff in Hz. Phase 1 hardcoded
/// mapping: knob 4 → cutoff. The audio thread reads the atomic each
/// block and applies it to the active `SynthBackend::Native`; harmless
/// when other backends are active. Clamped to [20, 20000] in Rust.
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

    let props = properties! {
        *pw::keys::MEDIA_TYPE => "Audio",
        *pw::keys::MEDIA_CATEGORY => "Playback",
        *pw::keys::MEDIA_ROLE => "Music",
        *pw::keys::NODE_NAME => "polyclav",
        *pw::keys::NODE_DESCRIPTION => "polyclav audio core",
        *pw::keys::APP_NAME => "polyclav",
        *pw::keys::NODE_LATENCY => "128/48000",
    };

    let stream =
        StreamBox::new(&core, "polyclav-output", props).map_err(|e| format!("Stream::new: {e}"))?;

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
    let mut compressor = Compressor::new();
    let mut reverb = Reverb::new();
    let mut mastering = MasteringCompressor::new(SAMPLE_RATE);
    let mut limiter = Limiter::new(SAMPLE_RATE);
    compressor.set_amount(initial_comp);
    reverb.set_mix(initial_mix);
    mastering.set_amount(initial_mastering);
    limiter.set_ceiling_db(initial_ceiling_db);

    let user_data = UserData {
        sine_phase: 0.0,
        callback_count: 0,
        last_frames: 0,
        synth,
        midi_queue: Arc::clone(midi_queue()),
        reload_queue: Arc::clone(synth_reload_queue()),
        dsp_params: params,
        compressor,
        reverb,
        mastering,
        limiter,
        last_comp_amount: initial_comp,
        last_reverb_mix: initial_mix,
        last_mastering_amount: initial_mastering,
        last_limiter_ceiling_db: initial_ceiling_db,
    };

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

fn process_audio(stream: &pw::stream::Stream, user_data: &mut UserData) {
    // 1. Hot-swap soundfont if a freshly loaded backend is pending.
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

    // 2. Drain MIDI events into the current synth.
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
            Some(SynthBackend::Lv2(ref mut s)) => s.push_midi(&event),
            Some(SynthBackend::Clap(ref mut s)) => s.push_midi(&event),
            Some(SynthBackend::Native(ref mut s)) => s.handle_event(&event),
            None => {}
        }
    }

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

        // Phase 1 native-synth knob override: push the latest cutoff +
        // resonance atomics into the active native synth before
        // rendering. This is a no-op for other backends (the match arm
        // doesn't fire).
        if let Some(SynthBackend::Native(ref mut s)) = user_data.synth {
            s.set_cutoff_hz(user_data.dsp_params.native_cutoff_hz());
            s.set_resonance(user_data.dsp_params.native_resonance());
            let (fa, fd, fs, fr, famt) = user_data.dsp_params.native_filter_env();
            s.set_filter_env(fa, fd, fs, fr, famt);
            for idx in 0..3 {
                let (wave, octave, detune_cents, level) = user_data.dsp_params.native_osc(idx);
                s.set_osc(idx, wave, octave, detune_cents, level);
            }
            s.set_noise_level(user_data.dsp_params.native_noise_level());
            s.set_glide(user_data.dsp_params.native_glide_s());
        }

        match user_data.synth {
            Some(SynthBackend::Oxi(ref mut s)) => s.write(&mut *samples),
            Some(SynthBackend::Sfizz(ref mut s)) => s.render(samples),
            Some(SynthBackend::Lv2(ref mut s)) => s.render(samples),
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
        p.set_native_cutoff_hz(1_234.0);
        p.set_native_resonance(0.5);
        p.set_native_filter_env(0.01, 0.2, 0.5, 0.3, 0.25);
        p.set_native_osc(1, 1, 1, 12.5, 0.25);
        p.set_native_noise_level(0.5);
        p.set_native_glide_s(0.25);

        for bad in NON_FINITE {
            p.set_master_volume(bad);
            p.set_comp_amount(bad);
            p.set_reverb_mix(bad);
            p.set_patch_gain(bad);
            p.set_mastering_amount(bad);
            p.set_limiter_ceiling_db(bad);
            p.set_native_cutoff_hz(bad);
            p.set_native_resonance(bad);
            p.set_native_filter_env(bad, bad, bad, bad, bad);
            p.set_native_osc(1, 1, 1, bad, bad);
            p.set_native_noise_level(bad);
            p.set_native_glide_s(bad);

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
    }

    /// The C ABI setters route through the same non-finite rejection.
    /// This test only touches the six generic DSP globals (volume /
    /// compressor / reverb / patch gain / mastering / limiter) — the
    /// native_* globals are owned by the other FFI tests, and tests run
    /// in parallel in one process.
    #[test]
    fn ffi_setters_reject_non_finite_values() {
        polyclav_dsp_set_master_volume(0.5);
        polyclav_dsp_set_compressor(0.25);
        polyclav_dsp_set_reverb(0.75);
        polyclav_dsp_set_patch_gain(2.0);
        polyclav_dsp_set_mastering_compressor(0.5);
        polyclav_dsp_set_limiter_ceiling_db(-6.0);

        for bad in NON_FINITE {
            polyclav_dsp_set_master_volume(bad);
            polyclav_dsp_set_compressor(bad);
            polyclav_dsp_set_reverb(bad);
            polyclav_dsp_set_patch_gain(bad);
            polyclav_dsp_set_mastering_compressor(bad);
            polyclav_dsp_set_limiter_ceiling_db(bad);

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
        }

        // Restore the documented boot values.
        polyclav_dsp_set_master_volume(1.0);
        polyclav_dsp_set_compressor(0.0);
        polyclav_dsp_set_reverb(0.0);
        polyclav_dsp_set_patch_gain(1.0);
        polyclav_dsp_set_mastering_compressor(0.0);
        polyclav_dsp_set_limiter_ceiling_db(-0.3);
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
