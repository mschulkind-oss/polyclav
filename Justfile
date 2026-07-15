default:
    @just --list

setup:
    cargo fetch
    go mod download

build-rust:
    cargo build --release --manifest-path audio-core/Cargo.toml

# The dev auto-reloader (.air.toml) uses this so a Go/Rust save never waits
# on a Next.js export.
# Backend binary only — embeds internal/web/static/app as-is.
build-bin: build-rust
    mkdir -p bin
    go build -o bin/polyclav ./cmd/polyclav

# Without pnpm (e.g. a no-Node release box) falls back to the committed
# export with a warning instead of failing.
# Refresh the embedded web export from the web/ sources on disk.
web-sync:
    @if command -v pnpm >/dev/null 2>&1; then \
        if [ ! -d web/node_modules ]; then (cd web && pnpm install --frozen-lockfile); fi; \
        just web-build; \
    else \
        echo "warning: pnpm not found — using the committed web export (may be stale)"; \
    fi

# Everything-fresh build: web export rebuilt from disk, then the binary embeds it.
build: web-sync build-bin

format:
    cargo fmt --manifest-path audio-core/Cargo.toml
    go fmt ./...

lint: build-rust
    cargo clippy --manifest-path audio-core/Cargo.toml --all-targets -- -D warnings
    go vet ./...

lint-ci: build-rust
    cargo fmt --manifest-path audio-core/Cargo.toml --check
    cargo clippy --manifest-path audio-core/Cargo.toml --all-targets -- -D warnings
    # gofmt is a filesystem walker (ignores .gitignore), so scope it to the
    # project's Go source dirs — avoids descending into .yolo/, .gocache/,
    # vendored module caches with intentionally-malformed testdata, etc.
    # Print the offending files on failure (don't swallow them in $()).
    @bad="$(gofmt -l cmd internal)"; if [ -n "$bad" ]; then echo "gofmt needs formatting:"; echo "$bad"; exit 1; fi
    go vet ./...

test *args: build-rust
    cargo test --manifest-path audio-core/Cargo.toml {{args}}
    go test ./... {{args}}

# Local dev gate: format → lint → test → build everything.
check: format lint test
    # `go build ./...` catches cgo link errors `go test` misses: test only
    # links packages with test files, so missing system libs (-lsfizz,
    # -lasound) slip past the gate otherwise.
    go build ./...

check-ci: lint-ci test
    go build ./...

done: check-ci
    @git status --porcelain | tee /dev/stderr | (read -r line && echo "working tree dirty — commit or revert before calling done" && exit 1) || true

run *args: build
    ./bin/polyclav {{args}}

#   daemon — air rebuilds (just build-bin) + restarts on go/rs/toml/h saves
#   web    — next dev with HMR on :3000, /api/* proxied to the daemon :8666
# Browse http://localhost:3000/app/ (mockup playground: /app/mockup/).
# Auto-reloading dev loop for both halves (hivemind runs Procfile.dev).
dev:
    hivemind Procfile.dev

# Build and install both binaries to PREFIX/bin (default ~/.local/bin).
# Override the location with `PREFIX=/usr/local just install`.
PREFIX := env_var_or_default("PREFIX", env_var("HOME") / ".local")

install: build
    mkdir -p {{PREFIX}}/bin
    install -m 0755 bin/polyclav {{PREFIX}}/bin/polyclav
    # polyclav-components isn't part of `build`, so build it here too.
    go build -o bin/polyclav-components ./cmd/polyclav-components
    install -m 0755 bin/polyclav-components {{PREFIX}}/bin/polyclav-components
    @echo "installed polyclav + polyclav-components → {{PREFIX}}/bin"
    @echo "(make sure {{PREFIX}}/bin is on your PATH)"

# Download all soundfont packs into ~/.local/share/polyclav/soundfonts/.
bootstrap *args: build
    # Idempotent — re-running skips packs already on disk. Pass `-y` to
    # accept licenses non-interactively, or `--dest <path>` to override.
    ./bin/polyclav bootstrap {{args}}

clean:
    cargo clean --manifest-path audio-core/Cargo.toml
    rm -rf bin .gocache

# Drop a default soundfont into soundfonts/ for dev. FreePats is small and
# public-domain. URL is configurable. Override SOUNDFONT_URL / SOUNDFONT_FILE
# to grab something bigger (e.g. Salamander Grand Piano SF2).
SOUNDFONT_URL  := "https://freepats.zenvoid.org/Piano/SF/freepats-acoustic-grand-piano-20211029.sf2"
SOUNDFONT_FILE := "freepats-acoustic-grand-piano.sf2"

# ---- web dashboard (web/ — Next.js static export, docs/WEB_UI.md) ---------
# web-check is deliberately NOT part of `check`: web checks run only when
# web/ changed (docs/WEB_UI.md §Decisions #5). Run it by hand (or in a
# path-filtered CI job) whenever you touch web/.

web-setup:
    cd web && pnpm install

# Dev loop: next dev on :3000 proxying /api/* to the daemon on :8666
# (next.config.ts rewrites). Run the daemon too — or use
# `hivemind Procfile.dev` to get both.
web-dev:
    cd web && pnpm dev

# Build the static export and refresh the embedded copy. The copy under
# internal/web/static/app/ is COMMITTED so `go build` needs no node.
web-build:
    cd web && pnpm build
    rm -rf internal/web/static/app
    mkdir -p internal/web/static/app
    cp -a web/out/. internal/web/static/app/

web-check:
    cd web && pnpm exec biome ci . && pnpm exec tsc --noEmit

# Web unit/component tests (vitest) — run whenever you touch web/.
web-test:
    cd web && pnpm test

fetch-soundfont:
    mkdir -p soundfonts
    @if [ -f soundfonts/{{SOUNDFONT_FILE}} ]; then \
        echo "already have soundfonts/{{SOUNDFONT_FILE}}"; \
    else \
        echo "fetching {{SOUNDFONT_URL}} …"; \
        curl -fL --retry 3 -o soundfonts/{{SOUNDFONT_FILE}} {{SOUNDFONT_URL}} \
            || { echo "fetch failed — drop any SF2 at soundfonts/{{SOUNDFONT_FILE}} and re-run"; exit 1; }; \
    fi
    @echo "to use: in polyclav.toml set [soundfont] path = \"$PWD/soundfonts/{{SOUNDFONT_FILE}}\""
