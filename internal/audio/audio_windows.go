//go:build windows

package audio

// Windows link flags: cpal/WASAPI (windows-rs) plus the Rust staticlib.
//
// A Rust staticlib does NOT propagate its transitive cargo:rustc-link-lib
// directives to the cgo link, so we restate the system libraries here:
//   -lpolyclav_audio_core : the Rust staticlib itself
//   ws2_32/userenv/advapi32/bcrypt/ntdll : Rust std (net, env, ACL, getrandom, NT)
//   ole32/oleaut32/uuid   : COM, used by cpal's WASAPI host
//   winmm/ksuser          : rtmidi's WinMM backend (see its own #cgo windows line)
//
// The -L path is the TARGET-SPECIFIC release dir on purpose. Go's cgo on Windows
// needs gcc, which means the Rust side must be built for
// `x86_64-pc-windows-gnu` — and `cargo build --target x86_64-pc-windows-gnu`
// writes there on a native Windows box and on a Linux cross-build alike. Pointing
// at target/release instead would pick up a *Linux* staticlib when both are
// present, which fails at link time with a confusing error.
//
// -static keeps every MinGW runtime off the dependency list. The narrow
// -static-libgcc/-static-libstdc++ pair is NOT enough, and believing it was is
// how v0.3.0 shipped with no Windows asset: it leaves libwinpthread-1.dll
// dynamic, because libstdc++ (pulled in by rtmidi's C++) links the C++
// runtime's thread support separately, and MSYS2's MinGW uses the winpthreads
// thread model. The release job's portability check caught the import and
// failed; ci-windows.yml had only asserted libgcc/libstdc++, so CI was green.
//
// The broad -static works on both toolchains: MSYS2 statically links its
// winpthreads, and nix's MinGW (thread model "mcf") statically links
// mcfgthread, which only needs to be on the link path — see the -L in
// docs/WINDOWS_PORT.md §7. The result depends only on Windows system DLLs.
//
// The `asio` cargo feature needs no extra flags here — asio-sys fetches the SDK
// and compiles it itself. A binary built with it links the GPL-3.0 ASIO SDK and
// is distributed under GPL-3.0; see README.md § License.

// #cgo LDFLAGS: -L${SRCDIR}/../../target/x86_64-pc-windows-gnu/release -lpolyclav_audio_core
// #cgo LDFLAGS: -lws2_32 -luserenv -ladvapi32 -lbcrypt -lntdll
// #cgo LDFLAGS: -lole32 -loleaut32 -luuid -lwinmm -lksuser
// #cgo LDFLAGS: -static
import "C"
