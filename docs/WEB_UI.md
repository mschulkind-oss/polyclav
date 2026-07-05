# Design: Web Settings Interface

> **Status:** phases A+B shipped 2026-07-05 — the REST + SSE API and the
> embedded interim dashboard (`[web]` in `polyclav.toml`, off by default,
> `127.0.0.1:8666`; see `docs/USER_GUIDE.md` "Web dashboard"). Still to
> come: the Next.js app (phase C+), config editing from the browser, and
> the velocity editor page. Companion doc: `docs/VELOCITY_CURVES.md`
> (the velocity editor is a planned page of this UI). Tech choices follow
> the portfolio standards (Next.js frontend, pnpm + biome + tsc,
> hivemind for dev processes).

## Goal

A browser page served by the polyclav daemon itself that:

1. **Shows** what's going on — connected devices, current patch, live knob
   values, mastering settings, config file contents.
2. **Tweaks** the things that are already live-settable — patch selection,
   volume/reverb/comp, mastering comp + limiter ceiling, native-synth
   cutoff — without touching the Launchkey.
3. Eventually **edits config** — patches, OSC bindings, velocity curves —
   with validation, from the browser.

Non-goals: remote multi-user access, auth beyond trusted-LAN, a DAW. The
UI is an instrument front panel, not an admin console.

## Why this matters

Today the **only** UI is the Launchkey (screen + pads + knobs). Without
one connected there is no way to see or change anything at runtime — no
patch switching, no volume, nothing (see `docs/CONFIGURABILITY.md` §1.4,
"feedback-less degradation"). A web page is the natural generic front
panel: every laptop and phone on the LAN has a browser, and it sidesteps
the control-surface abstraction problem for *output* (state display)
entirely.

## Current state (what exists to build on)

- **No HTTP server** anywhere in the daemon today (only the bootstrap
  downloader uses `net/http` as a client).
- **Live-settable audio params** already exist as thread-safe setters in
  `internal/audio/audio.go`: `SetMasterVolume`, `SetReverb`,
  `SetCompressor`, `SetPatchGain`, `SetMasteringCompressor`,
  `SetLimiterCeilingDB`, `SetNativeCutoffHz` — all backed by atomics read
  per audio block. A web UI can call these directly; no audio-thread work
  needed.
- **Patch switching** is `registry.Select(name)` / `SelectIndex(i)`
  (`internal/patches/patches.go:120,139`) — already safe to call from any
  goroutine (backends swap via the reload queue).
- **Persistence** exists: `internal/state.Store` (debounced, atomic-write
  `state.toml`) holds per-patch knob values and the current patch. Web
  tweaks should flow through the same store so browser and Launchkey edits
  are indistinguishable.
- **Device presence** is already queryable: `sup.LaunchkeyState()` and
  `sup.XR18State()` (`internal/supervisor/supervisor.go:40,43`).

**The one real gap:** nothing *notifies* on change. When a Launchkey knob
turns, the new value goes into the atomics and the state store, but no
event fires that a web page could subscribe to. The daemon needs a small
change-notification hub.

## Architecture

```
┌────────────────────────── polyclav daemon (Go) ──────────────────────────┐
│                                                                          │
│  Launchkey ─▶ onDAWEvent ─┐                                              │
│                           ├─▶ controls layer ─▶ audio atomics + state.toml│
│  Browser ──▶ REST PATCH ──┘         │                                    │
│                                     ▼                                    │
│  Browser ◀── SSE /api/events ◀── change hub (pub/sub)                    │
│  Browser ◀── static files ◀── go:embed web/out (Next.js static export)   │
└──────────────────────────────────────────────────────────────────────────┘
```

### Backend: in the Go daemon, stdlib `net/http`

The daemon is the only process holding the registry, the audio atomics,
and the state store — so the API **must** live in it. A sidecar backend
(FastAPI etc.) would need an IPC layer into the daemon that doesn't exist;
rejected. Go stdlib `net/http` + `encoding/json` is enough; no framework
dependency.

### Frontend: Next.js static export, embedded in the binary

Per the portfolio tech standards: **Next.js** (with `output: "export"`),
**pnpm**, **biome** for format/lint, **tsc** for type-checking, node via
**mise**. The static export (`web/out/`) is embedded with `go:embed` at
build time, so the released `polyclav` binary stays fully self-contained —
no node at runtime, no separate deploy.

Dev loop: `Procfile.dev` run with **hivemind** — the daemon on
`127.0.0.1:8666` plus `next dev` on `:3000` proxying `/api/*` to the
daemon (Next `rewrites`). `just web-dev`, `just web-build` targets;
`just check` grows a `web` leg (biome ci + tsc + vitest) that only runs
when `web/` exists/changed.

### Transport: REST for commands, SSE for state

Server-Sent Events over WebSocket: state flows one way (daemon → browser),
commands are individual HTTP calls, SSE is stdlib-implementable and
auto-reconnects in every browser for free.

## API sketch

| Method & path | Purpose |
|---|---|
| `GET /api/status` | One-shot snapshot: daemon version, launchkey/xr18 reconciler states, current patch, knob values, mastering params, sfizz availability |
| `GET /api/events` | SSE stream of the same snapshot's deltas (`patch-changed`, `knob-changed`, `device-changed`, …) |
| `GET /api/patches` | Patch list (name, display, type, pad_color, gain_db, slot index) |
| `POST /api/patches/{name}/select` | Switch patch (same path as a pad press: select → restore knobs → persist) |
| `PATCH /api/params` | Body `{"volume": 0.8}` / `reverb` / `compressor` / `native_cutoff_hz` — per-current-patch, persisted via the state store |
| `PATCH /api/mastering` | `comp_amount`, `limiter_ceiling_db` (runtime-only until config write lands) |
| `GET /api/config` | The loaded config, serialized (paths expanded, host redacted if desired) |
| `PUT /api/config` *(phase C)* | Validated TOML write-back; response says whether a restart is needed |
| `GET/PUT /api/velocity` *(phase C)* | Velocity curve — see `docs/VELOCITY_CURVES.md` |

## The real work: a controls layer

The daemon's param-changing logic currently lives in closures inside
`cmd/polyclav/main.go` (`onDAWEvent`, ~lines 235–318): knob delta →
clamp → `audio.Set*` → `stateStore.UpdatePatchKnob` → Launchkey screen
update. The web UI needs the **same** sequence minus the screen part, and
both sources need to publish to the change hub. So the prerequisite
refactor is:

1. Extract a `controls` package: `SetVolume(v)`, `SetReverb(v)`,
   `SetCompressor(v)`, `SetCutoffHz(hz)`, `SelectPatch(name)` — each doing
   atomics + state store + `hub.Publish(event)`.
2. `onDAWEvent` and the HTTP handlers both call it; the Launchkey screen
   update becomes a hub *subscriber* like the browser.
3. The hub itself: ~50 lines — mutex, subscriber channels,
   non-blocking fan-out (drop-oldest per slow subscriber).

This refactor is also step one of the control-surface abstraction in
`docs/CONFIGURABILITY.md` §Tier 3 — the two designs share it, and it
should be built once.

## Config vs. state: two write paths, kept distinct

- **Live params** (knobs, patch selection, mastering, velocity curve
  tweaks): write through the existing `state.Store` — debounced, atomic,
  already the source of truth for restore-on-boot.
- **Structural config** (`[[patches]]`, OSC bindings, MIDI port match):
  phase C only. `PUT /api/config` validates with the existing
  `config.Load`+`Validate` path against a temp file, then atomically
  replaces `polyclav.toml`. Hot-reload of structural config is **out of
  scope** — the UI shows a "restart to apply" banner. (Live patch-list
  reload is a possible later increment; it touches the registry, pads, and
  state keys.)

## Security model

- `[web]` config block: `enabled = false` **by default** (same opt-in
  philosophy as `osc.xr18.host`), `listen = "127.0.0.1:8666"`.
- **No auth, ever-for-now (decided):** localhost binding is the security
  boundary. Setting `listen = "0.0.0.0:8666"` is the documented LAN
  opt-in — the user is explicitly allowed to make that call for their own
  network, and the docs say what it means. No tokens, no TLS, no accounts.
- polyclav sits on a music-room LAN next to a mixer that accepts
  unauthenticated UDP; matching that threat model is honest.

## Phasing

| Phase | Ships | Depends on |
|---|---|---|
| **A — dashboard** | `[web]` config, HTTP server, embedded static page, `GET /api/status` + SSE; read-only view of devices/patch/knobs | change hub + minimal controls extraction |
| **B — control** | patch select, `PATCH /api/params`, `PATCH /api/mastering`; sliders and patch grid in the UI | phase A |
| **C — editing** | config viewer/editor with validation + restart banner; velocity curve editor (`docs/VELOCITY_CURVES.md`) | phase B, velocity doc's Go work |

Phase A alone is already worth shipping: it's the first way to see
polyclav's state without a Launchkey attached.

## Decisions (resolved 2026-07-04)

1. **Auth: none.** Bind `127.0.0.1` by default; opening the listen
   address to the LAN is the user's informed decision. No token layer.
2. **Port: `8666` stands** as the default.
3. **Config redaction: none.** `GET /api/config` returns the config
   verbatim, XR18 host IP included.
4. **Layout: laptop-first.** Design for a laptop screen; keep it
   responsive enough that a phone works, but the phone is not the
   design target.
5. **Check gating: run web checks (biome/tsc/vitest) only when `web/`
   changed** — with the expectation that integration tests will
   eventually exercise daemon + UI together and force full runs anyway.
   Structure the Justfile so both modes are easy.

<!-- changelog -->
- [051c3f37] Dropped the auth question: decided no auth — localhost bind is the boundary, LAN exposure is the user's call; security section updated to match.
- [78592d7d] Locked port 8666 as the default.
- [054af7c7] Decided: `GET /api/config` returns config verbatim, no redaction.
- [63e17f52] Decided laptop-first layout; phone stays workable but is not the design target.
- [c268d4b0] Decided change-gated web checks, noting integration tests will later force full runs; converted Open Questions to a resolved-decisions section.
