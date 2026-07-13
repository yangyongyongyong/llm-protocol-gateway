#!/usr/bin/env bash
set -euo pipefail

SRC="$(cd "$(dirname "$0")" && pwd)"
NAME="local.openai-key-toggle"
VERSION="$(python3 -c "import json; print(json.load(open('$SRC/package.json'))['version'])")"
DEST_ROOT="${HOME}/.cursor/extensions"
DEST="${DEST_ROOT}/${NAME}-${VERSION}"

mkdir -p "$DEST_ROOT"
rm -rf "${DEST_ROOT}/${NAME}-"*
mkdir -p "$DEST"
cp "$SRC/package.json" "$SRC/extension.js" "$DEST/"

python3 - <<PY
import json, os, time
from pathlib import Path

ext_id = "local.openai-key-toggle"
version = "${VERSION}"
dest = Path(os.path.expanduser("~/.cursor/extensions")) / f"{ext_id}-{version}"
index = Path(os.path.expanduser("~/.cursor/extensions/extensions.json"))

entries = []
if index.exists():
    entries = json.loads(index.read_text())

entries = [e for e in entries if e.get("identifier", {}).get("id") != ext_id]
entries.append({
    "identifier": {"id": ext_id},
    "version": version,
    "location": {
        "\$mid": 1,
        "fsPath": str(dest),
        "external": f"file://{dest}",
        "path": str(dest),
        "scheme": "file",
    },
    "relativeLocation": f"{ext_id}-{version}",
    "metadata": {
        "installedTimestamp": int(time.time() * 1000),
        "source": "vsix",
    },
})
index.write_text(json.dumps(entries, indent=None))
print(f"Installed {ext_id}@{version} -> {dest}")
PY

echo "Done. In Cursor: Developer: Reload Window, then press Cmd+Alt+Shift+K"
