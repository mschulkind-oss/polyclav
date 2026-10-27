# Contributing to polyclav

Patches welcome. [`AGENTS.md`](AGENTS.md) covers the workflow, code layout, and
current milestones. Run `just check` before sending anything — it gates on
`cargo build --release`, `cargo clippy -D warnings`, `go vet`, `cargo test`,
`go test`, and `go build ./...`.

## Licence terms for contributions

**Contributions are accepted under the Apache License, Version 2.0.**

That is already Apache 2.0's default: §5 says that unless you state otherwise,
anything you intentionally submit for inclusion is licensed under the project's
licence. Stating it here removes the ambiguity.

You keep your copyright. There is no CLA, no copyright assignment, and neither
is needed: Apache 2.0 already gives the project everything required to ship
your contribution in every build polyclav produces, including a future
proprietary one, because Apache 2.0 §4 permits distributing derivative works
under other terms provided the Apache notices are kept.

### Please do not submit GPL-3.0-only code

polyclav is dual-licensed (Apache-2.0 OR GPL-3.0-only) so that the optional
ASIO-enabled Windows build can be GPL-3.0 while every other build stays
Apache-2.0. Code that can only be licensed under GPL-3.0 can go into the
GPL-3.0 build but never into the Apache-2.0 ones, which would split the
project. If you want to use a GPL-3.0-only dependency, please open an issue
first so we can decide deliberately rather than discover it at merge time.

Inbound Apache 2.0 is compatible with all three futures, so this is the only
restriction worth stating: Apache-2.0 code may be taken into a GPL-3.0 work
(the permitted direction), and Apache 2.0's own terms allow the proprietary
case.

## Sign-off

Commits should carry a `Signed-off-by` trailer, which `git commit -s` adds for
you:

```
Signed-off-by: Your Name <you@example.com>
```

That is the [Developer Certificate of Origin](https://developercertificate.org),
a one-line statement that you have the right to submit the code under the
project's licence. It is not a CLA and grants the project no additional rights.
It exists so that a patch cannot accidentally carry code you do not own — an
employer's work, or a copy from a differently licensed project.

## Bug reports

Please include `polyclav --version`, the contents of your `config.toml` (redact
paths if you like), and the relevant slice of `/tmp/polyclav.log` (overmind
tees there by default).
