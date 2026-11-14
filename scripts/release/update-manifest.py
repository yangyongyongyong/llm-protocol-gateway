#!/usr/bin/env python3
"""Build the stable update.json asset for a GitHub Release.

The app reads this file from:
  https://github.com/OWNER/REPO/releases/latest/download/update.json

Keeping the manifest as a release asset avoids using the GitHub API for every
installed app and makes the feed stable even when the release tag changes.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
from pathlib import Path
from urllib.parse import quote


ASSETS = {
    "darwin-arm64": "llm-protocol-gateway-desktop-darwin-arm64.zip",
    "darwin-amd64": "llm-protocol-gateway-desktop-darwin-amd64.zip",
    "windows-amd64": "llm-protocol-gateway-desktop-windows-amd64.exe",
    "linux-amd64": "llm-protocol-gateway-desktop-linux-amd64.AppImage",
}


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--assets", required=True, type=Path)
    parser.add_argument("--notes-file", type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()

    version = args.version.removeprefix("v")
    tag = args.tag if args.tag.startswith("v") else "v" + args.tag
    notes = ""
    if args.notes_file and args.notes_file.exists():
        notes = args.notes_file.read_text(encoding="utf-8")
        if len(notes) > 8000:
            notes = notes[:8000] + "\n\n…\n"

    assets = {}
    for key, name in ASSETS.items():
        path = args.assets / name
        if not path.is_file():
            raise SystemExit(f"missing release asset: {path}")
        assets[key] = {
            "name": name,
            "url": f"https://github.com/{args.repo}/releases/download/{tag}/{quote(name)}",
            "size": path.stat().st_size,
            "sha256": sha256(path),
        }

    payload = {
        "version": version,
        "publishedAt": dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat(),
        "releaseUrl": f"https://github.com/{args.repo}/releases/tag/{tag}",
        "notes": notes,
        "assets": assets,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(args.output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
