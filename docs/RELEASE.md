# Cutting a release

A release is a **published GitHub release with a `vX.Y.Z` tag**. That single act
triggers `.github/workflows/publish.yml`, which builds and publishes everything
else. Nothing is uploaded from a developer machine.

## What one release produces

| Job | Output |
|---|---|
| `build-linux` | manylinux wheel → PyPI (`uvx polyclav`, `pipx install polyclav`) |
| `build-macos` | macOS arm64 wheel → PyPI, plus `polyclav-macos-arm64.tar.gz` |
| `build-windows` | `polyclav-windows-x86_64.zip` (with `polyclav.exe` and `polyclav-components.exe`) as a **release asset** |
| `update-homebrew-tap` | rewrites the tap formula to the new version |
| `publish` | publishes the wheels to PyPI |

`build-windows` is **not** in `needs:` of `update-homebrew-tap` or `publish`, so a
Windows failure does not block a release. That is deliberate — the Windows port
is not hardware-verified — but it means a release can go out *without* a Windows
asset and nobody is told. **Check for the zip after every release** (below).

## Before you tag

- `just check` is green on `main`, and CI is green on all three workflows.
- The tree is clean. Go stamps `+dirty` into `--version` when it is not, and that
  suffix would ship in the release artifact.
- Decide the version. Features added since the last tag mean a minor bump;
  `v0.2.0` → `v0.3.0` for the Windows port.
- Optional dry run: run the **Publish to PyPI** workflow manually
  (`workflow_dispatch`) with a version. It builds everything and uploads build
  artifacts, but does not publish to PyPI and does not attach release assets —
  so it validates the pipeline, not the release.

## Cutting it

Draft a GitHub release, create the tag `vX.Y.Z` on the commit you want, and
publish it. Then watch the run:

```sh
gh run list -R mschulkind-oss/polyclav --limit 5
gh run watch <run-id> -R mschulkind-oss/polyclav
```

## After it publishes — verify, do not assume

1. **The Windows asset exists.** This is the one that silently disappears.
   ```sh
   gh release view vX.Y.Z -R mschulkind-oss/polyclav --json assets -q '.assets[].name'
   ```
   You want `polyclav-windows-x86_64.zip` alongside the Linux and macOS tarballs.
2. **The Windows binary reports the tag.** Download the zip, run
   `polyclav.exe --version`; it should print `vX.Y.Z`, not a pseudo-version and
   not `+dirty`.
3. **The exe is portable.** `objdump -p polyclav.exe | grep 'DLL Name'` should
   list only Windows system DLLs.
4. **The zip contains both binaries.** `unzip -l` should show `polyclav.exe`
   and `polyclav-components.exe`. A zip with only the daemon is a build-windows
   step that lost the components build, and the Scoop manifest's `bin` is
   derived from what the archive actually holds — so a partial zip produces a
   manifest that shims only `polyclav`.
5. **The Scoop manifest matches the asset.** `bucket/polyclav.json` in the bucket
   repo must carry the new version and the sha256 of the *published* zip
   (`sha256sum polyclav-windows-x86_64.zip`). See
   [`SCOOP.md`](./SCOOP.md).

## sfizz is built and released separately

`sfizz.dll` (Windows), `libsfizz.dylib` (macOS) and `libsfizz.so` (Linux) are
**not** part of a versioned release. Each is built by its own workflow and
published under a `sfizz-<os>-<ref>` tag:

| Workflow | Asset | Tag |
|---|---|---|
| `build-sfizz-windows.yml` | `sfizz.dll` | `sfizz-windows-<ref>` |
| `build-sfizz-macos.yml` | `libsfizz.dylib` | `sfizz-macos-<ref>` |
| `build-sfizz-linux.yml` | `libsfizz.so` | `sfizz-linux-<ref>` |

All three are `workflow_dispatch` only, because sfizz releases infrequently and
re-running on every push would risk picking up an unreviewed sfizz version.
`polyclav bootstrap` downloads the matching asset; a missing asset means `.sfz`
patches are silent, not that bootstrap fails.

## Point releases

The intended repair path is a new patch tag (`vX.Y.Z+1`), not re-tagging: PyPI
and Homebrew will not accept a version they have already seen, and a moved tag
makes every checksum published for it wrong. Fix forward.

## Rolling back

Delete the release and the tag, and yank the PyPI version. The Windows zip and
the Scoop manifest are the easy ones — the Scoop manifest just needs regenerating
against the replacement release.
