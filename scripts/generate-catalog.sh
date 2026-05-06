#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

: "${CATALOG_NAME:=gatekeeper-earlywatch}"
: "${CATALOG_VERSION:=v0.1.0}"
: "${CATALOG_REPOSITORY:=https://github.com/sozercan/gatekeeper-earlywatch}"
export CATALOG_REPOSITORY
: "${CATALOG_CATEGORY:=$CATALOG_NAME}"
export CATALOG_CATEGORY
: "${CATALOG_BASE_URL:=https://raw.githubusercontent.com/sozercan/gatekeeper-earlywatch/main}"

if ! command -v gator >/dev/null 2>&1; then
  echo "gator is required to generate catalog.yaml" >&2
  exit 1
fi

python3 - <<'PY'
from pathlib import Path
import json
import os
import shutil

repo = Path.cwd()
library_root = repo / "library"
collection_name = os.environ["CATALOG_CATEGORY"]
collection = library_root / collection_name

policies = [
    {
        "slug": "existing-resources",
        "source": "policy/01-existing-resources-template.yaml",
        "title": "Existing Resources",
        "description": "Requires configured resources to already exist in Gatekeeper inventory before matching operations are allowed.",
    },
    {
        "slug": "name-reference-check",
        "source": "policy/02-name-reference-check-template.yaml",
        "title": "Name Reference Check",
        "description": "Prevents deleting resources that are still referenced by configured inventory objects.",
    },
    {
        "slug": "annotation-check",
        "source": "policy/03-annotation-check-template.yaml",
        "title": "Annotation Check",
        "description": "Requires matching resources to carry a configured annotation and, optionally, a configured annotation value.",
    },
    {
        "slug": "approval-check",
        "source": "policy/04-approval-check-template.yaml",
        "title": "Approval Check",
        "description": "Verifies signed approvals for DELETE and UPDATE requests through the EarlyWatch external-data provider.",
    },
    {
        "slug": "check-lock",
        "source": "policy/05-check-lock-template.yaml",
        "title": "Check Lock",
        "description": "Blocks updates or deletes to locked resources unless the change is limited to allowed lock metadata.",
    },
    {
        "slug": "expression-check",
        "source": "policy/06-expression-check-template.yaml",
        "title": "Expression Check",
        "description": "Evaluates structured operation, namespace, and name predicates against admission requests.",
    },
    {
        "slug": "manual-touch-check",
        "source": "policy/07-manual-touch-check-template.yaml",
        "title": "Manual Touch Check",
        "description": "Requires a recent matching manual touch event before allowing protected operations.",
    },
    {
        "slug": "service-pod-selector-check",
        "source": "policy/08-service-pod-selector-check-template.yaml",
        "title": "Service Pod Selector Check",
        "description": "Prevents pod label changes or deletions that would break existing Service pod selectors.",
    },
    {
        "slug": "data-key-safety-check",
        "source": "policy/09-data-key-safety-check-template.yaml",
        "title": "Data Key Safety Check",
        "description": "Prevents removing ConfigMap or Secret data keys that are still referenced by Pods.",
    },
]


def yaml_string(value: str) -> str:
    # YAML is a superset of JSON strings, so JSON quoting gives predictable
    # escaping without depending on PyYAML in this lightweight generation step.
    return json.dumps(value)


def add_metadata_annotations(text: str, annotations: dict[str, str]) -> str:
    lines = text.splitlines(keepends=True)
    try:
        metadata_idx = next(i for i, line in enumerate(lines) if line == "metadata:\n")
    except StopIteration as exc:
        raise ValueError("template is missing top-level metadata") from exc

    # End of top-level metadata block: the next top-level non-empty line.
    metadata_end = len(lines)
    for i in range(metadata_idx + 1, len(lines)):
        line = lines[i]
        if line.strip() and not line.startswith((" ", "\t")):
            metadata_end = i
            break

    existing_keys = {key for key in annotations if any(line.lstrip().startswith(f"{key}:") for line in lines[metadata_idx:metadata_end])}
    annotation_lines = [
        f"    {key}: {yaml_string(value)}\n"
        for key, value in annotations.items()
        if key not in existing_keys
    ]
    if not annotation_lines:
        return text

    annotations_idx = None
    for i in range(metadata_idx + 1, metadata_end):
        if lines[i] == "  annotations:\n":
            annotations_idx = i
            break

    if annotations_idx is not None:
        insert_at = annotations_idx + 1
        lines[insert_at:insert_at] = annotation_lines
        return "".join(lines)

    name_idx = None
    for i in range(metadata_idx + 1, metadata_end):
        if lines[i].startswith("  name:"):
            name_idx = i
            break
    if name_idx is None:
        raise ValueError("template metadata is missing name")

    lines[name_idx + 1:name_idx + 1] = ["  annotations:\n", *annotation_lines]
    return "".join(lines)


if library_root.exists():
    shutil.rmtree(library_root)
collection.mkdir(parents=True)

(library_root / "kustomization.yaml").write_text(
    "apiVersion: kustomize.config.k8s.io/v1beta1\n"
    "kind: Kustomization\n"
    "resources:\n"
    f"- {collection_name}\n"
)

(collection / "kustomization.yaml").write_text(
    "apiVersion: kustomize.config.k8s.io/v1beta1\n"
    "kind: Kustomization\n"
    "resources:\n"
    + "".join(f"- {policy['slug']}\n" for policy in policies)
)

for policy in policies:
    policy_dir = collection / policy["slug"]
    policy_dir.mkdir(parents=True)
    (policy_dir / "kustomization.yaml").write_text(
        "apiVersion: kustomize.config.k8s.io/v1beta1\n"
        "kind: Kustomization\n"
        "resources:\n"
        "- template.yaml\n"
    )
    source = repo / policy["source"]
    text = source.read_text()
    text = add_metadata_annotations(
        text,
        {
            "metadata.gatekeeper.sh/title": policy["title"],
            "metadata.gatekeeper.sh/version": "1.0.0",
            "description": policy["description"],
        },
    )
    (policy_dir / "template.yaml").write_text(text)
PY

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
