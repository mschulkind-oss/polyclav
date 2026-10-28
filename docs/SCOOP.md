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

**Chosen: manual, from the release.** No secret to rotate, one step per release.
After a release publishes, regenerate the manifest from the *published* asset
and push it to the bucket:

```sh
curl -sSL -o /tmp/polyclav-windows-x86_64.zip \
  https://github.com/mschulkind-oss/polyclav/releases/download/vX.Y.Z/polyclav-windows-x86_64.zip
scripts/scoop-manifest.sh X.Y.Z /tmp/polyclav-windows-x86_64.zip <bucket>/bucket/polyclav.json
git -C <bucket> commit -am "polyclav X.Y.Z"
git -C <bucket> push
```

Use `curl`, not `gh release download`: the github broker refuses the latter
because it writes files on the host, while the asset URL is public.

The alternative, if that step gets tedious: have `publish.yml`'s `build-windows`
job regenerate the manifest and push it with a `SCOOP_BUCKET_TOKEN` secret
(fine-grained PAT, `contents: write` on just that repo). Not done — one more
secret to rotate for one step per release.

Either way, the check in [`RELEASE.md`](./RELEASE.md) applies: the manifest's
version and hash must match the asset that was actually published.

## Status

- **Live.** The bucket exists and carries `polyclav.json` 0.3.1, generated from
  the published `polyclav-windows-x86_64.zip`
  (sha256 `1427fe4e…`).
- `checkver`/`autoupdate` are in the manifest, so `scoop update` can find later
  releases on its own. A fresh `scoop install` still uses the committed version
  and hash, so regenerate after each release.
- The binary is unsigned, so the first run shows SmartScreen's "Windows protected
  your PC". Scoop users are used to this; code signing is tracked separately.
