#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

: "${CATALOG_NAME:=gatekeeper-earlywatch}"
: "${CATALOG_VERSION:=v0.1.0}"
: "${CATALOG_REPOSITORY:=https://github.com/sozercan/gatekeeper-earlywatch}"
export CATALOG_REPOSITORY
: "${CATALOG_BASE_URL:=https://raw.githubusercontent.com/sozercan/gatekeeper-earlywatch/main}"

if ! command -v gator >/dev/null 2>&1; then
  echo "gator is required to generate catalog.yaml" >&2
  exit 1
fi

gator policy generate-catalog \
  --library-path="$repo_root" \
  --output="$repo_root/catalog.yaml" \
  --name="$CATALOG_NAME" \
  --version="$CATALOG_VERSION" \
  --base-url="$CATALOG_BASE_URL"

python3 - <<'PY'
from pathlib import Path
import os

catalog = Path("catalog.yaml")
repository = os.environ["CATALOG_REPOSITORY"]
lines = catalog.read_text().splitlines(keepends=True)
for i, line in enumerate(lines):
    if line.startswith("  repository: "):
        lines[i] = f"  repository: {repository}\n"
        break
else:
    raise SystemExit("metadata.repository not found in catalog.yaml")
catalog.write_text("".join(lines))
PY

chmod 0644 catalog.yaml
