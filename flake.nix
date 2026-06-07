{
  description = "polyclav build environment: system libs + toolchains for the Rust audio-core + Go cgo build.";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in {
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            # Language toolchains. mise.toml pins exact versions for the
            # yolo-jail / dev-host flow; this shell provides a self-contained
            # alternative for CI and any nix-equipped contributor.
            go
            rustc cargo rustfmt clippy
            just

            # System libs the Rust audio-core links against.
            pkg-config
            clang
            sfizz       # SFZ playback (cgo: -lsfizz)
            pipewire    # PipeWire I/O (pipewire-rs)
            alsa-lib    # ALSA MIDI seq (gomidi/rtmididrv)
            liblo       # OSC client
            lilv lv2 serd sord sratom  # LV2 plugin hosting (livi-rs)
            clap        # CLAP plugin spec headers (clack-rs)
          ];

          # bindgen needs LIBCLANG_PATH to find libclang. Override mise.toml's
          # yolo-jail-pinned store path with this shell's actual libclang so
          # the same flake works on any host (CI, dev machine, etc.).
          LIBCLANG_PATH = "${pkgs.libclang.lib}/lib";
        };
      });
}
