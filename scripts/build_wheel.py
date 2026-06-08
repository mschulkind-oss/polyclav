#!/usr/bin/env python3
"""Package pre-built polyclav binaries into a manylinux Python wheel.

go-to-wheel can't be used here: it forces CGO_ENABLED=0 and cross-compiles,
but polyclav is a cgo binary linking a Rust staticlib + PipeWire/ALSA/lilv.
So we build the binary natively (see .github/workflows/publish.yml) and
package it ourselves.

The binaries go in `<dist>.data/scripts/`, which pip/uv install straight into
the environment's bin/ — so `uvx polyclav` runs the native binary directly,
no Python shim. The wheel is dynamically linked: it depends on the user's
system PipeWire/ALSA/lilv (FHS-standard locations, resolved by SONAME via
ldconfig). It is tagged manylinux (a glibc-baseline promise) but is NOT
auditwheel-self-contained — by design (no bundling, no static linking).

Usage:
  build_wheel.py --version 0.1.0 \
      --platform-tag manylinux_2_35_x86_64 \
      --binary dist/polyclav --binary dist/polyclav-components \
      --readme README.md --output-dir dist
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import zipfile
from pathlib import Path

NAME = "polyclav"
SUMMARY = "Linux live-piano host: MIDI keyboard -> soundfont/plugin synthesis -> PipeWire"
AUTHOR = "Matthew Schulkind"
URL = "https://github.com/mschulkind-oss/polyclav"
LICENSE = "Apache-2.0"


def _record_line(arcname: str, data: bytes) -> str:
    digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()
    return f"{arcname},sha256={digest},{len(data)}"


def build(version: str, platform_tag: str, binaries: list[Path], readme: Path, out_dir: Path) -> Path:
    dist_info = f"{NAME}-{version}.dist-info"
    data_scripts = f"{NAME}-{version}.data/scripts"

    long_desc = readme.read_text(encoding="utf-8") if readme and readme.exists() else SUMMARY

    metadata = "\n".join(
        [
            "Metadata-Version: 2.1",
            f"Name: {NAME}",
            f"Version: {version}",
            f"Summary: {SUMMARY}",
            f"Author: {AUTHOR}",
            f"License: {LICENSE}",
            f"Project-URL: Homepage, {URL}",
            "Description-Content-Type: text/markdown",
            "Classifier: Operating System :: POSIX :: Linux",
            "Classifier: License :: OSI Approved :: Apache Software License",
            "Classifier: Topic :: Multimedia :: Sound/Audio :: MIDI",
            "Requires-Python: >=3.8",
            "",
            long_desc,
        ]
    )

    wheel_meta = "\n".join(
        [
            "Wheel-Version: 1.0",
            "Generator: polyclav-build_wheel",
            "Root-Is-Purelib: false",
            f"Tag: py3-none-{platform_tag}",
            "",
        ]
    )

    # (arcname, data, is_executable)
    entries: list[tuple[str, bytes, bool]] = []
    for b in binaries:
        entries.append((f"{data_scripts}/{b.name}", b.read_bytes(), True))
    entries.append((f"{dist_info}/METADATA", metadata.encode("utf-8"), False))
    entries.append((f"{dist_info}/WHEEL", wheel_meta.encode("utf-8"), False))

    record_lines = [_record_line(arc, data) for arc, data, _ in entries]
    record_lines.append(f"{dist_info}/RECORD,,")
    record = ("\n".join(record_lines) + "\n").encode("utf-8")
    entries.append((f"{dist_info}/RECORD", record, False))

    out_dir.mkdir(parents=True, exist_ok=True)
    wheel_path = out_dir / f"{NAME}-{version}-py3-none-{platform_tag}.whl"
    with zipfile.ZipFile(wheel_path, "w", zipfile.ZIP_DEFLATED) as zf:
        for arc, data, is_exec in entries:
            info = zipfile.ZipInfo(arc)
            # rwxr-xr-x for binaries, rw-r--r-- for metadata; high 16 bits = mode.
            info.external_attr = (0o755 if is_exec else 0o644) << 16
            info.compress_type = zipfile.ZIP_DEFLATED
            zf.writestr(info, data)

    return wheel_path


def main() -> None:
    ap = argparse.ArgumentParser(description="Package polyclav binaries into a manylinux wheel.")
    ap.add_argument("--version", required=True)
    ap.add_argument("--platform-tag", required=True, help="e.g. manylinux_2_35_x86_64")
    ap.add_argument("--binary", action="append", dest="binaries", required=True, type=Path)
    ap.add_argument("--readme", type=Path, default=Path("README.md"))
    ap.add_argument("--output-dir", type=Path, default=Path("dist"))
    args = ap.parse_args()

    for b in args.binaries:
        if not b.exists():
            raise SystemExit(f"binary not found: {b}")

    wheel = build(args.version, args.platform_tag, args.binaries, args.readme, args.output_dir)
    print(f"built {wheel} ({wheel.stat().st_size} bytes)")


if __name__ == "__main__":
    main()
