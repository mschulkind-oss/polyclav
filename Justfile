default:
    @just --list

setup:
    cargo fetch
    go mod download

build-rust:
    cargo build --release --manifest-path audio-core/Cargo.toml

build: build-rust
    mkdir -p bin
    go build -o bin/polyclav ./cmd/polyclav

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
