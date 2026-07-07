# Running polyclav on macOS (without a Mac)

> Status: strategy / feasibility document. Scope: what it takes to build, automatically test, and (eventually) hand-verify the Linux-first polyclav live-piano host on macOS/Apple Silicon when the owner has no Mac.

---

## 1. TL;DR / verdict

**Yes, you can get polyclav building and running a large fraction of its test suite on macOS without ever touching a Mac — but you cannot do the final "does it actually sound right and drive my hardware" verification remotely.** The split is clean:

- **Build + automated test on macOS: achievable via GitHub-hosted Apple-Silicon CI runners** (free on public repos), *after* one real code change: audio-core hard-depends on the PipeWire crate, which is Linux-only and has no macOS port. That single dependency has to move behind a `cfg(target_os)` seam with a CoreAudio backend (cpal or coreaudio-rs) on the other side. Everything else in the stack already ports: MIDI (`rtmidi` has a native CoreMIDI backend), OSC (pure-Go UDP), and the entire synth/DSP/plugin core (oxisynth, fundsp, CLAP, sfizz) are platform-neutral.
- **Real-hardware verification: no.** No cloud/CI Mac can see your USB Launchkey MK4 or give you true speaker/round-trip latency ([AWS EC2 Mac has no USB attach](https://aws.amazon.com/ec2/instance-types/mac/faqs/); [Apple's Virtualization.framework has no general USB passthrough](https://developer.apple.com/forums/thread/741785)). The XR18 mixer is the one exception — it's OSC-over-UDP, so any Mac that can route to it on your LAN/VPN can drive it.

The honest one-paragraph answer: **treat this as two projects.** Project A (build a working macOS binary and prove the synth/DSP/MIDI-parse logic with deterministic, device-free tests) is entirely doable off-Mac and cheaply. Project B (confirm audible correctness, real latency, and the Launchkey DAW-mode handshake over USB) genuinely requires a physical Mac plus the physical devices in the same room — a one-time bring-up you close by borrowing/renting/buying a Mac mini or shipping the rig to a beta tester. Don't let Project B's hard wall block Project A; almost all regression value lives in Project A.

---

## 2. What's actually coupled to Linux

All OS-audio coupling is remarkably shallow and localized. On the Rust side it lives in **one file** (`audio-core/src/lib.rs`) in **two functions** (`run_audio` at `lib.rs:1271-1393` and the RT callback `process_audio` at `lib.rs:1395-1608`). On the Go side it lives in **one file** (`internal/audio/audio.go`) plus its `zix_link.go` sibling. There are **zero `GOOS` conditionals and zero `_linux.go`/`_darwin.go` files** in the Go tree today — the only build tag anywhere is `//go:build !portable` in `zix_link.go`, which is a nix-vs-distro distinction, not an OS one.

### Coupling map

| Coupling point | Location | Verdict | Notes |
|---|---|---|---|
| **PipeWire audio I/O (Rust crate)** | `audio-core/Cargo.toml` `pipewire = "0.10"`, `lib.rs:11-20`, setup at `lib.rs:1275-1391`, SPA buffer marshalling at `1460-1483`/`1583-1607` | **Hard wall** | [PipeWire is Linux-only, no macOS port](https://crates.io/crates/pipewire). The single blocker. Replace with CoreAudio via [cpal](https://github.com/RustAudio/cpal) or coreaudio-rs. |
| **PipeWire link (Go cgo)** | `internal/audio/audio.go:16` `#cgo pkg-config: libpipewire-0.3 libspa-0.2` (unconditional — file has no build constraint) | **Hard wall** | Must move behind `//go:build linux`; a `//go:build darwin` sibling re-states `-framework` flags instead. |
| **MIDI (rtmidi via gomidi)** | `internal/midi/midi.go`, `internal/launchkey/driver/driver.go` | **Portable** | rtmidi's vendored cgo already has `#cgo darwin … -framework CoreMIDI/CoreAudio/CoreServices/CoreFoundation` ([rtmidi.go](https://gitlab.com/gomidi/midi/-/raw/master/v2/drivers/rtmididrv/imported/rtmidi/rtmidi.go)). ALSA `-lasound` is gated to `#cgo linux`. Requires `CGO_ENABLED=1`. |
| **OSC (XR18 control)** | `internal/osc/client.go` | **Portable** | Pure-Go `hypebeast/go-osc` over UDP, no cgo. `liblo` in flake/jail is only the `oscsend`/`oscdump` dev CLIs — never linked into the binary. |
| **Synth + DSP core** | oxisynth `0.1`, fundsp `0.23`, `synth/*`, `dsp/*` | **Portable** | Pure Rust, no OS coupling. Compiles and runs identically on macOS. |
| **CLAP host** | `clack-host 0.1`, `plugin_clap.rs` | **Portable** | Pure-Rust host; loads `.clap` bundles via libloading. [clap-sys has zero deps / no headers](https://crates.io/api/v1/crates/clap-sys/0.5.0/dependencies) — the `clap` flake input is vestigial. On macOS clack-host pulls objc2-foundation (NSBundle) → needs `-framework CoreFoundation`/`Foundation`. |
| **sfizz (optional SFZ)** | `audio-core/src/sfizz_sys.rs:53` dlopen `libsfizz.so.1`/`libsfizz.so` | **Small fix** | Add a Darwin branch for `libsfizz.dylib`. Optional backend, degrades gracefully; not a build dep. No Homebrew formula — macOS users install the [SFZTools `.pkg`](https://sfztools.github.io/sfizz/downloads/). |
| **LV2 host stack** | `livi 0.7`+`lilv`, `plugin_lv2.rs`, cgo `-llilv-0 -lserd-0 -lsord-0 -lsratom-0` (`audio.go:13`), `-lzix-0` (`zix_link.go`) | **Buildable, low value** | All present as [Homebrew arm64 bottles](https://formulae.brew.sh/formula/lilv) (lilv 0.28, serd/sord/sratom, zix 0.8.2). But **LV2 plugins are rare on macOS** — AU/VST3/CLAP dominate. See §3 plugin note. |
| **`-ldl` in cgo LDFLAGS** | `audio.go:4` | **Drop on Darwin** | No `libdl` on macOS (`dlopen` is in libSystem); `-ldl` fails the link. `-lpthread`/`-lm` are harmless libSystem no-ops. |
| **glibc/ALSA env hacks** | `.mise.local.toml` (`-idirafter` glibc, `CPLUS_INCLUDE_PATH` alsa, `-lasound`) | **Linux-only** | glibc doesn't exist on macOS; the `#include_next <cstdlib>` problem doesn't arise with libc++/Apple SDK. Omit entirely. |
| **CI / release** | `.github/workflows/ci.yml` (ubuntu + nix), `publish.yml` (manylinux wheel, `readelf`/`ldd`) | **Needs macOS job** | See §4 and §7. |

### The FFI contract that must be preserved byte-for-byte

The Go↔Rust boundary is a hand-written C ABI (`audio-core/include/polyclav_audio.h`; no `build.rs`/cbindgen) exporting **33 `#[no_mangle] extern "C"` functions** from a `staticlib`. **None of them touch PipeWire** — they push onto lock-free queues / atomics that the audio thread reads. This entire surface is platform-neutral and must stay identical across the port:

- **Lifecycle:** `polyclav_audio_start`, `polyclav_audio_stop`
- **Backend selection:** `set_soundfont`, `reload_soundfont`, `set_lv2_plugin`, `set_clap_plugin`, `sfizz_available`, `set_native_patch`
- **MIDI:** `note_on`, `note_off`, `cc`, `pitch_bend`
- **Generic DSP (6):** master volume, compressor, reverb, patch gain, mastering compressor, limiter ceiling
- **Native-synth params (17):** cutoff, resonance, filter env, osc, noise, glide, amp env, pulse width, drive, vel routing, kbd track, LFO, bend range, voice mode, oversample, etc.

The audio thread communicates only through process-global statics — `MIDI_QUEUE` (`lib.rs:133`), `SYNTH_RELOAD_QUEUE` (`152`), `SOUNDFONT_PATH` (`138`), `DSP_PARAMS` (`619`) — plus the `SynthBackend` enum whose variants each expose `render(&mut [f32])`. **This is the de-facto backend seam; none of it mentions PipeWire.** The port formalizes it into an `AudioBackend` trait; the FFI header does not change.

### Baked-in audio assumptions to watch on CoreAudio

- `SAMPLE_RATE = 48000.0` (`lib.rs:39`) is forced into the stream format and every backend constructor. **There is no runtime SR negotiation, and CoreAudio's default device rate is frequently 44.1 kHz.** A CoreAudio backend must either request a 48 k device rate or the DSP must be SR-parameterized. Note `dsp/compressor.rs:1` and `dsp/reverb.rs` (Freeverb) hardcode 48 k tunings and take no SR arg, so they'd mistune off 48 k; the native synth/ADSR/osc/filter *are* properly SR-parameterized.
- Format is hardcoded **F32LE, 2ch, interleaved**. Block size is already dynamic (`n_frames` from `pw_buf.requested`, capped by `MAX_QUANTUM=8192`), so variable CoreAudio buffer sizes are already tolerated.
- **CoreAudio typically hands back non-interleaved buffers**, yet every non-oxisynth backend renders to separate L/R scratch and re-interleaves (`sfizz.rs`, `plugin_lv2.rs`, `plugin_clap.rs`) — a CoreAudio backend would de-interleave what the backends just interleaved. An argument for pushing the L/R seam down eventually; not a blocker.

---

## 3. Porting plan

The goal: **one codebase, two audio backends, an unchanged FFI/Go contract.**

### Phase P0 — Make audio-core compile on macOS (unblocks all Rust tests for free)

1. Move the PipeWire crate to a target-gated dependency in `audio-core/Cargo.toml`:
   ```toml
   [target.'cfg(target_os = "linux")'.dependencies]
   pipewire = "0.10"
   ```
2. Gate `use pipewire as pw;`, `run_audio`, `process_audio`, and the PipeWire bodies of `polyclav_audio_start`/`_stop` behind `#[cfg(target_os = "linux")]`. Provide a `#[cfg(not(target_os = "linux"))]` stub for `start`/`stop` so the FFI symbols still exist (they can return an error until P2).
3. Do the same for `livi`/lilv if it doesn't cleanly build on macOS, or drop the LV2 backend on Darwin (see plugin note).

This alone lets **all 147 Rust `#[test]`s compile and run on macOS** — they never needed PipeWire; the crate-root dependency was the only thing blocking them.

### Phase P1 — Introduce the `AudioBackend` seam

Today the render-dispatch + DSP chain is copy-pasted *inside* the PipeWire callback. Extract it first:

- Hoist `process_audio`'s body — MIDI drain (`1413-1458`), backend hot-swap (`1397-1410`), native-param push (`1489-1513`), render dispatch (`1515-1532`), DSP chain (`1534-1580`) — into a single `fn render_block(user_data: &mut UserData, samples: &mut [f32])`. This is 100% portable buffer math.
- Define a small trait, e.g.:
  ```rust
  trait AudioBackend { fn start(render: impl FnMut(&mut [f32])) -> Result<Self>; fn stop(&mut self); }
  ```
- `run_audio`/`process_audio` become the **Linux** implementation (buffer acquisition + format/SR negotiation only, calling `render_block`).

### Phase P2 — CoreAudio backend for macOS

- Add a `#[cfg(target_os = "macos")]` backend using [`cpal`](https://github.com/RustAudio/cpal)'s CoreAudio backend (simplest cross-platform path) or coreaudio-rs/`AURenderCallback` directly. It allocates/receives its own buffer, handles the 44.1-vs-48 k SR question, de-interleaves as needed, and calls the **unchanged** `render_block`.
- cpal pulls `coreaudio-sys`, whose `build.rs` emits framework link directives for **AudioUnit, AudioToolbox, CoreAudio, CoreMIDI, IOKit** ([coreaudio-sys build.rs](https://github.com/RustAudio/coreaudio-sys/blob/master/build.rs)). Because a Rust **staticlib swallows** those `cargo:rustc-link-lib=framework=` directives (the exact same reason the LV2 `-l` flags are re-stated on Linux), the Go cgo step must re-state them (see below).

### Latency configuration on macOS

Latency is configured very differently on the two platforms, and it's the one piece of behavior the CoreAudio backend must actively re-implement (not just inherit). On Linux polyclav requests its audio period with a single stream property — `*pw::keys::NODE_LATENCY => "128/48000"` (`lib.rs:1289`): a **128-frame quantum at 48 kHz** (~2.67 ms/period). PipeWire treats it as a *graph-wide, negotiable* hint that WirePlumber or `clock.force-quantum` can override globally — that's how the XR18 gets pinned to a low quantum (see `docs/INSTALL.md`).

macOS has **no graph, no WirePlumber, no `pw-metadata` analog.** Latency is a *per-device, per-app* CoreAudio property the backend sets directly on the HAL:

| Concept | Linux / PipeWire | macOS / CoreAudio |
|---|---|---|
| Buffer size ("quantum") | `NODE_LATENCY "128/48000"` (request) | `kAudioDevicePropertyBufferFrameSize` on the device — the app sets it |
| Allowed range | graph-negotiated | `kAudioDevicePropertyBufferFrameSizeRange` (device-reported; clamp to it) |
| Sample rate | `set_rate(48000)` in the format POD | `kAudioDevicePropertyNominalSampleRate` |
| Who owns it | daemon/graph, forced globally | the app, per device |
| Unavoidable fixed latency | mostly tunable via quantum | `kAudioDevicePropertySafetyOffset` + `…PropertyLatency` — driver-fixed, **query-only, cannot be reduced** |
| Exclusive access | — | `kAudioDevicePropertyHogMode` (lets you push smaller buffers) |

In the cpal backend this is just `StreamConfig { buffer_size: BufferSize::Fixed(128), sample_rate: SampleRate(48000), channels: 2 }` — cpal maps `Fixed(n)` straight onto `kAudioDevicePropertyBufferFrameSize`, erroring if `n` is outside the device's range (so clamp to the reported `…BufferFrameSizeRange`). To report a true round-trip figure you *query* safety-offset + stream latency; a fixed slice of macOS latency is the driver's safety offset which — unlike the PipeWire quantum — you cannot lower.

Two payoffs are already in the code: the render path has **no hard-coded block size** (`process_audio` honors a dynamic `n_frames` up to `MAX_QUANTUM = 8192`, `lib.rs:1476`), so whatever buffer size CoreAudio hands back just works; and the class-compliant **XR18 appears as a native CoreAudio device**, so you set its buffer size the same way — no WirePlumber rule needed. **Recommendation:** lift the hardcoded `"128/48000"` into a `latency_frames` config value that feeds `NODE_LATENCY` on Linux and `BufferSize::Fixed` on macOS — one integer, two backends. Caveat: buffer-size/latency tuning is only meaningful against real hardware (§5), so CI can't measure it — it validates that a requested size is *accepted*, not what it sounds like.

### Phase P3 — Go cgo split

- Split `internal/audio/audio.go` into `audio_linux.go` / `audio_darwin.go` (or gate the cgo preamble by build tag). There is **no seam for this today** — the preamble compiles unconditionally.
- Darwin cgo LDFLAGS ≈ `-lpolyclav_audio_core -framework CoreAudio -framework CoreMIDI -framework AudioToolbox -framework AudioUnit -framework CoreFoundation` (plus `-framework Foundation` for clack-host's NSBundle usage), **replacing** Linux's `-lpthread -ldl -lm` + `pkg-config libpipewire-0.3 libspa-0.2`.
- Keep the LV2 `-llilv-0 -lserd-0 -lsord-0 -lsratom-0` **only if** you keep the LV2 backend; resolve them from `/opt/homebrew/lib` instead of `/nix/store`.
- Fix `sfizz_sys.rs:53` to try `libsfizz.dylib` on Darwin, or the SFZ backend silently stays inert on Mac.

### macOS build dependencies (Homebrew)

| Formula/pkg | Version (2026-07) | arm64 bottle | Purpose |
|---|---|---|---|
| `lilv` (pulls `lv2`, `serd`, `sord`, `sratom`, `zix`, `libsndfile`) | 0.28.0 | ✅ | LV2 host stack ([formula](https://formulae.brew.sh/formula/lilv)) |
| `liblo` | 0.36 | ✅ | OSC dev CLIs only ([formula](https://formulae.brew.sh/formula/liblo)) — not linked |
| `pkg-config`/`pkgconf` | — | ✅ | `.pc` resolution; non-keg-only formulae symlink into `/opt/homebrew/lib/pkgconfig` (default search path), so `PKG_CONFIG_PATH` is usually unnecessary |
| `sfizz` | — | ❌ (no formula) | Install the [SFZTools `.pkg`](https://sfztools.github.io/sfizz/downloads/) if you want `.sfz`; optional |
| `clap` | — | not needed | clap-sys is hand-written, zero-dep |
| `llvm` (libclang) | — | usually not needed | On macOS bindgen auto-detects Apple's libclang via `xcode-select`; `LIBCLANG_PATH` only needed if using Homebrew LLVM. Moot if PipeWire (the only bindgen consumer) is excluded — though coreaudio-sys also runs bindgen. |

**The zix caveat:** Homebrew `lilv 0.28` depends on **`zix 0.8.2` as a separate library**, so on macOS you need the `-lzix-0` (i.e. the `!portable`, non-`-tags portable`) path — the same as nixpkgs lilv 0.26+, *opposite* to the Ubuntu-wheel path (Ubuntu's older lilv 0.24 vendors zix). Note the raw research contained a stale "Homebrew lilv 0.24 vendors zix → use `-tags portable`" claim; **trust the verified 0.28/separate-zix finding**, but re-confirm against whatever `lilv` version Homebrew actually installs at build time — this can drift.

- `CGO_CFLAGS_ALLOW="-fno-strict-aliasing|-fno-strict-overflow"` exists only to accept PipeWire's `.pc` flags — a harmless no-op on macOS (no PipeWire `.pc`).

### Plugin ecosystem note (LV2 vs CLAP vs AU)

- **LV2 *hosting* compiles on macOS** (lilv has an arm64 bottle) but LV2 *plugins* are rare on Mac — it'd be largely dead weight. Consider shipping macOS as **CLAP + sfizz + native synth only** and dropping the whole lilv/serd/sord/sratom/zix Homebrew chain.
- **CLAP is the best-fit cross-platform path** (pure-Rust host, growing catalog: u-he, Airwindows).
- **AudioUnit hosting** would give far more Mac plugin coverage but there is **no mature pure-Rust AU host** — it's objc2 + AudioToolbox work. Treat as a "phase 2" note, explicitly *not* a v1 blocker.

### Nix vs Homebrew for macOS

The current `flake.nix` won't evaluate on `aarch64-darwin` because `pipewire` and `alsa-lib` are unconditionally in `buildInputs` (both `meta.platforms = linux`). To serve both OSes from one flake you'd wrap them in `lib.optionals stdenv.isLinux [...]` + a Darwin frameworks branch. Feasible, but **Homebrew is the pragmatic macOS-CI choice**: GitHub's Apple-Silicon runners ship Homebrew + Xcode CLT preinstalled, so you just `brew install lilv liblo` (or nothing, if CLAP-only) and rely on the runner's SDK for frameworks + libclang. Keep the Nix flake as the Linux devShell/CI and add a separate Homebrew-based macOS job.

---

## 4. Developing & testing WITHOUT a Mac

This is the heart of the doc. The strategy: **prove everything that can be proven deterministically and device-free on CI; treat real hardware as a separate, later, human step.**

### 4.1 GitHub Actions macOS runners

**Architecture & labels (as of 2026-07, verified):** every *unsuffixed* macOS label — `macos-latest`, `macos-14`, `macos-15`, `macos-26` — is **ARM64 / Apple Silicon (M1)** ([GitHub-hosted runners reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)). Intel requires an explicit `-intel` (`macos-15-intel`, `macos-26-intel`) *or* the legacy `-large` suffix. Confusingly, `-xlarge` is the opposite: a *larger ARM* (M2 Pro) runner, not Intel.

**Specs (standard, verified):**

| Runner | vCPU | RAM | Disk | Overage $/min |
|---|---|---|---|---|
| standard arm64 (M1) — `macos-14/15/26` | 3 | 7 GB | 14 GB SSD | $0.062 |
| standard Intel (x64) — `macos-15-intel` | 4 | 14 GB | 14 GB SSD | $0.062 |
| `macos-*-large` (12-core Intel) | 12 | 30 GB | 14 GB | $0.077 |
| `macos-*-xlarge` (5-core M2 Pro) | 5 | 14 GB | 14 GB | $0.102 |

The **14 GB SSD on standard arm64 is tight** for Xcode + Rust toolchain + Go module cache + build artifacts — watch for disk-full failures; clean aggressively or go `-large`/`-xlarge` if you hit it.

**Pin an explicit label — do not use `macos-latest` right now.** `macos-latest` is [mid-migration from `macos-15` to `macos-26`, rolling out June 15 → July 15, 2026](https://github.com/actions/runner-images/issues/14167) ([changelog](https://github.blog/changelog/2026-05-14-github-actions-upcoming-image-migrations/)). Today (2026-07-07) it may resolve to either. Pin `runs-on: macos-15` for stability (macos-26 breaking changes include default Xcode/Node/OpenSSL/Ruby bumps).

**Headless VM.** These are headless VMs with no monitor, speakers, or physical MIDI ports. Jobs run inside an auto-logged-in Aqua/GUI session (which is why CoreMIDI/`MIDIServer` and audio work at all).

**Limits & cost (verified):**
- **6-hour job limit; 35-day workflow-run limit** ([limits](https://docs.github.com/en/actions/reference/limits)).
- **macOS bills included minutes at a 10x multiplier** vs Linux (Windows 2x) ([billing](https://docs.github.com/en/actions/concepts/billing-and-usage)) — a 10-minute macOS job burns 100 included minutes.
- **Concurrency is the real bottleneck: only 5 concurrent macOS jobs** for Free/Pro/Team (50 Enterprise), *shared* across standard and larger runners — a big matrix serializes.
- **Standard macOS runners are FREE on public repositories** ([pricing](https://docs.github.com/en/billing/reference/actions-runner-pricing)) — keep the repo public if you can; that's the cheapest path to Mac CI with no hardware. Larger runners are never free and never draw included minutes.

**Sample macOS CI job sketch:**

```yaml
# .github/workflows/ci-macos.yml
jobs:
  macos:
    runs-on: macos-15           # pinned; NOT macos-latest during the migration window
    env:
      CGO_ENABLED: "1"          # rtmidi + the staticlib link require cgo
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.26.x' }
      - uses: dtolnay/rust-toolchain@stable
      # CLAP-only build needs no brew deps; add these only if keeping the LV2 backend:
      # - run: brew install lilv liblo
      - name: Build Rust staticlib (macOS backend)
        run: cargo build --release --manifest-path audio-core/Cargo.toml
      - name: Rust DSP/synth tests (device-free)
        run: cargo test --manifest-path audio-core/Cargo.toml
      - name: Go build + tests
        run: |
          go build -tags portable ./...     # 'portable' → drop -lzix-0 only if brew lilv vendors zix; else omit
          go test ./...
      # Optional: interactive debug only when something above failed
      - name: Debug shell on failure
        if: ${{ failure() }}
        uses: mxschmitt/action-tmate@v3
        with: { limit-access-to-actor: true }
```

### 4.2 Interactive "borrowed Mac" via action-tmate

When you need to poke at CoreAudio/CoreMIDI live, [`mxschmitt/action-tmate`](https://github.com/mxschmitt/action-tmate) starts a tmate session on the runner and prints an `ssh <token>@<region>.tmate.io` string (plus a web-terminal URL) **to the Checks log every ~5 seconds**. You get a real interactive shell on an Apple-Silicon Mac VM to run `cargo`, `go test`, `system_profiler SPAudioDataType`, etc. — effectively a rented Mac for the job's lifetime.

- **Bounded by the 6-hour job limit**; ends when the workflow ends, when a connected user exits the shell, or when you `touch continue` in the workspace.
- Gate it with `if: ${{ failure() }}` so it only opens on failing jobs, and set `limit-access-to-actor: true` to restrict SSH to the triggering GitHub user. (Traffic routes through tmate.io's public relay — don't expose secrets.)

### 4.3 Headless audio strategy — offline render is the robust path

**Do NOT rely on a runner's default output device.** The critical correction to earlier assumptions: current runners are **not** provisioned with BlackHole. GitHub [removed blackhole-2ch from the images](https://github.com/actions/runner-images/pull/9487) (upstream site flagged malicious, TLS cert expired); the current [`install-audiodevice.sh`](https://raw.githubusercontent.com/actions/runner-images/main/images/macos/scripts/build/install-audiodevice.sh) installs only `switchaudio-osx` and `sox`. The default device today is Apple's virtual **"Null Audio Device"** — and it has two disqualifying problems for verification:

1. **It's a null sink, not a loopback** — audio written to it is discarded, so you cannot read back what the app played.
2. **It fails to initialize on ~30% of runs** ([open issue #13668](https://github.com/actions/runner-images/issues/13668)), leaving `system_profiler` empty and causing device-open errors (`kAudioQueueErr_InvalidDevice -66680`). This is flaky red CI unrelated to your code.

So the ranked strategy is:

**PRIMARY — offline / non-realtime render into a buffer, no device at all.** This sidesteps devices, coreaudiod, sessions, reboots, and the 30% flake entirely; it's deterministic and faster-than-realtime. **polyclav is perfectly positioned for this** because its DSP chain is already pure `&mut [f32]` math (`lib.rs:1515-1580`) and the Rust *test* harness already renders offline: `render_ms()` (`synth/mod.rs:807`) renders `synth.render(chunk)` into a `Vec<f32>` in 128-frame blocks and asserts RMS (`:827`), an `hf_ratio` brightness proxy (`:841`), and **bit-exact golden samples** (`:857`). The house style already proves the approach — it's just not exposed outside the test module. (For a CoreAudio-native offline path Apple offers [AVAudioEngine manual rendering](https://developer.apple.com/documentation/avfaudio/audio_engine/performing_offline_audio_processing) / [`renderOffline`](https://developer.apple.com/documentation/avfaudio/avaudioengine/renderoffline(_:to:)), but for polyclav the pure-buffer path — your own synth+DSP with no CoreAudio — is more portable and even runs on Linux CI.)

**SECONDARY — BlackHole loopback capture**, only if you must exercise the *real* CoreAudio output path. [BlackHole is a userspace HAL plugin, no kext, signed+notarized, no SIP disable](https://github.com/existentialaudio/blackhole); set output to "BlackHole 2ch" with [`SwitchAudioSource`](https://github.com/deweller/switchaudio-osx) and record its input with `ffmpeg`/`sox`. **But it's impractical on hosted runners:** the cask prints "You must reboot for the installation to take effect," coreaudiod won't enumerate a newly-dropped HAL plugin until reload, [`SwitchAudioSource` then fails until reboot](https://github.com/actions/runner-images/issues/11746), [`launchctl kickstart` of coreaudiod is blocked on macOS 14.4+ even as root](https://www.kevinmcox.com/2024/03/changes-to-launchctl-kickstart-in-macos-14-4/), and **you cannot reboot a hosted runner mid-job**. Realistic only on a **self-hosted** runner with BlackHole baked into the image and rebooted once.

### 4.4 Virtual MIDI injection (CoreMIDI virtual ports)

MIDI *input* is testable headlessly on hosted runners. **This is proven:** the [`midir` crate's `tests/virtual.rs`](https://raw.githubusercontent.com/Boddlnagg/midir/master/tests/virtual.rs) creates virtual in/out CoreMIDI ports, sends NoteOn/NoteOff between them, and asserts receipt — and its [CI runs `cargo test` on a `macos-latest` "macOS (CoreMIDI)" matrix entry](https://raw.githubusercontent.com/Boddlnagg/midir/master/.github/workflows/ci.yml). [`jazz-soft/midi-test`](https://github.com/jazz-soft/midi-test) explicitly recommends `runs-on: macos-latest` for MIDI CI. Because `MIDIServer` is a per-user Aqua LaunchAgent and hosted runners run in a logged-in Aqua session, `MIDISourceCreate`/`MIDIDestinationCreate` succeed.

Two design constraints:

- **Don't use the IAC driver** — it's disabled by default and normally enabled via the Audio MIDI Setup GUI ([guide](https://midilize.com/guides/iac-driver-mac)); no ports until you enable it. Programmatic virtual ports need no GUI.
- **A single RtMidi client cannot receive from a virtual port it created itself** ([thestk/rtmidi #208](https://github.com/thestk/rtmidi/issues/208)). So the test injector must be a **separate process/client** from the app under test. Pattern: a small injector opens a virtual *source* (`rtmididrv.OpenVirtualOut("test-injector")`), sends `NoteOn`/`ControlChange`, and the app-under-test (separate process) finds that port, listens, and the test asserts the delivered `midi.Event` / resulting state. This exercises `internal/midi.Listen` and the parse/dispatch path headlessly. (Open question: whether two `rtmididrv.New()` *instances in the same process* suffice, or a separate OS process is strictly required — RtMidi's self-filtering suggests separate processes are safest.)

Note also the `Role`/`PickPortName` dual-seq-port heuristics (`midi.go:116-208`) and the Ardour "MIDI 2" duplicate-name workaround (`midi.go:189`) are tuned to Linux ALSA-seq port names; the MK4 enumerates differently under CoreMIDI, so those substring heuristics ("midi"/"daw") need re-validation against real CoreMIDI names — which only the physical device can provide (see §5).

### 4.5 Concrete recommendation: add an offline-render surface to audio-core

To maximize CI coverage, make the offline path a first-class, non-test capability. Ranked by leverage:

| # | Change | Unlocks | Effort |
|---|---|---|---|
| **A** | Make `pipewire` target-conditional + `#[cfg]`-gate `use pipewire`/`process_audio`/`start`/`stop` (Phase P0) | **All 147 Rust DSP/synth tests on macOS** | tiny |
| **B** | Extract `render_block(synth, dsp, &mut [f32])` shared by `process_audio` + a new offline renderer; add a Rust integration test (optionally a `hound` WAV dump) asserting non-silence/RMS/duration | whole synth+DSP chain assertable end-to-end, not just per-module | small |
| **C** | Add `polyclav render --clip <id> --out x.wav --seconds N` wiring the player `Sink` (`player.go:29`) into (B) instead of `audio.PushMIDI` | the audition path (`--play`) becomes **CI-observable**: render a clip, assert WAV length + non-silence | small (needs B) |
| **D** | Move `realAudioBackend` out of `internal/patches` (it's already behind the `audioBackend` interface, `patches.go:54`) into a `main`-wired adapter | **de-taints `patches → controls → controls/pages → web → cmd/polyclav`, ~150 more Go tests build on macOS** | pure refactor |
| **E** | Add a virtual-MIDI injection test (§4.4) | MIDI-in parse/dispatch proven headlessly | small |

**Why D matters:** today `internal/patches/patches.go:65-74` is the only leaf pulling `internal/audio` (hence PipeWire) into the pure-Go graph, tainting ~150 tests (controls 53, web 38+21+4, patches 16, pages 12, main 9). Once de-tainted, ~120 Go tests already build on macOS today (config, state, velocity, osc, launchkey, player, smf, bootstrap, midi/picker — all via fake/interface seams), and D adds ~150 more.

**Net with A+B+C+D+E:** a Mac with no audio/MIDI hardware can prove ~all 147 Rust tests, ~270 Go tests, the full synth+DSP render via golden WAV, and the MIDI-in path — leaving only literal speaker/Launchkey/XR18 behavior for the bench.

---

## 5. The hardware wall

Some things **cannot** be verified without a physical Mac plus the physical devices in the same room. No cloud/CI Mac closes these, because cloud Macs are headless boxes with no path to your USB devices: [AWS EC2 Mac has no Wi-Fi/Bluetooth and no USB attach](https://aws.amazon.com/ec2/instance-types/mac/faqs/) (the disk is EBS over Thunderbolt via Nitro), and [Apple's Virtualization.framework has no general USB passthrough](https://developer.apple.com/forums/thread/741785). USB-over-IP (VirtualHere, Citrix VDA, DCV) tunnels a device plugged into *your local machine* and is flaky/latency-prone for real-time MIDI/audio anyway — and still requires you to physically own the device.

| Cannot verify remotely | Why | How to close it |
|---|---|---|
| **Audible correctness / real speaker & round-trip latency** | No real output device; CoreAudio buffer/latency behavior isn't reproduced by null/virtual devices | Physical Mac + monitors |
| **Launchkey MK4 DAW-mode handshake, pads, LCD** | USB class-compliant MIDI: host sends `9F 0C 7F` to enter DAW mode (`9F 0C 00` to exit); the 128×64 screen is a fixed **1216-byte SysEx bitmap** (`F0 00 20 29 02 14 …`); pad RGB/feature CCs on DAW ports (query ch.8, reply ch.7). Fully scriptable — but only against the real device on a real Mac's CoreMIDI stack ([Programmer's ref](https://fael-downloads-prod.focusrite.com/customer/prod/downloads/launchkey_mk4_programmer_s_reference_guide_v2_en.pdf), [DAW mode](https://userguides.novationmusic.com/hc/en-gb/articles/23754923378066-Launchkey-Programmer-s-DAW-mode)) | Physical Mac + physical Launchkey. SysEx *encoding* is already unit-tested against driver fakes (`driver_test.go`); the *wire* round-trip is not |
| **CoreMIDI port-name heuristics for the MK4** | `PickPortName` substrings tuned to ALSA names; CoreMIDI names differ | Read real enumeration once on a physical Mac, then re-tune |
| **Realtime CoreAudio callback path** | The macOS analog of `process_audio` | Physical Mac (offline render covers the DSP math, not the RT device path) |

**The one exception: the XR18 mixer.** It speaks **OSC over UDP on the network**, not USB. Any Mac that can route UDP to it — including a **cloud Mac reached via a WireGuard/VPN tunnel to your LAN**, or a borrowed Mac on the same switch — can drive and verify it (mute/param/meter behavior). The reconciler *logic* is already tested against a fake `Sender` (`reconciler_test.go`); the real mixer round-trip is verifiable off-site. (Open question, not confirmed by the bundle: whether Scaleway/EC2-Mac allow the inbound VPN needed to reach your home LAN's OSC port — plausible since both give full network control, but untested.)

**Options to close the USB/audio wall:**

1. **Buy/borrow a used Apple-Silicon Mac mini (M1/M2)** for one-time bring-up — cheapest real fix; a weekend covers the Launchkey handshake + latency verification and removes every caveat.
2. **Ship the rig to a Mac-owning beta tester** who runs a scripted verification harness and returns logs/recordings — good for repeat regression, slow feedback.
3. **One-time bring-up trip**, then rely on CI + VPN-reachable XR18 for ongoing verification once the USB/audio paths are proven.
4. **Cloud Mac (Scaleway ~€0.22/hr M4, [pricing](https://www.scaleway.com/en/pricing/apple-silicon/); AWS EC2 Mac ~$0.65–0.88/hr with a 24h host-allocation floor; [Cirrus $150/mo unlimited-minutes GH runner](https://cirrus-runners.app/pricing/))** for everything *except* USB and real speakers — builds, signing/notarization, and VPN-OSC XR18 work. All Apple-Silicon cloud Macs share a **24-hour minimum billing floor** (Apple SLA).

---

## 6. Cross-compilation verdict

**Don't cross-compile Linux→macOS as the primary path; build natively on CI.** For a cgo binary that links CoreAudio/CoreMIDI frameworks, [`zig cc` alone can't target Darwin](https://github.com/wailsapp/wails/discussions/4267) (Apple doesn't distribute the SDK frameworks), so you'd need [osxcross](https://github.com/tpoechtrager/osxcross/blob/master/README.md) with an extracted Xcode SDK. That path is:

- **Brittle** — real reproduced failures cross-linking framework-using cgo include missing `objc`/`resolv` and `Undefined symbols … ___isPlatformVersionAtLeast`; no source shows CoreAudio/CoreMIDI working, so you'd be pioneering.
- **Legally gray** — the Apple SDK/Xcode agreement restricts use to Apple-branded hardware; osxcross itself warns to read the license first. Fine for private tinkering, not for public OSS CI or a release pipeline.
- **Incomplete anyway** — code-signing and notarization must run *on macOS*, so you need a Mac at the end regardless.

Native-on-CI (free Apple-Silicon runners) gives a real, signable, testable binary for $0 on a public repo. Cross-compiling buys a possibly-broken unsigned binary plus a licensing headache to avoid a free runner. Keep osxcross at most as an optional local smoke-compile for whoever enjoys maintaining it.

---

## 7. Recommended roadmap

Ordered, phased. Each phase is independently valuable — ship them in order.

| Phase | Do | Proves | Cost |
|---|---|---|---|
| **0. Green macOS build** | P0 (target-gate pipewire) + P1 (extract `render_block`, `AudioBackend` trait) + P2 (cpal CoreAudio backend) + P3 (Go cgo `darwin` split, drop `-ldl`, `libsfizz.dylib` branch). Add a pinned `macos-15` CI job. | audio-core + the Go binary **compile and link on macOS**; the FFI contract is unchanged. | Dev time; free public-repo CI. |
| **1. Rust DSP/synth tests** | Runs automatically once P0 lands (no test changes). | **All 147 Rust `#[test]`s** (RMS/brightness/golden-sample) pass on macOS. | ~free (folds into phase 0). |
| **2. Offline render tests** | Recommendations **B + C**: shared `render_block`, offline renderer + WAV dump, `polyclav render` CLI wired to the player `Sink`. | The **whole synth+DSP+audition chain** end-to-end, deterministically, no device — immune to the 30% Null-device flake. | Small. |
| **3. Go coverage unlock** | Recommendation **D**: move `realAudioBackend` out of `internal/patches`. | **~270 Go tests** build/run on macOS (adds controls/web/patches/pages/main). | Pure refactor. |
| **4. Virtual MIDI tests** | Recommendation **E**: separate-process CoreMIDI virtual-port injection. | **MIDI-in parse/dispatch** path headlessly (proven viable by midir/jazz-soft on hosted runners). | Small. |
| **5. Real-hardware bring-up** | One-time physical Mac + Launchkey + XR18 (buy/borrow/beta-tester). Optionally VPN-OSC the XR18 from a cloud Mac earlier. | Audible correctness, real latency, **Launchkey DAW-mode wire behavior**, CoreMIDI port-name re-tuning, real CoreAudio RT path. | The only real $ / logistics cost; the hard wall. |

After phase 4, CI on a hardware-less Mac proves essentially everything except literal speaker/Launchkey/XR18-USB behavior. Phase 5 is a bounded, one-time human task — not a recurring blocker.

---

## 8. Open questions / risks

- **CoreAudio sample-rate mismatch.** The engine forces 48 kHz with no runtime SR negotiation, but CoreAudio devices default to 44.1 kHz; `dsp/compressor.rs` and `dsp/reverb.rs` (Freeverb) hardcode 48 k tunings. The CoreAudio backend must either force a 48 k device rate or the DSP must be SR-parameterized. Decide before P2.
- **Interleaving.** CoreAudio hands back non-interleaved buffers while backends internally interleave — the CoreAudio backend will de-interleave what the backends just interleaved. Works, but argues for eventually pushing the L/R seam down.
- **Which Homebrew `lilv` version, and does it split zix?** The verified finding says brew lilv 0.28 links zix separately (`-lzix-0`, `!portable` path), contradicting a stale "0.24 vendors zix" research note. Re-confirm against the actual installed version at build time; the `-tags portable` choice hinges on it. (Also empirically confirm `pkg-config --cflags lilv-0` resolves without `PKG_CONFIG_PATH` — medium-confidence inference.)
- **LV2 on macOS: keep or drop?** Hosting compiles but plugins are scarce. Dropping it removes the entire lilv/serd/sord/sratom/zix Homebrew chain and simplifies the cgo link. AU hosting (biggest coverage) has no mature Rust host — decide if CLAP-only is acceptable for v1.
- **RtMidi same-process virtual ports.** Unclear whether two `rtmididrv.New()` instances in one process suffice for the injection test, or a separate OS process is strictly required (RtMidi #208 self-filtering suggests separate processes are safest).
- **VPN reach to the XR18.** Whether Scaleway/EC2-Mac permit the inbound WireGuard tunnel to reach your home-LAN OSC/UDP port is plausible but unverified in the bundle.
- **Null Audio Device flakiness affects any device-touching test.** Even non-audio jobs that incidentally open the default device will hit the ~30% init failure ([#13668](https://github.com/actions/runner-images/issues/13668)). Guard/skip device-dependent paths; rely on offline render.
- **`macos-latest` ambiguity through mid-July 2026.** Pin `macos-15` (or `macos-26` once you've validated its Xcode/Node/OpenSSL/Ruby bumps).
- **Disk pressure.** 14 GB SSD on standard arm64 is tight for Xcode + Rust + Go caches; monitor for disk-full failures.
- **Cost/concurrency at scale.** macOS bills at 10x included minutes with only 5 concurrent macOS jobs (Free/Pro/Team) — a large matrix serializes. Keep the repo public (free standard runners) and keep the macOS job lean; consider a self-hosted Mac mini or Cirrus if volume grows.