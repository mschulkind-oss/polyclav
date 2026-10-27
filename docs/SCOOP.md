# Installing on Windows with Scoop

polyclav ships as a portable `polyclav.exe` inside a zip, which is exactly what
Scoop is good at: user-scope install, no admin, no installer, and upgrades that
do not fight a running process.

## Install

```powershell
scoop bucket add polyclav https://github.com/mschulkind-oss/scoop-bucket
scoop install polyclav
```

Then, once:

```powershell
polyclav bootstrap   # starter soundfonts, ~500 MB
polyclav            # start the daemon; web UI at http://127.0.0.1:8666/
```

`polyclav doctor` prints what the build found. Nothing makes sound until you name
your keyboard in `[midi].allow_devices` — the web UI has a picker that writes it
for you. The manifest's `notes` say the same thing at install time.

## Upgrading

```powershell
scoop update polyclav
```

Scoop installs each version into its own directory and re-points the shim, so an
upgrade does not try to overwrite `polyclav.exe` while the daemon is running —
the usual failure mode for portable Windows apps. Stop the daemon first anyway if
you want the old version's files reclaimed immediately.

## What the bucket repo holds

`mschulkind-oss/scoop-bucket` is a git repo with one manifest at
`bucket/polyclav.json`. That is the whole bucket — Scoop looks for `bucket/*.json`
in the repo.

The manifest carries the version, the release-asset URL, and the **sha256 of the
published zip**, so it cannot be written by hand: the hash only exists after the
release build. `scripts/scoop-manifest.sh` in this repo generates it from a
downloaded asset:

```sh
scripts/scoop-manifest.sh 0.3.0 polyclav-windows-x86_64.zip bucket/polyclav.json
```

It also emits `checkver`/`autoupdate` pointing at this repository's GitHub
releases, so Scoop can discover later versions on its own even if the manifest is
not updated for a release.

## Keeping it current

Two options; pick one and write it down here once it is settled.

1. **Manual, from the release.** After a release, regenerate the manifest from
   the published asset and push it to the bucket repo. Simple, no secret, and
   one step per release.
2. **Automated, from CI.** `publish.yml`'s `build-windows` job regenerates the
   manifest and pushes it to the bucket repo using a `SCOOP_BUCKET_TOKEN` secret
   (fine-grained PAT, `contents: write` on that one repo). More moving parts, but
   no manual step.

Whichever is chosen, the check in [`RELEASE.md`](./RELEASE.md) applies: the
manifest's version and hash must match the asset that was actually published.

## Not yet done

- The bucket repo and its first manifest do not exist yet; they appear at the
  first Windows release.
- The binary is unsigned, so the first run shows SmartScreen's "Windows protected
  your PC". Scoop users are used to this; code signing is tracked separately.
