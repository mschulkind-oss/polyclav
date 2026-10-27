#!/usr/bin/env bash
# Generate the Scoop manifest for a built release zip.
#
#   scripts/scoop-manifest.sh <version> <zip> <out>
#   scripts/scoop-manifest.sh 0.3.0 dist/polyclav-windows-x86_64.zip bucket/polyclav.json
#
# The repo is its own Scoop bucket (a `bucket/` directory of JSON manifests), so
# the manifest has to carry the exact sha256 of the asset the release published —
# which is only knowable after the build, hence a generator rather than a
# hand-edited file.
#
# checkver/autoupdate are emitted anyway: they let Scoop discover later releases
# on its own, so a release where this step did not run still upgrades correctly
# once the manifest exists.
set -euo pipefail

version="${1:?usage: scoop-manifest.sh <version> <zip> <out>}"
zip="${2:?usage: scoop-manifest.sh <version> <zip> <out>}"
out="${3:?usage: scoop-manifest.sh <version> <zip> <out>}"

[ -f "$zip" ] || { echo "scoop-manifest: no such zip: $zip" >&2; exit 1; }
case "$version" in
  v*) echo "scoop-manifest: version must not include the tag's leading 'v': $version" >&2; exit 1 ;;
esac

hash=$(sha256sum "$zip" | cut -d' ' -f1)
asset=$(basename "$zip")
repo="https://github.com/mschulkind-oss/polyclav"

mkdir -p "$(dirname "$out")"
cat >"$out" <<EOF
{
  "\$schema": "https://raw.githubusercontent.com/ScoopInstaller/Scoop/master/schema.json",
  "version": "$version",
  "description": "Live piano host: a MIDI keyboard through soundfont synthesis to an audio sink",
  "homepage": "$repo",
  "license": "Apache-2.0",
  "architecture": {
    "64bit": {
      "url": "$repo/releases/download/v$version/$asset",
      "hash": "$hash"
    }
  },
  "bin": "polyclav.exe",
  "checkver": {
    "github": "$repo"
  },
  "autoupdate": {
    "architecture": {
      "64bit": {
        "url": "$repo/releases/download/v\$version/$asset"
      }
    }
  },
  "notes": [
    "First run downloads the starter soundfonts (~500 MB): polyclav bootstrap",
    "Then start the daemon: polyclav    (web UI at http://127.0.0.1:8666/)",
    "Name your keyboard in [midi].allow_devices, or no notes will play. The web UI has a picker.",
    "Check the install: polyclav doctor"
  ]
}
EOF

echo "wrote $out (version $version, sha256 $hash)"
