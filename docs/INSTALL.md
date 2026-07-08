# polyclav — Install

A from-zero install guide for someone who just cloned this repo. For the
day-to-day "configure and play" side, see `USER_GUIDE.md` instead.

## Platform

**Linux only.** polyclav talks to **PipeWire** for audio and ALSA-seq for
MIDI; both are mandatory. PulseAudio-only systems and macOS/Windows are
not supported.

## Toolchains

- **Go** ≥ 1.23 (the project targets 1.26; anything ≥ 1.23 should
  compile).
- **Rust** stable (the `audio-core` crate has no nightly features).
- **mise** (https://mise.jdx.dev) is recommended for pinning both —
  `mise install` reads `mise.toml` and sets up the right versions. You
  can also manage Go and Rust by hand; just match the pins.
- **just** (https://github.com/casey/just) for the task runner.
- **overmind** (https://github.com/DarthSim/overmind) for the
  `Procfile`-driven supervisor — optional, you can `./bin/polyclav` by
  hand instead.

## System libraries

polyclav links against PipeWire (audio), ALSA (MIDI in via rtmidi),
sfizz (SFZ playback), liblo (OSC), and the LV2 + CLAP plugin stacks. The
LV2 and CLAP libraries are **not optional** — LV2 and CLAP plugin
hosting is shipped, so `audio-core` fails to build without them. Headers
+ libs for everything below must be present at build time.

| Component | Used for | Nix attr | Debian/Ubuntu | Fedora/RHEL | Arch |
|---|---|---|---|---|---|
| PipeWire dev | audio backend (Rust `pipewire` crate) | `pipewire.dev` | `libpipewire-0.3-dev` | `pipewire-devel` | `pipewire` |
| ALSA lib dev | MIDI (rtmidi cgo) | `alsa-lib.dev` | `libasound2-dev` | `alsa-lib-devel` | `alsa-lib` |
| sfizz | SFZ playback (C++ via cgo) | `sfizz` | `libsfizz-dev` (third-party) | `sfizz-devel` | AUR `sfizz` |
| liblo | OSC client (XR18 mixer) | `liblo` | `liblo-dev` | `liblo-devel` | `liblo` |
| lilv | LV2 host (`livi` crate) | `lilv` | `liblilv-dev` | `lilv-devel` | `lilv` |
| lv2 | LV2 spec headers | `lv2` | `lv2-dev` | `lv2-devel` | `lv2` |
| serd | LV2 RDF (lilv dep) | `serd` | `libserd-dev` | `serd-devel` | `serd` |
| sord | LV2 RDF store (lilv dep) | `sord` | `libsord-dev` | `sord-devel` | `sord` |
| sratom | LV2 atom serialise (lilv dep) | `sratom` | `libsratom-dev` | `sratom-devel` | `sratom` |
| CLAP headers | CLAP host (`clack-host` crate) | `clap` | `clap` (or vendored headers) | `clap-devel` | AUR `clap` |
| Clang/LLVM | bindgen for `pipewire-sys` | `clang` | `libclang-dev` | `clang-devel` | `clang` |
| pkg-config | locating .pc files | `pkg-config` | `pkg-config` | `pkgconf-pkg-config` | `pkgconf` |

The lilv stack also pulls in **zix** at link time (nixpkgs `zix` attr;
most distros bundle it inside lilv). CLAP is a header-only spec — its
host (`clack-host`) is pure-Rust and loads `.clap` libraries at runtime.

## .mise.local.toml caveat

The committed `mise.toml` is intentionally portable — it pins only Go,
Rust, and `just`. The **nix-store-specific build paths** live in
`.mise.local.toml`, which is **gitignored** (auto-provided inside the
yolo jail; nix users get the equivalent from `nix develop`). Because
that file never lands in git, the hard-won details are documented here:

- `LIBCLANG_PATH` — bindgen needs libclang for `pipewire-sys`.
- `CPLUS_INCLUDE_PATH` — rtmidi's cgo hardcodes `<alsa/asoundlib.h>`.
  glibc-dev must **not** go here: it would prepend before gcc's C++
  system headers and break the `#include_next <stdlib.h>` inside
  `<cstdlib>`.
- `C_INCLUDE_PATH` — `libspa-sys` bindgen pulls in glibc system headers.
- `CGO_CFLAGS` / `CGO_CXXFLAGS` — glibc-dev added via **`-idirafter`** so
  it sorts *after* gcc's own system include dirs (required for the
  `#include_next <stdlib.h>` resolution above).
- `CGO_LDFLAGS` — `-L`/`-rpath` for sfizz, asound, and the lilv stack
  (their `.so` files live outside the linker's default search path under
  nix). The Rust staticlib doesn't propagate cargo's link-lib directives
  across the staticlib boundary, so cgo links the lilv stack explicitly.
- `PKG_CONFIG_PATH` — pipewire / sfizz / alsa-lib / lilv / lv2 / serd /
  sord / sratom `.pc` directories.

Two non-obvious workarounds also live there:

- **sfizz `.pc` typo.** The nixpkgs `sfizz.pc` carries a `-llibsfizz`
  typo, so we hand-link `-lsfizz` in cgo + `build.rs` to dodge it.
- **glibc include ordering.** The `-idirafter` placement above is load
  bearing — prepending glibc-dev breaks `<cstdlib>`.

**On any other system**, replace these with whatever your package
manager provides — or drop them entirely if your distro puts the
libraries in standard locations (`/usr/lib`, `/usr/include`,
`/usr/lib/pkgconfig`), which is the common case.

Quick test that your env is right:

```sh
pkg-config --cflags --libs libpipewire-0.3 alsa sfizz lilv-0 lv2
```

If that prints flags without error, the build will work.

## Build

```sh
mise install            # optional but recommended
eval "$(mise env)"      # only if you use mise
just build              # cargo build --release + go build
just check              # the universal gate (build + clippy + vet + tests)
```

`just check` runs a trailing `go build ./...` on purpose: `go test` only
links cgo for packages that have test files, so without it a missing
`-lsfizz`, `-lasound`, or `-llilv` slips past.

## Soundfonts

**polyclav ships no soundfonts** (size + license). The example config
references 8 free starter packs (~500 MB total) plus one pure-Rust
synth that needs no download.

### Quickest: `polyclav bootstrap`

The daemon's built-in `bootstrap` subcommand downloads every pack the
example config expects, into `~/.local/share/polyclav/soundfonts/`, and
verifies the on-disk layout matches `polyclav.example.toml` exactly:

```sh
polyclav bootstrap                # interactive: prints licenses, prompts once
polyclav bootstrap -y             # non-interactive bulk accept
polyclav bootstrap --dest <path>  # different destination
```

Re-running is safe (existing files are skipped). A `LICENSES.txt` file
is written to the destination directory for redistribution audits. The
full URL/license list lives in `internal/bootstrap/spec.go`.

Three FreePats packs are `.7z` archives, so bootstrap needs a 7-Zip CLI
on `PATH`: Linux, install your distro's p7zip package (e.g.
`apt install p7zip-full`); macOS, `brew install sevenzip` — note this
installs the binary as `7zz`, not `7z` (bootstrap checks both names). If
neither is found, bootstrap reports which packages to install rather
than a raw "executable not found" error.

On macOS, bootstrap also installs SFZ (`.sfz`) support automatically:
sfztools' own official release is x86_64-only and can't be used by a
native arm64 process, so bootstrap downloads polyclav's own arm64 build
(`.github/workflows/build-sfizz-macos.yml`) to
`~/.local/share/polyclav/lib/libsfizz.dylib` instead — no Homebrew
formula exists for sfizz, and none is needed. See
`internal/bootstrap/sfizz.go` and `docs/MACOS_PORT.md`.

### Manual: download by hand

Drop soundfonts anywhere — the example config uses
`~/.local/share/polyclav/soundfonts/` and polyclav expands a leading `~`.
The authoritative pack list, with URLs and licenses, is
`internal/bootstrap/spec.go`. `just fetch-soundfont` downloads the small
FreePats acoustic grand into `./soundfonts/` if you just want one to test
with — but `polyclav bootstrap` is the maintained path.

## Hardware

polyclav's developed-against rig is a **Novation Launchkey 61 MK4** plus a
**Behringer XR18** (USB audio class-compliant; OSC over the network).
You don't need either to use polyclav:

- **MIDI keyboard.** Any class-compliant MIDI keyboard works for the
  basic synth path. The `[midi].port_match` substring picks the input.
- **Audio interface.** Anything PipeWire enumerates. The default sink
  is fine — no XR18-specific routing is required.
- **Launchkey-specific code paths** (DAW driver, pad colors, screen,
  per-patch knob state) light up only if the Launchkey is detected.
  Without it they stay idle; the audio + MIDI path still works.

For low-latency on an XR18, install a WirePlumber rule under
`~/.config/wireplumber/wireplumber.conf.d/`, pinning:

```
api.alsa.period-size=128
api.alsa.period-num=3
api.alsa.headroom=0
```

That gets ~8 ms round-trip at 48 kHz. See PipeWire/WirePlumber upstream
docs for the full rule syntax.

## First run

The daemon enforces a "functioning config or refuse" startup rule:
either every `[[patches]]` entry's dependencies resolve, or polyclav
prints a formatted error and exits 1. There's no silent fallback to
sine on missing soundfonts.

Two-step first run:

```sh
polyclav                          # writes ~/.config/polyclav/polyclav.toml from
                                  # the embedded default, then refuses to
                                  # start with a list of missing soundfonts
polyclav bootstrap                # downloads the ~500 MB of free packs
                                  # the default config references
polyclav                          # now starts cleanly (or: overmind start -D)
```

If you want to skip the download (e.g. you'll wire your own soundfonts),
edit `~/.config/polyclav/polyclav.toml` and trim or replace the `[[patches]]`
entries. The pure-Rust `moog-bass-native` entry validates with zero
dependencies — a config with only that patch will start without
bootstrap.

Logs tee to `/tmp/polyclav.log` when run under overmind. On startup
failure the error goes to stderr (multi-line, human-readable); routine
operation goes to stdout as structured slog lines.

## Where to go next

- `USER_GUIDE.md` — full config schema, every key explained.
- `AGENTS.md` — developer / agent workflow, current milestone state.
- `ROADMAP.md` — what's shipped and what's planned next.
- `HARDWARE_TESTS.md` — hardware verification checklist.
