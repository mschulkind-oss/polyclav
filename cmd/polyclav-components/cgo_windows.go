//go:build windows

package main

// Windows link flags for polyclav-components.
//
// This binary links rtmidi (C++, for MIDI) but NOT internal/audio, and cgo
// LDFLAGS are PER-PACKAGE. So the static link flag in
// internal/audio/audio_windows.go does not reach it. Without a flag here the
// binary imports libstdc++-6.dll and libgcc_s_seh-1.dll, and dies on a clean
// machine with a missing-DLL dialog. CI caught exactly that the first time this
// binary was added to the Windows build.
//
// The flag is the same choice the daemon makes, for the same reason: the narrow
// -static-libgcc / -static-libstdc++ pair leaves libwinpthread-1.dll dynamic
// under the winpthreads thread model MSYS2 uses. Unlike the daemon there is no
// Rust staticlib here, so the mcfgthread -L flag that nix cross-builds need does
// not apply.
//
// The cgo preamble below (the comment block immediately above import "C") holds
// only the directive; the explanation lives up here, detached by a blank line.

// #cgo LDFLAGS: -static
import "C"
