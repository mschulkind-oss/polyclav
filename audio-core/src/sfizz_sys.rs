//! Runtime (dlopen) binding to the small subset of libsfizz we use.
//!
//! sfizz is an OPTIONAL backend: the binary does NOT link it. We `dlopen`
//! the platform's libsfizz on first use; if it is absent the SFZ backend
//! is simply unavailable and `.sfz` patches degrade gracefully —
//! oxisynth (SF2/SF3), the native synth, and LV2/CLAP plugins are
//! unaffected. See `sfizz.h` (sfztools/sfizz) for the full C API.
//!
//! On both macOS and Linux, `polyclav bootstrap` installs polyclav's own
//! prebuilt libsfizz (see `internal/bootstrap/sfizz.go`) to the polyclav data
//! dir's `lib/` (see `bootstrap_lib_dir` below), tried first:
//!
//! - On macOS, sfztools' own official release (`sfizz-<ver>-macos.tar.gz`)
//!   is x86_64-only (their CI still targets macos-11, from before GitHub
//!   had Apple Silicon runners), and a native arm64 process cannot dlopen
//!   a foreign-architecture dylib, so that upstream tarball is useless on
//!   the Apple Silicon Macs most people actually have. If someone manually
//!   installs a working libsfizz themselves, the bare-name fallback below
//!   still finds it: extracting sfztools' tarball to `/usr/local` (on
//!   dyld's default fallback search path) makes a bare
//!   `dlopen("libsfizz.dylib")` succeed with no extra setup — this just
//!   isn't architecture-correct on its own for most machines today.
//!   (Apple Silicon Homebrew's `/opt/homebrew/lib` is NOT on that fallback
//!   path; if sfizz ever gets a Homebrew formula, installs there would
//!   need `DYLD_LIBRARY_PATH` set to be found.)
//! - On Linux, sfztools publishes no binary at all in their GitHub
//!   releases (only macOS and Windows bundles), so there is nothing to
//!   fall back to at all beyond a distro package (where one exists — see
//!   `docs/INSTALL.md`) or building from source. The bootstrap-installed
//!   path is the only option that works the same way on every distro.

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
// polyclav's own bootstrap-installed build first on both platforms.
#[cfg(target_os = "linux")]
const LIB_NAMES: &[&str] = &["libsfizz.so.1", "libsfizz.so"];
#[cfg(target_os = "macos")]
const LIB_NAMES: &[&str] = &["libsfizz.1.dylib", "libsfizz.dylib"];
// Windows has no soname convention, so these are just the candidate filenames:
// `LoadLibrary` searches the DLL's own directory and the PATH. `sfizz.dll` is
// what build-sfizz-windows.yml publishes (MSVC names the target without a `lib`
// prefix); `libsfizz.dll` is accepted too so a MinGW-built copy also loads.
#[cfg(target_os = "windows")]
const LIB_NAMES: &[&str] = &["sfizz.dll", "libsfizz.dll"];

// Filename `internal/bootstrap/sfizz.go` installs for this platform, under
// `bootstrap_lib_dir()`.
#[cfg(target_os = "linux")]
const BOOTSTRAPPED_LIB_NAME: &str = "libsfizz.so";
#[cfg(target_os = "macos")]
const BOOTSTRAPPED_LIB_NAME: &str = "libsfizz.dylib";
#[cfg(target_os = "windows")]
const BOOTSTRAPPED_LIB_NAME: &str = "sfizz.dll";

/// The directory `polyclav bootstrap` puts the library in: the polyclav data
/// dir plus `lib` — the Rust mirror of Go's `config.DataDir()` joined with
/// `"lib"` (`cmd/polyclav`'s `defaultSfizzLibDest`, which is also what
/// `internal/bootstrap/sfizz.go` is handed as `Dest`).
///
/// Linux and macOS use `$HOME/.local/share/polyclav/lib`, unchanged from when
/// this was a single hardcoded constant; Windows uses
/// `%LOCALAPPDATA%\polyclav\lib`, matching the platform data dir Go resolves
/// there. The two sides must agree on every platform or an installed
/// `sfizz.dll` is silently never found.
#[cfg(any(target_os = "macos", target_os = "linux", target_os = "windows"))]
fn bootstrap_lib_dir() -> Option<std::path::PathBuf> {
    #[cfg(target_os = "windows")]
    let base = std::path::PathBuf::from(std::env::var_os("LOCALAPPDATA")?).join("polyclav");
    #[cfg(not(target_os = "windows"))]
    let base = std::path::PathBuf::from(std::env::var_os("HOME")?).join(".local/share/polyclav");
    Some(base.join("lib"))
}

/// polyclav bootstrap's own prebuilt libsfizz
/// (`internal/bootstrap/sfizz.go`) at its fixed install path. See the
/// module doc comment for why this exists instead of relying on any
/// system-wide location.
#[cfg(any(target_os = "macos", target_os = "linux", target_os = "windows"))]
fn load_bootstrapped_lib() -> Option<Library> {
    let path = bootstrap_lib_dir()?.join(BOOTSTRAPPED_LIB_NAME);
    unsafe { Library::new(&path) }.ok()
}

/// Locates and dlopen's libsfizz: polyclav's own bootstrap-installed
/// build first, then the bare system-search names.
fn find_library() -> Option<Library> {
    #[cfg(any(target_os = "macos", target_os = "linux", target_os = "windows"))]
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

#[cfg(all(
    test,
    any(target_os = "macos", target_os = "linux", target_os = "windows")
))]
mod tests {
    use super::*;

    // Only the host's own branch is compiled, so this pins the POSIX spelling
    // on POSIX hosts and the %LOCALAPPDATA% spelling on Windows ones — the
    // Rust half of the Go table test for config.DataDir().
    #[test]
    fn bootstrap_lib_dir_matches_the_go_data_dir() {
        let dir = bootstrap_lib_dir().expect("HOME / LOCALAPPDATA should be set in tests");
        #[cfg(target_os = "windows")]
        let want = std::path::Path::new(&std::env::var_os("LOCALAPPDATA").expect("LOCALAPPDATA"))
            .join("polyclav")
            .join("lib");
        #[cfg(not(target_os = "windows"))]
        let want = std::path::Path::new(&std::env::var_os("HOME").expect("HOME"))
            .join(".local/share/polyclav")
            .join("lib");
        assert_eq!(dir, want);
        assert_eq!(dir.file_name().and_then(|n| n.to_str()), Some("lib"));
    }

    #[test]
    fn bootstrapped_lib_path_is_absolute_and_named_for_this_platform() {
        let path = bootstrap_lib_dir()
            .expect("HOME / LOCALAPPDATA should be set in tests")
            .join(BOOTSTRAPPED_LIB_NAME);
        assert!(path.is_absolute(), "{} is not absolute", path.display());
        assert_eq!(
            path.file_name().and_then(|n| n.to_str()),
            Some(BOOTSTRAPPED_LIB_NAME)
        );
    }
}
