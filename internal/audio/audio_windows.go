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
// -static-libgcc/-static-libstdc++ keep libgcc and libstdc++ off the dependency
// list: rtmidi is C++, so a plain link would otherwise need libstdc++-6.dll on
// the target machine. The result depends only on the Windows system DLLs.
//
// The `asio` cargo feature needs no extra flags here — asio-sys fetches the SDK
// and compiles it itself. A binary built with it links the GPL-3.0 ASIO SDK and
// is distributed under GPL-3.0; see README.md § License.

// #cgo LDFLAGS: -L${SRCDIR}/../../target/x86_64-pc-windows-gnu/release -lpolyclav_audio_core
// #cgo LDFLAGS: -lws2_32 -luserenv -ladvapi32 -lbcrypt -lntdll
// #cgo LDFLAGS: -lole32 -loleaut32 -luuid -lwinmm -lksuser
// #cgo LDFLAGS: -static-libgcc -static-libstdc++
import "C"
