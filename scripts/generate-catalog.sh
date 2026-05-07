#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

: "${CATALOG_NAME:=gatekeeper-earlywatch}"
: "${CATALOG_VERSION:=v0.1.0}"
: "${CATALOG_REPOSITORY:=https://github.com/sozercan/gatekeeper-earlywatch}"
export CATALOG_REPOSITORY
: "${CATALOG_BASE_URL:=https://raw.githubusercontent.com/sozercan/gatekeeper-earlywatch/main}"
export CATALOG_BASE_URL

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
import re

catalog = Path("catalog.yaml")
repository = os.environ["CATALOG_REPOSITORY"]
base_url = os.environ["CATALOG_BASE_URL"].rstrip("/")
bundle_name = "earlywatch-default"

# Gator PolicyCatalog bundles reference policy names, and each policy carries
# the bundle-specific constraint path. Gator currently supports only one
# constraint path per policy per bundle, so templates with multiple default
# constraints are represented by catalog-only policy aliases that reuse the
# same ConstraintTemplate and point at different constraint manifests.
constraint_paths = {
    "ewannotationcheck": "library/gatekeeper-earlywatch/annotation-check/constraint.yaml",
    "ewapprovalcheck": "library/gatekeeper-earlywatch/approval-check/constraint.yaml",
    "ewchecklock": "library/gatekeeper-earlywatch/check-lock/constraint.yaml",
    "ewdatakeysafetycheck": "library/gatekeeper-earlywatch/data-key-safety-check/constraint.yaml",
    "ewexistingresources": "library/gatekeeper-earlywatch/existing-resources/constraint.yaml",
    "ewexpressioncheck": "library/gatekeeper-earlywatch/expression-check/constraint.yaml",
    "ewmanualtouchcheck": "library/gatekeeper-earlywatch/manual-touch-check/constraint.yaml",
    "ewnamereferencecheck": "library/gatekeeper-earlywatch/name-reference-check/constraint.yaml",
    "ewservicepodselectorcheck": "library/gatekeeper-earlywatch/service-pod-selector-check/constraint.yaml",
}

aliases = [
    {
        "name": "ewexistingresources-static",
        "source": "ewexistingresources",
        "description": "Catalog alias used by the earlywatch-default bundle for the EWExistingResources static-selector constraint.",
        "constraint_path": "library/gatekeeper-earlywatch/existing-resources/constraint-static.yaml",
    },
    {
        "name": "ewexpressioncheck-regex",
        "source": "ewexpressioncheck",
        "description": "Catalog alias used by the earlywatch-default bundle for the EWExpressionCheck regex constraint.",
        "constraint_path": "library/gatekeeper-earlywatch/expression-check/constraint-regex.yaml",
    },
]

bundle_policies = [
    "ewannotationcheck",
    "ewapprovalcheck",
    "ewchecklock",
    "ewdatakeysafetycheck",
    "ewexistingresources",
    "ewexistingresources-static",
    "ewexpressioncheck",
    "ewexpressioncheck-regex",
    "ewmanualtouchcheck",
    "ewnamereferencecheck",
    "ewservicepodselectorcheck",
]

for path in list(constraint_paths.values()) + [alias["constraint_path"] for alias in aliases]:
    if not Path(path).is_file():
        raise SystemExit(f"constraint path not found: {path}")


def content_url(path: str) -> str:
    return f"{base_url}/{path}"


def add_bundle_fields(block: str, constraint_path: str) -> str:
    block = block.rstrip() + "\n"
    return (
        block
        + "  bundles:\n"
        + f"  - {bundle_name}\n"
        + "  bundleConstraints:\n"
        + f"    {bundle_name}: {content_url(constraint_path)}\n"
    )


def field(block: str, name: str) -> str:
    if name == "category":
        match = re.search(r"(?m)^- category: (.+)$", block)
    else:
        match = re.search(rf"(?m)^  {name}: (.+)$", block)
    if not match:
        raise SystemExit(f"policy block missing {name}:\n{block}")
    return match.group(1)


lines = catalog.read_text().splitlines(keepends=True)
for i, line in enumerate(lines):
    if line.startswith("  repository: "):
        lines[i] = f"  repository: {repository}\n"
        break
else:
    raise SystemExit("metadata.repository not found in catalog.yaml")
text = "".join(lines)
text = text.replace(
    "#   gator policy generate-catalog --library-path=. --output=catalog.yaml\n",
    "#   ./scripts/generate-catalog.sh\n",
)

bundle_block = "bundles:\n"
bundle_block += "- description: Default Gatekeeper EarlyWatch policy bundle with preconfigured constraints.\n"
bundle_block += f"  name: {bundle_name}\n"
bundle_block += "  policies:\n"
for policy_name in bundle_policies:
    bundle_block += f"  - {policy_name}\n"

text = re.sub(r"(?m)^bundles: \[\]\n", bundle_block, text, count=1)
if "bundles: []" in text:
    raise SystemExit("failed to replace empty bundles list")

prefix, sep, policies_text = text.partition("policies:\n")
if not sep:
    raise SystemExit("policies section not found in catalog.yaml")

raw_blocks = [block for block in re.split(r"(?m)(?=^- category: )", policies_text) if block.strip()]
source_blocks = {}
for block in raw_blocks:
    source_blocks[field(block, "name")] = block

aliases_by_source = {}
for alias in aliases:
    aliases_by_source.setdefault(alias["source"], []).append(alias)

out_blocks = []
seen_names = set()
for block in raw_blocks:
    policy_name = field(block, "name")
    if policy_name in constraint_paths:
        block = add_bundle_fields(block, constraint_paths[policy_name])
    out_blocks.append(block)
    seen_names.add(policy_name)

    for alias in aliases_by_source.get(policy_name, []):
        source = source_blocks[alias["source"]]
        alias_block = (
            f"- category: {field(source, 'category')}\n"
            f"  description: {alias['description']}\n"
            f"  name: {alias['name']}\n"
            f"  templatePath: {field(source, 'templatePath')}\n"
            f"  version: {field(source, 'version')}\n"
        )
        alias_block = add_bundle_fields(alias_block, alias["constraint_path"])
        out_blocks.append(alias_block)
        seen_names.add(alias["name"])

missing = [policy_name for policy_name in bundle_policies if policy_name not in seen_names]
if missing:
    raise SystemExit(f"bundle policies missing from catalog policies: {', '.join(missing)}")

catalog.write_text(prefix + sep + "".join(out_blocks))
PY

chmod 0644 catalog.yaml
