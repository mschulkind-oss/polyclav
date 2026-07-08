//! Runtime (dlopen) binding to the small subset of libsfizz we use.
//!
//! sfizz is an OPTIONAL backend: the binary does NOT link it. We `dlopen`
//! the platform's libsfizz on first use; if it is absent the SFZ backend
//! is simply unavailable and `.sfz` patches degrade gracefully —
//! oxisynth (SF2/SF3), the native synth, and LV2/CLAP plugins are
//! unaffected. See `sfizz.h` (sfztools/sfizz) for the full C API.
//!
//! On macOS, `polyclav bootstrap` installs polyclav's own prebuilt arm64
//! build (see `internal/bootstrap/sfizz.go`) to a fixed path under the
//! user's data dir, tried first — sfztools' own official macOS release
//! (`sfizz-<ver>-macos.tar.gz`) is x86_64-only (their CI still targets
//! macos-11, from before GitHub had Apple Silicon runners), and a native
//! arm64 process cannot dlopen a foreign-architecture dylib, so that
//! upstream tarball is useless on the Apple Silicon Macs most people
//! actually have. If someone manually installs a working libsfizz
//! themselves, the bare-name fallback below still finds it: extracting
//! sfztools' tarball to `/usr/local` (on dyld's default fallback search
//! path) makes a bare `dlopen("libsfizz.dylib")` succeed with no extra
//! setup — this just isn't architecture-correct on its own for most
//! machines today. (Apple Silicon Homebrew's `/opt/homebrew/lib` is NOT
//! on that fallback path; if sfizz ever gets a Homebrew formula, installs
//! there would need `DYLD_LIBRARY_PATH` set to be found.)

use std::os::raw::{c_char, c_int};
use std::sync::OnceLock;

use libloading::Library;

#[allow(non_camel_case_types)]
pub enum sfizz_synth_t {}

type CreateSynth = unsafe extern "C" fn() -> *mut sfizz_synth_t;
type Free = unsafe extern "C" fn(*mut sfizz_synth_t);
type LoadFile = unsafe extern "C" fn(*mut sfizz_synth_t, *const c_char) -> bool;
type SetSampleRate = unsafe extern "C" fn(*mut sfizz_synth_t, f32);
type SetSamplesPerBlock = unsafe extern "C" fn(*mut sfizz_synth_t, c_int);
type SendNote = unsafe extern "C" fn(*mut sfizz_synth_t, c_int, c_int, c_int);
type SendCc = unsafe extern "C" fn(*mut sfizz_synth_t, c_int, c_int, c_int);
type SendPitch = unsafe extern "C" fn(*mut sfizz_synth_t, c_int, c_int);
type RenderBlock = unsafe extern "C" fn(*mut sfizz_synth_t, *mut *mut f32, c_int, c_int);

/// Resolved libsfizz entry points. The `Library` is kept mapped for the
/// lifetime of the process so the function pointers stay valid.
pub struct SfizzApi {
    _lib: Library,
    pub create_synth: CreateSynth,
    pub free: Free,
    pub load_file: LoadFile,
    pub set_sample_rate: SetSampleRate,
    pub set_samples_per_block: SetSamplesPerBlock,
    pub send_note_on: SendNote,
    pub send_note_off: SendNote,
    pub send_cc: SendCc,
    pub send_pitch_wheel: SendPitch,
    pub render_block: RenderBlock,
}

// The function pointers are plain code addresses into the mapped library,
// which lives for the whole process. Safe to share across threads.
unsafe impl Send for SfizzApi {}
unsafe impl Sync for SfizzApi {}

static API: OnceLock<Option<SfizzApi>> = OnceLock::new();

// Versioned SONAME first (what a real install provides), then the
// unversioned dev symlink. Linux resolution honours the binary's
// RUNPATH (dev/nix builds) and the system ldconfig cache (portable
// builds); macOS resolution goes through dyld's default fallback search
// path (see the module doc comment for exactly how a bare "libsfizz.dylib"
// gets found with no extra setup) -- but find_library() below tries
// polyclav's own bootstrap-installed build first on macOS.
#[cfg(target_os = "linux")]
const LIB_NAMES: &[&str] = &["libsfizz.so.1", "libsfizz.so"];
#[cfg(target_os = "macos")]
const LIB_NAMES: &[&str] = &["libsfizz.1.dylib", "libsfizz.dylib"];

/// polyclav bootstrap's own prebuilt arm64 libsfizz.dylib
/// (`internal/bootstrap/sfizz.go`) at its fixed install path. See the
/// module doc comment for why this exists instead of relying on any
/// system-wide location.
#[cfg(target_os = "macos")]
fn load_bootstrapped_lib() -> Option<Library> {
    let home = std::env::var_os("HOME")?;
    let path = std::path::Path::new(&home).join(".local/share/polyclav/lib/libsfizz.dylib");
    unsafe { Library::new(&path) }.ok()
}

/// Locates and dlopen's libsfizz: on macOS, polyclav's own
/// bootstrap-installed build first, then the bare system-search names on
/// either platform.
fn find_library() -> Option<Library> {
    #[cfg(target_os = "macos")]
    {
        if let Some(lib) = load_bootstrapped_lib() {
            return Some(lib);
        }
    }
    LIB_NAMES
        .iter()
        .find_map(|name| unsafe { Library::new(*name) }.ok())
}

fn load() -> Option<SfizzApi> {
    let lib = find_library()?;
    unsafe {
        macro_rules! sym {
            ($t:ty, $name:literal) => {
                *lib.get::<$t>($name).ok()?
            };
        }
        Some(SfizzApi {
            create_synth: sym!(CreateSynth, b"sfizz_create_synth\0"),
            free: sym!(Free, b"sfizz_free\0"),
            load_file: sym!(LoadFile, b"sfizz_load_file\0"),
            set_sample_rate: sym!(SetSampleRate, b"sfizz_set_sample_rate\0"),
            set_samples_per_block: sym!(SetSamplesPerBlock, b"sfizz_set_samples_per_block\0"),
            send_note_on: sym!(SendNote, b"sfizz_send_note_on\0"),
            send_note_off: sym!(SendNote, b"sfizz_send_note_off\0"),
            send_cc: sym!(SendCc, b"sfizz_send_cc\0"),
            send_pitch_wheel: sym!(SendPitch, b"sfizz_send_pitch_wheel\0"),
            render_block: sym!(RenderBlock, b"sfizz_render_block\0"),
            _lib: lib,
        })
    }
}

/// The resolved libsfizz API, or `None` if libsfizz could not be loaded.
/// Loaded once on first call and cached for the process lifetime.
pub fn api() -> Option<&'static SfizzApi> {
    API.get_or_init(load).as_ref()
}

/// Whether libsfizz is available (i.e. SFZ playback is possible).
pub fn available() -> bool {
    api().is_some()
}
