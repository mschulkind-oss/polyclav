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
        }
    }

    fn store_clamped(slot: &AtomicU32, v: f32, lo: f32, hi: f32) {
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

        // Phase 1 native-synth knob-4 override: push the latest cutoff
        // atomic into the active native synth before rendering. This is
        // a no-op for other backends (the match arm doesn't fire).
        if let Some(SynthBackend::Native(ref mut s)) = user_data.synth {
            s.set_cutoff_hz(user_data.dsp_params.native_cutoff_hz());
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
