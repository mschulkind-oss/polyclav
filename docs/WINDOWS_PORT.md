# Running polyclav on Windows

> Status: **investigation, 2026-10-27. Nothing is ported yet.** This doc records
> the coupling map against the post-macOS-port codebase, the four deltas that are
> genuinely different from macOS, a phased plan, and the Windows-VM strategy that
> makes the port testable from inside the Linux dev jail.
>
> The VM path is verified as far as this jail allows without `/dev/kvm`: dockur's
> Windows 11 LTSC guest boots here under software emulation (§6). What blocks the
> *fast* version is one yolo setting, not a design question.
>
> Read [`MACOS_PORT.md`](./MACOS_PORT.md) first if you have not: that port did the
> structural work (the `render_block`/`drain_midi`/`swap_pending_backend` seam, the
> per-OS Go cgo split), and this doc builds directly on it.

## 1. TL;DR / verdict

**The port is small. The latency is the hard part.** Those are separate questions
and conflating them is how this project would waste a week.

Portability is largely already done, because macOS forced the codebase into a
two-arm platform seam. Windows is a *third arm of an existing seam*, not a new
architecture:

- `cargo check --target x86_64-pc-windows-gnu` on today's tree fails with
  **exactly three errors**, all the same shape — a `#[cfg]` pair with no Windows
  arm: `sfizz_sys.rs:114` (`LIB_NAMES`), `lib.rs:1600` (the audio-backend `setup`
  binding), `lib.rs:3376` (`load_synth_by_type`). Nothing structural.
- The Go side needs **two new files**: `internal/audio/audio_windows.go` (today
  `internal/audio` has only `_linux.go` and `_darwin.go`, so `GOOS=windows` reports
  *"build constraints exclude all Go files"*) and `cmd/polyclav/singleton_windows.go`
  (`main.go:109` calls `acquireDefaultDaemonLock`, which lives in the
  `//go:build unix` `singleton_unix.go`).
- MIDI already works. rtmidi's vendored cgo has a Windows arm —
  `#cgo windows CXXFLAGS: -D__WINDOWS_MM__` and
  `LDFLAGS: -luuid -lksuser -lwinmm -lole32` — so `gomidi` → WinMM needs no change.
- Everything else (DSP, synth, OSC, config, state, velocity, the embedded web
  export) is pure Go/Rust and platform-neutral.

**The real problem is the audio latency model, and it is not a porting problem —
it is a design decision.** polyclav asks for a 128-frame / 48 kHz period
(~2.67 ms). On Windows through `cpal` you cannot get that: cpal's WASAPI backend is
**shared-mode only**, and for ordinary (non-offloaded) endpoints it reports the
device period as *both* the minimum and maximum buffer size — typically 10 ms,
i.e. 480 frames at 48 kHz. The existing macOS backend's `clamp_frames` would
therefore silently turn 128 into 480. Getting 2.67 ms needs either `IAudioClient3`
low-latency shared mode (driver-dependent, frequently still 10 ms) or a
hand-written WASAPI exclusive-mode backend that cpal does not provide.

So: **a Windows build is a few days of bounded work; a Windows build that feels
like the Linux one under the fingers is a second, harder job that cannot be
settled without real hardware.** Don't let the second block the first.

## 2. What is coupled to a platform now

The macOS port already moved every OS-specific thing behind a seam. What remains
is a per-OS table with two filled rows and a third to fill.

| Coupling point | Where | Windows verdict |
|---|---|---|
| **Audio output** | `lib.rs` dispatch at `1594-1600`; `run_audio`/`process_audio` (Linux, `#[cfg(target_os = "linux")]`), `backend_macos.rs` (macOS, cpal) | **New backend file.** cpal/WASAPI works and is the cheap path; latency is the caveat (§3.1). |
| **Latency knob** | `LATENCY_FRAMES` in `lib.rs`, already shared and clamped to `[16,8192]` by `polyclav_audio_set_latency_frames` | **Reusable unchanged.** The *number* survives; what a backend can honour does not (§3.1). |
| **`load_synth_by_type`** | `lib.rs:3237` (linux), `lib.rs:3258` (macos) | **Third arm.** Same body as the macOS one (soundfont + native only). |
| **sfizz dlopen** | `sfizz_sys.rs:80-90` — `LIB_NAMES`, `BOOTSTRAPPED_LIB_NAME`; bootstrap path gated to `any(macos, linux)` | **Third arm** — `sfizz.dll`, and the bootstrap dir under `%LOCALAPPDATA%`. Needs polyclav's own DLL build (§3.4). |
| **Go cgo preamble** | `internal/audio/audio_linux.go`, `audio_darwin.go` | **New `audio_windows.go`.** MinGW system-lib list, static link (§3.3). |
| **Singleton lock** | `cmd/polyclav/singleton_unix.go` (`//go:build unix`, `syscall.Flock`) | **New `singleton_windows.go`** — `LockFileEx` or a named `CreateMutex`. |
| **MIDI input** | `internal/midi/midi.go`, `internal/launchkey/driver/driver.go` → `gomidi` → vendored rtmidi | **Already portable.** `#cgo windows` → WinMM. Port-name heuristics (`PickPortName` substrings tuned to ALSA names) will need re-checking against WinMM names. |
| **OSC (XR18)** | `internal/osc/client.go` — pure-Go UDP | **Portable, unchanged.** |
| **Web UI** | `internal/web/static.go` embeds `all:static/app` (a Next.js export) | **Portable.** The export is compiled into the binary; Windows needs no Node at runtime. |
| **Signal handling** | `main.go:154,235,1717` — `signal.NotifyContext(…, syscall.SIGTERM)` | **Compiles unchanged.** Go defines `SIGTERM` on Windows for compatibility; it is simply never delivered. Ctrl-C (`os.Interrupt`) is the real path. |
| **Task runner** | `Justfile` recipes are POSIX shell | **Dev-loop only.** Run `cargo`/`go` directly, or use Git Bash / MSYS2. Not a product concern. |
| **LV2 hosting** | `plugin_lv2.rs`, `livi`/`lilv`, gated `linux` | **Drop, as macOS did.** lilv on Windows means MSYS2, and LV2 plugins are rare there (§3.4). |
| **CLAP hosting** | `plugin_clap.rs`, `clack-host`, gated `linux` | **Re-enable for Windows.** Pure-Rust host; Windows CLAP plugins are DLLs; `libloading` works (§3.4). |

The FFI contract (`audio-core/include/polyclav_audio.h`, 33 `extern "C"`
functions, plus the offline-render entry points) **does not change at all**. None
of it mentions a platform; it pushes onto the same lock-free queues the render
thread drains.

## 3. The four deltas that are genuinely different from macOS

### 3.1 Audio output: WASAPI, and why 2.67 ms is the whole argument

This is the one place where "it builds" and "it works" diverge.

**What cpal gives you on Windows.** `cpal`'s WASAPI backend creates every stream
in **shared mode** — its own source says so: *"We always create voices in shared
mode, therefore all samples go through an audio processor to mix them together."*
Output streams additionally set `AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM`, so
`Initialize` accepts *any* format and Windows resamples it to the endpoint mix
format. Two consequences:

1. **Good:** polyclav's hardcoded 48 kHz stereo f32 request is always accepted.
   The DSP's 48 k assumption (`SAMPLE_RATE` in `lib.rs`, and the 48 k-tuned
   `dsp/compressor.rs` / `dsp/reverb.rs`) stays correct — Windows resamples at the
   edge, not inside the engine.
2. **Bad:** the Windows resampler is in the signal path whenever the endpoint's mix
   format is not 48 kHz, and shared mode means the audio engine is mixing you with
   everything else in the session.

**What buffer size you actually get.** For software audio stacks (i.e. almost
every endpoint — hardware-offloaded audio is the exception), cpal does not report a
buffer *range*; it substitutes the device period for both ends:

```rust
SupportedBufferSize::Unknown => device_period_hns
    .map(|p| SupportedBufferSize::Range { min: frames, max: frames })
```

The device period from `GetDevicePeriod` is 10 ms on essentially all consumer
hardware. At 48 kHz that is **480 frames**, so `clamp_frames(128, 480, 480)`
returns 480. polyclav's 2.67 ms target becomes ~10 ms with no error and no
warning. This is the single most important thing to know before writing a line of
Windows code: **the existing macOS backend, copied to Windows, will run — and it
will quietly be four times looser than on Linux.**

**The three real options**, in increasing cost:

| Option | Period | Cost | Notes |
|---|---|---|---|
| **A. cpal shared mode as-is** | ~10 ms (device period) | ~0 (backend is `backend_macos.rs` with a different `cfg`) | Ship-quality for a practice instrument? 10 ms is roughly a well-set-up MIDI keyboard + soft synth on Windows. Bit-exactness is lost to the OS resampler only if the endpoint is not 48 k. |
| **B. `IAudioClient3` low-latency shared mode** | down to the driver's reported minimum | Moderate — cpal does not expose it; needs a `windows`-crate backend or a cpal patch | Windows 10+. `GetSharedModeEnginePeriod` reports min/ fundamental/max; `InitializeSharedAudioStream` takes a period that is a multiple of the fundamental. Microsoft documents "a small buffer (less than 10 ms)"; **measured reality is pessimistic** — a survey of real endpoints found 10 ms minimum for NVIDIA HDA and Thesycon (SSL 2) drivers, 7 ms for one `usbaudio2` device. Worth measuring against the actual interface before designing for it. |
| **C. WASAPI exclusive mode** | ~2.67 ms achievable | High — ~400–600 lines of `unsafe` `IAudioClient`/`IAudioRenderClient` + event-driven callback | cpal has **no** exclusive mode (the "Added WASAPI exclusive host" PR was closed, not merged). Exclusive mode takes the device away from everything else, requires the endpoint to accept the exact format, and changes the failure modes (device in use → hard error). |

**Recommendation:** ship **A** first, because it is nearly free and it gets a
sounding Windows build in front of a keyboard. Measure the real device period on
the actual hardware (§6), and only then decide whether **B** is enough. Treat **C**
as the "make it feel like Linux" project, and only if A/B measurably fail the
under-the-fingers test.

There is a fourth option nobody should take: lowering `LATENCY_FRAMES` and
believing the number. A backend that cannot honour the request must *report* what
it actually got, or the latency telemetry lies.

### 3.2 Real-time scheduling: MMCSS, not `SCHED_FIFO`

Linux gives polyclav a real-time thread through PipeWire's data-loop thread
priority. macOS gives it CoreAudio's HAL thread. Windows has neither, and the
equivalent is a different mechanism entirely:

- **MMCSS** (Multimedia Class Scheduler Service) — the audio thread calls
  `AvSetMmThreadCharacteristics("Pro Audio", …)` and gets elevated scheduling with
  guaranteed CPU share. This is what every serious Windows audio app does, and
  **cpal does not do it.** cpal 0.18 does raise stream threads (the
  "set realtime priority for stream threads in alsa and wasapi" change), but there
  is a reported bug where the WASAPI path passes `GetCurrentThreadId()` where
  `SetThreadPriority` needs a thread *handle*, so the boost silently does nothing.
  Do not rely on it; own the thread priority in the backend.
- **Power throttling / core parking.** A background-looking process gets its
  threads throttled by the Windows scheduler. The audio thread must opt out
  (`SetThreadPriority(THREAD_PRIORITY_TIME_CRITICAL)` after MMCSS, and the process
  should not be eligible for EcoQoS throttling).
- **No `SCHED_FIFO`, no `chrt`, no `ulimit -r`.** There is nothing to configure
  outside the process — it is all in-process API calls.

This is real work with no Linux/macOS analogue, and it is invisible in CI: a
mis-scheduled thread still produces correct samples, just with occasional
dropouts. It belongs to the hardware bring-up phase.

### 3.3 Toolchain: cgo means MinGW, which dictates the Rust target

This is the constraint most likely to burn a day if discovered late.

- **Go's cgo on Windows uses gcc, not MSVC.** There is no MSVC support for cgo.
  So the host toolchain is **MinGW-w64** (`x86_64-w64-mingw32-gcc`), and cgo
  compiles the C/C++ it is handed with it.
- **Therefore `audio-core` must be built for `x86_64-pc-windows-gnu`**, not
  `x86_64-pc-windows-msvc`. A staticlib built for MSVC is not linkable by MinGW.
  `x86_64-pc-windows-gnu` is a Tier-1 Rust target with host tools, and cpal /
  `windows-rs` build on it fine — but it is *not* the default a `rustup` on
  Windows installs, and a developer who builds the Rust side in Visual Studio will
  produce a `.lib` that Go cannot use. Say so in `docs/INSTALL.md` when the port
  lands.
- **The staticlib swallows its own link directives.** Exactly as on Linux and
  macOS, the Rust staticlib does not re-propagate `cargo:rustc-link-lib` to the
  cgo link, so `audio_windows.go` must restate the system libraries. Expect the
  union of: the Rust std set (`ws2_32`, `userenv`, `advapi32`, `bcrypt`, `ntdll`,
  `ole32`, `oleaut32`, `uuid`) plus what rtmidi's C++ and WinMM need (`winmm`,
  `ksuser`) — the rtmidi preamble already names `-luuid -lksuser -lwinmm -lole32`
  for exactly this reason.
- **Link statically, or ship DLLs.** rtmidi is C++, so a default MinGW link leaves
  `libstdc++-6.dll` / `libgcc_s_seh-1.dll` as runtime dependencies — a `polyclav.exe`
  that fails on a clean machine with a missing-DLL dialog. Use `-static` (or
  `-static-libgcc -static-libstdc++`) so the release is a single self-contained
  `.exe`, matching how the Linux and macOS releases are shipped.
- **MSVC is still fine for the Rust-only test suite**, which needs no cgo. But
  never mix the two in one build tree.

### 3.4 Plugin hosting and sfizz

- **LV2: drop it on Windows**, for the same reason macOS dropped it. Hosting would
  compile (lilv is in MSYS2), but LV2 plugin availability on Windows is close to
  zero, and it drags in a hand-built lilv/serd/sord/sratom/zix chain. Not worth it.
- **CLAP: this is the Windows plugin story.** `clack-host` is pure Rust and
  cross-platform, Windows CLAP plugins are ordinary DLLs loaded by `libloading`,
  and the catalog (u-he, Surge XT, Dexed, Airwindows) is the same one polyclav
  already targets. The `plugin_clap.rs` module is gated `linux` today; moving it to
  `any(linux, windows)` is the single highest-value plugin change for Windows.
- **sfizz needs polyclav's own build, again.** `sfizz_sys.rs` `dlopen`s a bare
  library name; sfizz's official Windows download is an *installer* shipping a
  VST3 and a standalone, not a plain `sfizz.dll` to `dlopen`. So this repeats the
  macOS arm64 story: a `build-sfizz-windows.yml` workflow builds polyclav's own
  DLL from sfizz's vendored-submodule tree, publishes it as a release asset, and
  `internal/bootstrap/sfizz.go` grows a `case "windows":` returning
  `sfizz.dll`. Optional backend either way — `.sfz` patches degrade gracefully
  when the DLL is absent, which is already the contract.

## 4. Porting plan

Phased so each phase is independently valuable and independently verifiable.

### W0 — Make `audio-core` compile for `x86_64-pc-windows-gnu`

The whole phase is three `#[cfg]` arms plus one dependency line:

1. `Cargo.toml`: `[target.'cfg(target_os = "windows")'.dependencies] cpal = "0.18.1"`.
   (cpal already appears under `macos`; the same crate serves both.)
2. `sfizz_sys.rs`: `#[cfg(target_os = "windows")] const LIB_NAMES = &["sfizz.dll"];`
   and a `BOOTSTRAPPED_LIB_NAME`; widen the bootstrap-path `cfg` or add a Windows
   branch using `%LOCALAPPDATA%`.
3. `lib.rs:1600`: a `#[cfg(target_os = "windows")]` `setup` binding — start with a
   stub that returns an error, exactly as macOS did in its P0.
4. `lib.rs:3376`: a `#[cfg(target_os = "windows")]` `load_synth_by_type` with the
   same body as the macOS one (soundfont + native).

**Proves:** the entire device-free Rust test suite (DSP, synth, envelopes, golden
samples, offline render) compiles and runs on Windows. **Cost:** hours.

### W1 — A cpal/WASAPI backend that makes sound

New `audio-core/src/backend_windows.rs`. The honest first move is to make
`backend_macos.rs` generic rather than copy it: the only macOS-specific parts are
the `#![cfg(target_os = "macos")]` line, the module doc, and the comment about
CoreAudio's safety offset. cpal's device/config/stream API is identical on WASAPI.
Either rename it to `backend_cpal.rs` with `#[cfg(any(target_os = "macos",
target_os = "windows"))]`, or keep two thin files over one shared implementation.

Two Windows-specific behaviours to get right here:

- **Report what you got.** `clamp_frames` will collapse 128 → the device period.
  Surface the accepted frame count (log it, and let the latency telemetry read it)
  so nobody debugs a "128-frame" build that is really 480.
- **Own the thread priority** (§3.2) rather than trusting cpal.

**Proves:** sound out of a Windows machine; the full DSP chain runs against a real
device. **Cost:** a day, most of it fighting COM/device-selection errors.

### W2 — The Go side

- `internal/audio/audio_windows.go` — `//go:build windows`, cgo LDFLAGS: the
  staticlib plus the MinGW system-lib list, `-static`.
- `cmd/polyclav/singleton_windows.go` — `daemonLock` + `acquireDefaultDaemonLock`
  over a named mutex (`CreateMutex` + `GetLastError() == ERROR_ALREADY_EXISTS`) or
  `LockFileEx` on the same lock files, preserving the "held" vs "unusable"
  distinction `singleton_unix.go` documents.
- `internal/bootstrap/sfizz.go` — `case "windows"`.

**Proves:** `polyclav.exe` builds, links and runs; the daemon starts, the web UI
serves, MIDI enumerates. **Cost:** a day.

### W3 — Packaging and CI

- `windows-latest` GitHub runner: `cargo build --target x86_64-pc-windows-gnu`,
  `go build`, `go test ./...`, plus the Rust test suite. Windows bills at 2x
  included minutes (vs macOS's 10x) and is **free on public repos**.
- Release artifact: a `.zip` with `polyclav.exe` (statically linked) plus the
  optional `sfizz.dll`, mirroring the macOS `polyclav-macos-arm64.tar.gz` shape in
  `publish.yml`.
- `docs/INSTALL.md` gains a Windows section, and its "Windows and PulseAudio-only
  systems are not supported" line changes.

### W4 — Hardware bring-up

A real Windows machine, a real USB MIDI keyboard, real speakers, and a stopwatch.
This is the phase that closes §3.1, §3.2 and the WinMM port-name heuristics — and
it is exactly what the VM in §6 cannot fully replace.

## 5. What can be proven without Windows

The macOS doc's central lesson applies here with one important upgrade: **on
Windows you can cross-compile, which you could not do for macOS.**

| Check | Runs on Linux today? | Notes |
|---|---|---|
| `cargo check --target x86_64-pc-windows-gnu` | **Yes** — already run | 3 errors, listed in §1. `rustup target add x86_64-pc-windows-gnu` is all it takes. |
| `cargo test --target x86_64-pc-windows-gnu` | **No** — needs a Windows runner or Wine | Tests execute, so they need a Windows kernel or Wine. Wine is a plausible cheap smoke test for the device-free suite. |
| `GOOS=windows go build ./...` | **Yes** (cgo off) | Confirms every pure-Go package; `internal/audio` and cgo-only deps are the expected exclusions. |
| `GOOS=windows CGO_ENABLED=1 go build` | **Yes, with MinGW-w64 installed** | This is the real cross-build. Unlike macOS there is no SDK-licensing problem — MinGW-w64 is a normal cross toolchain (§7). |
| Linking the Rust staticlib into the exe | **Yes, with MinGW-w64** | Same cross toolchain. |
| Running the binary | **No** | Needs Windows or Wine. |
| Audio, latency, MIDI, MMCSS | **No** | Needs a Windows machine with a device. |

So Linux can own *compilation and linking* of the Windows build end-to-end. What
it cannot own is *execution*.

## 6. Running real Windows inside this Linux jail

This is the piece the macOS port could never have: a **real Windows machine the
agent can drive**, rather than a borrowed Mac with no USB.

**Feasibility, verified in this jail (2026-10-27):**

| Requirement | State |
|---|---|
| Host CPU virtualization | `vmx` present (Intel i7-8700K), so KVM is available on the host |
| `/dev/kvm` in the jail | **Absent today.** yolo exposes it with `"kvm": true` (adds `--device /dev/kvm` and `--group-add keep-groups`), which is a *host fact* — `yolo check` inside a jail skips the check by design |
| Nested container runtime | **Works.** `podman run --rm alpine echo` succeeds; podman 5.8.9, cgroupfs, netavark |
| `/dev/net/tun` | **Present** (`crw-rw-rw-`) — dockur's networking needs it |
| `CAP_NET_ADMIN` / `CAP_SYS_ADMIN` | **Present** in the jail's capability set (not needed with user-mode networking, but available) |
| Network egress | Works (podman pulled `alpine` from Docker Hub) |
| Disk | **The tight constraint.** `/workspace` has ~52 GB free and is the *host's root LV at 95%*; the roomy filesystems (`/tmp`, `/var/lib/containers`, 3.4 TB) are **per-launch** and vanish on jail exit |

**Important performance note:** the jail is a *container*, not a VM. Passing
`/dev/kvm` through to it, and then to a nested container, is not nested
virtualization — QEMU talks to the host kernel's KVM directly. Expect near-native
guest performance, not the usual nested-virt penalty.

**The approach — dockur/windows via the jail's nested podman.** dockur handles the
unattended install (ISO download, autounattend, first-boot), which is the part
that is genuinely tedious to do by hand. This was **actually run in the jail on
2026-10-27 with `KVM=N`**: the image pulled, dockur downloaded Windows 11 LTSC
Evaluation (5.1 GB) from Microsoft, created the disk, booted QEMU 11.1.1, and
served the web console at HTTP 200. The *only* thing it lacked was `/dev/kvm`, so
the guest ran under TCG (~10× slower) — see the feasibility table above.

The invocation below is the one that works, and it is **not** the naive one. Two
of the settings exist purely to work around how this jail mounts `/sys`:

```bash
# Storage that outlives the guest: keeps the 5.1 GB ISO across restarts.
D="$HOME/windows-storage"
mkdir -p "$D"

# DEV=eno1 and NETWORK=slirp are required by this jail — see gotcha 1.
# DISK_SIZE=40G and ALLOCATE=N are required by dockur — see gotcha 2.
podman run -d --name windows \
  --net=host \
  --device /dev/kvm --device /dev/net/tun --cap-add NET_ADMIN \
  -e DEV=eno1 -e NETWORK=slirp \
  -e KVM=Y \
  -e VERSION=11l \
  -e DISK_SIZE=40G -e ALLOCATE=N \
  -e RAM_SIZE=8G -e CPU_CORES=6 \
  -e USERNAME=dev -e PASSWORD=dev \
  -e USER_PORTS=22 \
  -v "$D:/storage" \
  docker.io/dockurr/windows
```

`VERSION=11l` is Windows 11 LTSC (4.7 GB ISO, no Store, no bundled apps) — the
leanest SKU that is still a supported Windows 11. Then watch the install at
`http://localhost:8006` (web console) or RDP to `localhost:3389`. Note that with
`--net=host` a `-p` mapping is **ignored** — ports bind directly on the jail's own
localhost, which is what you want anyway.

**Gotcha 1 — `/sys` is a read-only host bind mount, so a nested container sees the
host's NICs.** dockur's `validateInterface()` is an unconditional
`[ ! -d "/sys/class/net/$DEV" ]`, and inside a nested podman container in this
jail `/sys/class/net` lists `eno1`, `podman0`, `veth*` (the host's view) while the
container's actual interface is `eth0`. So the default `DEV=eth0` fails with
*"Network interface 'eth0' does not exist inside the container!"*. Remounting a
fresh sysfs inside the container is denied even with `--cap-add SYS_ADMIN`
(`mount: permission denied`). The fix is `--net=host`, which makes the container's
netns match what `/sys` shows, plus `-e DEV=eno1` to name the jail's real
interface. Then add `-e NETWORK=slirp` so dockur uses QEMU user-mode networking
and **never touches that interface** — it only had to pass validation. Do not use
the default bridge mode with `--net=host`: it would create bridges and iptables
rules on the jail's own NIC and can cut the agent's network.

**Gotcha 2 — `DISK_SIZE` must be a real size, and the image must be sparse.**
`DISK_SIZE=12G` failed with `truncate: Invalid number: ''` (dockur sizes the
primary `data.img` from it); 40G works. `ALLOCATE=N` keeps the 40 GB raw image
sparse, so it costs ~0 until Windows writes to it — measured: a freshly created
40 GB `data.img` sat inside a 4.8 GB storage directory.

**SSH is not a dockur feature** — there is no SSH environment variable. Enable it
from inside the unattended install, which dockur exposes for exactly this:

```bash
# pass to the container: -e COMMAND='...'
powershell -Command "Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0; \
  Set-Service sshd -StartupType Automatic; Start-Service sshd; \
  New-NetFirewallRule -Name sshd -DisplayName 'OpenSSH Server' -Enabled True \
  -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22"
```

`USER_PORTS=22` is what makes that reachable: dockur warns that *"when you need to
forward custom ports to Windows, add them to the USER_PORTS variable"* under
user-mode networking. After that, `ssh dev@localhost` from inside the jail is a
normal shell on a normal Windows box — which is all the agent needs to install
MSYS2/MinGW-w64, Go, and the `x86_64-pc-windows-gnu` Rust target, clone the repo,
and run the whole W0–W2 loop.

**What the VM can prove:** the Windows build compiles and links with MinGW; the
binary runs; the offline render path produces correct audio; MIDI arrives through a
virtual WinMM port; the WASAPI backend opens a device and reports its real
`GetDevicePeriod` and `GetSharedModeEnginePeriod` (the §3.1 measurement); the
whole test suite passes on a Windows kernel.

**What it cannot prove:** audible correctness on real speakers, true round-trip
latency on the user's interface, MMCSS behaviour under load, and the Launchkey MK4's
real WinMM port names and DAW-mode wire behaviour. dockur *can* pass USB devices
through (`ARGUMENTS: "-device usb-host,vendorid=…,productid=…"` plus `/dev/bus/usb`),
so a real Launchkey is not strictly impossible — but that depends on the host's USB
tree reaching the jail and is not worth planning around until the build exists.

**Disk is the blocker to size first.** A comfortable Windows dev VM (Windows + MSYS2
+ Go + Rust + the repo + build artifacts) wants 60–80 GB. `/workspace` is the
host's root LV, and after caching the ISO it sat at **45 GB free / 96 % used**. A
40 GB sparse disk that grows to ~20–25 GB installed leaves ~20 GB for toolchains —
workable, tight, and it degrades the host's own headroom. The realistic options
are: free space on the host; accept the tight VM; or run it on `/tmp` (3.4 TB free)
and lose the guest on every jail restart. This is a decision for the human, not the
agent.

**The cached ISO is worth keeping.** dockur stores the downloaded ISO in
`/storage`, so mounting a durable directory means a jail restart does not repeat a
5.1 GB download. The ISO plus `windows.base`/`windows.ver` are already at
a directory outside the repo, so a restart does not repeat a 5.1 GB download; the
regenerable `data.img` was deliberately not kept. (Inside a yolo dev jail, that
directory must be under `$YOLO_DURABLE_DIR` — everything else in the jail's
filesystem is per-launch and is deleted when the jail exits.)

## 7. Cross-compilation verdict

**Unlike macOS, cross-compiling from Linux to Windows is a first-class, legal,
supported path — and it should be the primary CI strategy.**

- MinGW-w64 is an ordinary cross toolchain with no SDK-licensing problem (contrast
  `osxcross`, which needs an extracted Xcode SDK and lives in an Apple-licence gray
  zone).
- Rust's `x86_64-pc-windows-gnu` is Tier-1 and pairs with Go's cgo toolchain on
  Windows by construction.
- The whole toolchain is free and scriptable, so a `windows-latest` CI runner and a
  Linux cross-build are both viable — and the Linux cross-build gives the fastest
  inner loop (seconds, no VM).

The one thing cross-compilation does not give you is execution, which is what §6
is for. The combination — cross-build on Linux for speed, Windows VM for
execution, Windows CI for the gate — is strictly better than the macOS situation.

## 8. Recommended roadmap

| Phase | Do | Proves | Cost |
|---|---|---|---|
| **0. Compile** | W0: cpal target dep + 3 `cfg` arms | the entire Rust test suite builds for Windows | hours |
| **1. Sound** | W1: cpal/WASAPI backend (shared mode), report the real period, own thread priority | audio out of a Windows box, DSP chain against a real device | ~1 day |
| **2. Binary** | W2: `audio_windows.go`, `singleton_windows.go`, sfizz bootstrap case | `polyclav.exe` builds, links, runs; web UI and MIDI enumerate | ~1 day |
| **3. Gate** | W3: `windows-latest` CI job, static link, `.zip` release, INSTALL docs | a green Windows build on every PR | ~half day |
| **4. Measure** | §6 VM: `GetDevicePeriod` / `GetSharedModeEnginePeriod` on real endpoints | whether shared mode is good enough, or B/C is needed | ~half day once KVM is on |
| **5. Bring-up** | W4: real Windows machine + keyboard + speakers | audible correctness, real latency, MMCSS under load, WinMM port names | the hard wall |

Phases 0–4 are agent-doable and mostly device-free. Phase 5 is a human in a room.

## 9. Open questions / risks

- **Shared mode vs `IAudioClient3` vs exclusive mode** (§3.1). Decide after
  measuring, not before. If the answer is "exclusive", budget a week and expect to
  own COM lifetime, device-in-use handling and format negotiation by hand.
- **What period do real endpoints actually report?** Published measurements show
  10 ms minimums on common drivers and 7 ms on one class-compliant USB device. The
  XR18 (class-compliant USB audio) is the interface that matters here — measure it
  first.
- **Is ~10 ms acceptable for this instrument?** That is a judgment about playing
  feel, not a code property. It needs a human at a keyboard. (10 ms one-way is
  roughly what a well-configured Windows soft-synth rig gives; it is noticeably
  worse than 2.67 ms.)
- **WinMM MIDI is legacy.** rtmidi's Windows backend is `winmm`, which has coarse
  timestamps and no modern features. Windows MIDI Services is the current API and
  is not what rtmidi uses. Adequate for note input; a known ceiling for anything
  timing-critical.
- **MMCSS is unobservable in CI.** A mis-scheduled audio thread still renders
  correct samples — it just drops out under load. Only hardware bring-up catches it.
- **Power throttling and antivirus.** A distributed `.exe` will meet SmartScreen and
  possibly AV heuristics; unsigned binaries get a scary dialog. Signing is a
  separate project, and a reason to prefer a package manager (winget/scoop)
  over a raw download.
- **The static link choice is load-bearing.** Get `-static` wrong and the release
  is a `.exe` that only runs on machines with MinGW installed. Test on a clean
  Windows image, not the dev VM.
- **Disk for the VM** (§6) — 52 GB free on a 95%-full host LV is the practical
  constraint on how much Windows tooling can be installed.
- **Windows 10 is past consumer end-of-life** (Oct 2025). For a *product*, target
  Windows 11; for the *test VM*, Windows 11 LTSC (`11l`) is both modern and lean.
- **Two Rust targets, one tree.** A developer who builds `audio-core` for
  `x86_64-pc-windows-msvc` gets a staticlib Go cannot link. The CI job and
  `docs/INSTALL.md` must both say `x86_64-pc-windows-gnu` explicitly, and the
  workspace already has per-side build-state shadowing (`per_side_paths`) that this
  must not collide with.

## 10. Evidence log

Everything asserted above that is checkable in this tree was checked on
2026-10-27, in the Linux dev jail:

| Claim | How it was checked |
|---|---|
| Three Rust errors, no more | `cargo check --manifest-path audio-core/Cargo.toml --target x86_64-pc-windows-gnu` after `rustup target add x86_64-pc-windows-gnu` |
| `internal/audio` has no Windows file | `ls internal/audio/` → `audio_linux.go`, `audio_darwin.go`; `GOOS=windows go build ./...` → *"build constraints exclude all Go files"* |
| `main.go` needs a Windows singleton | `rg acquireDefaultDaemonLock` → `main.go:109` calls it; only definition is `singleton_unix.go` (`//go:build unix`) |
| rtmidi supports Windows | vendored `rtmidi.go` preamble: `#cgo windows CXXFLAGS: -D__WINDOWS_MM__`, `LDFLAGS: -luuid -lksuser -lwinmm -lole32` |
| cpal's WASAPI is shared-mode only | cpal `src/host/wasapi/device.rs` — *"We always create voices in shared mode"*; `AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM` for output |
| cpal reports min == max buffer size | same file, `supported_formats()`: `SupportedBufferSize::Range { min: frames, max: frames }` from `GetDevicePeriod` |
| No exclusive mode in cpal | PR "Added WASAPI exclusive host" closed, not merged; cpal README lists WASAPI (default), ASIO and JACK as Windows backends |
| Nested podman works in this jail | `podman run --rm docker.io/library/alpine:3.20 echo` → `NESTED_PODMAN_OK` |
| `/dev/kvm` is the only missing device | `ls /dev` shows no `kvm`; `/dev/net/tun` present; `CapEff` includes `CAP_NET_ADMIN` and `CAP_SYS_ADMIN` |
| `kvm` is a real yolo key | `yolo config-ref` → `kvm (boolean, default false): Expose /dev/kvm inside the jail`; `yolo check --no-build` accepts it at workspace scope and reports *"Inside jail — kvm checks skipped"* |
| Disk is the VM constraint | `df -h /workspace` → 52 G avail on the host root LV at 95%; `/tmp` and `/var/lib/containers` have 3.4 TB but are per-launch |
| dockur runs in this jail (minus KVM) | `podman run … docker.io/dockurr/windows` with `KVM=N`: pulled the image, downloaded `win11x64-enterprise-ltsc-eval.iso` (5.1 GB) from Microsoft, created `data.img`, booted QEMU 11.1.1, web console returned HTTP 200 |
| nested containers see the host's NICs | `podman run alpine -- ip -o link` → `eth0@if21`, but `ls /sys/class/net` → `eno1 lo podman0 veth0 veth1`; dockur `validateInterface()` tests `/sys/class/net/$DEV` |
| fresh sysfs cannot be remounted in a nested container | `--cap-add SYS_ADMIN` + `mount -t sysfs sysfs /sys` → `mount: permission denied (are you root?)` |
| `DISK_SIZE` too small breaks disk creation | `-e DISK_SIZE=12G` → `truncate: Invalid number: ''` then *"Could not create a growable raw disk image"*; `40G` + `ALLOCATE=N` succeeded |
| the raw disk stays sparse | 40 GB `data.img` inside a 4.8 GB storage directory (`du -sh`) |
