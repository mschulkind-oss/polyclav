# Vendored `asio-sys` 0.3.0 (one-hunk patch)

- **Upstream:** <https://crates.io/crates/asio-sys/0.3.0>
- **Repository:** <https://github.com/RustAudio/cpal/> (`asio-sys/` subdirectory)
- **Licence:** Apache-2.0, as declared in `Cargo.toml`. The repository's root
  [`LICENSE`](../../../LICENSE) is the copy of the licence that covers this
  vendored tree; upstream does not ship a separate `LICENSE` file.
- **Patched for:** polyclav's optional `asio` cargo feature, wired up through
  `[patch.crates-io]` in the repository root `Cargo.toml`.

## The change

One hunk, in `build.rs`: the `.allowlist_type("AsioDrivers")` line is removed,
so bindgen does not bind that C++ class.

## Why

`AsioDrivers` derives from `AsioDriverList`, and the Itanium C++ ABI places the
derived class's first member (`connID`) in the **tail padding** of the base
class — offset 12, inside the base's 12..16 pad. `#[repr(C)]` never reuses base
tail padding, so the struct bindgen emits lays `connID` out at 16. bindgen
computes the expected offset with clang (12) and then emits a struct Rust lays
out differently, so its own generated assertion
(`offset_of!(AsioDrivers, connID) - 12`) fails to compile. Passing `--target` to
clang does not help — tail-padding reuse is an ABI property, not a target
property — and `asio-sys` 0.4.0's `build.rs` is byte-identical.

Disabling bindgen's layout tests wholesale would have been the wrong fix: a
struct whose layout is genuinely wrong would corrupt memory instead of failing
to compile. The type is simply not bound, because nothing on the Rust side uses
it:

- every `AsioDrivers` use in this crate is C++ — `asio-link/helpers.cpp`
  constructs one on the stack in `get_driver_names`, and
  `remove_current_driver` dereferences the SDK's global `asioDrivers`;
- neither `src/` here nor `cpal`'s ASIO backend names the type.

Every other ASIO type (`ASIOCallbacks`, `ASIOBufferInfo`, `ASIOChannelInfo`, …)
keeps its generated layout assertion, so the structs that actually cross the
Rust↔C boundary are still checked.

Measured offsets and the full reasoning: [`docs/WINDOWS_PORT.md` §3.1](../../../docs/WINDOWS_PORT.md).

## Refreshing this copy

1. Download the new `asio-sys` version from crates.io and unpack it here,
   replacing this tree but keeping this file.
2. Re-apply the one-hunk removal of the `AsioDrivers` allowlist entry.
3. Confirm `cargo check --target x86_64-pc-windows-gnu --features asio` passes
   and that `AsioDrivers` still has no Rust-side users.
4. If upstream has fixed the layout (or stopped binding the type), drop this
   vendored copy and the `[patch.crates-io]` entry instead.
