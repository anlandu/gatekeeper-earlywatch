#!/usr/bin/env bash
# End-to-end CI parity validation for the default EarlyWatch-on-Gatekeeper stack.
#
# The script intentionally exercises the same paths a repository consumer will
# use: build the local external-data providers, install Gatekeeper in kind,
# install the default policy bundle with gator, and verify live admission with
# server-side dry-runs.
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

CLUSTER_NAME="${KIND_CLUSTER_NAME:-gatekeeper-earlywatch-ci}"
APPROVAL_IMAGE="${APPROVAL_IMAGE:-gatekeeper-earlywatch/approval-verifier:ci}"
TOUCH_IMAGE="${TOUCH_IMAGE:-gatekeeper-earlywatch/touch-monitor:ci}"
ARTIFACT_DIR="${ARTIFACT_DIR:-${ROOT}/.ci-artifacts}"
WORK_DIR="$(mktemp -d)"
CI_KIND_PARITY_HELPER="${WORK_DIR}/ci-kind-parity-helper"
MANUAL_TOUCH_USER_AGENT="kubectl/v1.32.0 (linux/amd64) kubernetes/ci"
GATEKEEPER_CHART_VERSION="${GATEKEEPER_CHART_VERSION:-3.22.2}"
EARLYWATCH_REPO_URL="${EARLYWATCH_REPO_URL:-https://github.com/brendandburns/early-watch}"
EARLYWATCH_UPSTREAM_COMMIT="${EARLYWATCH_UPSTREAM_COMMIT:-55fe022d6190f93474c7a15cb3408e6540160f5d}"
EARLYWATCH_COMMIT_SHORT="${EARLYWATCH_UPSTREAM_COMMIT:0:12}"
EARLYWATCH_WEBHOOK_IMAGE="${EARLYWATCH_WEBHOOK_IMAGE:-earlywatch/webhook:ci-${EARLYWATCH_COMMIT_SHORT}}"
EARLYWATCH_AUDIT_IMAGE="${EARLYWATCH_AUDIT_IMAGE:-earlywatch/audit-monitor:ci-${EARLYWATCH_COMMIT_SHORT}}"
DEPLOY_UPSTREAM_EARLYWATCH="${DEPLOY_UPSTREAM_EARLYWATCH:-1}"
PORT_FORWARD_PID=""

mkdir -p "${ARTIFACT_DIR}/assertions" "${ARTIFACT_DIR}/logs" "${ARTIFACT_DIR}/coverage"

log() {
  printf '\n[%s] %s\n' "$(date -Is)" "$*" >&2
}

run() {
  log "+ $*"
  "$@"
}

slugify() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-//; s/-$//'
}

collect_artifacts() {
  mkdir -p "${ARTIFACT_DIR}/logs"

  if ! command -v kubectl >/dev/null 2>&1; then
    return 0
  fi
  if ! kubectl cluster-info >/dev/null 2>&1; then
    return 0
  fi

  log "Collecting CI artifacts into ${ARTIFACT_DIR}"
  kubectl version -o yaml >"${ARTIFACT_DIR}/kubectl-version.yaml" 2>&1 || true
  kubectl cluster-info >"${ARTIFACT_DIR}/cluster-info.txt" 2>&1 || true
  kubectl get nodes -o wide >"${ARTIFACT_DIR}/nodes.txt" 2>&1 || true
  kubectl get namespaces -o yaml >"${ARTIFACT_DIR}/namespaces.yaml" 2>&1 || true
  kubectl get pods -A -o wide >"${ARTIFACT_DIR}/pods.txt" 2>&1 || true
  kubectl get deploy,svc -A -o wide >"${ARTIFACT_DIR}/workloads.txt" 2>&1 || true
  kubectl get events -A --sort-by=.metadata.creationTimestamp >"${ARTIFACT_DIR}/events.txt" 2>&1 || true
  kubectl get validatingwebhookconfiguration -o yaml >"${ARTIFACT_DIR}/validatingwebhookconfigurations.yaml" 2>&1 || true
  kubectl get constrainttemplates.templates.gatekeeper.sh -o yaml >"${ARTIFACT_DIR}/constrainttemplates.yaml" 2>&1 || true
  kubectl get providers.externaldata.gatekeeper.sh -o yaml >"${ARTIFACT_DIR}/providers.yaml" 2>&1 || true
  kubectl get config.config.gatekeeper.sh -n gatekeeper-system -o yaml >"${ARTIFACT_DIR}/gatekeeper-config.yaml" 2>&1 || true

  if kubectl api-resources --api-group=constraints.gatekeeper.sh -o name >/tmp/gatekeeper-earlywatch-ci-constraint-resources 2>/dev/null; then
    while IFS= read -r resource; do
      [[ -z "${resource}" ]] && continue
      kubectl get "${resource}" -o yaml >"${ARTIFACT_DIR}/constraints-${resource//\//-}.yaml" 2>&1 || true
    done </tmp/gatekeeper-earlywatch-ci-constraint-resources
  fi

  for ns in gatekeeper-system early-watch-system; do
    kubectl -n "${ns}" describe pods >"${ARTIFACT_DIR}/logs/${ns}-describe-pods.txt" 2>&1 || true
    while IFS= read -r pod; do
      [[ -z "${pod}" ]] && continue
      local pod_name="${pod#pod/}"
      kubectl -n "${ns}" logs "${pod}" --all-containers=true --tail=-1 >"${ARTIFACT_DIR}/logs/${ns}-${pod_name}.log" 2>&1 || true
    done < <(kubectl -n "${ns}" get pods -o name 2>/dev/null || true)
  done
}

cleanup() {
  local rc=$?
  set +e

  if [[ -n "${PORT_FORWARD_PID}" ]]; then
    kill "${PORT_FORWARD_PID}" >/dev/null 2>&1 || true
    wait "${PORT_FORWARD_PID}" >/dev/null 2>&1 || true
  fi

  collect_artifacts || true
  rm -rf "${WORK_DIR}" || true

  if [[ "${KEEP_KIND:-0}" != "1" ]] && command -v kind >/dev/null 2>&1; then
    kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
  fi

  exit "${rc}"
}
trap cleanup EXIT

require_commands() {
  local missing=0
  for cmd in docker git go kind kubectl helm openssl curl gator python3; do
    if ! command -v "${cmd}" >/dev/null 2>&1; then
      printf 'missing required command: %s\n' "${cmd}" >&2
      missing=1
    fi
  done
  if [[ "${missing}" -ne 0 ]]; then
    exit 127
  fi
}

build_ci_helper() {
  run go build -o "${CI_KIND_PARITY_HELPER}" "${ROOT}/scripts/ci-kind-parity-helper.go"
}

ci_helper() {
  run "${CI_KIND_PARITY_HELPER}" "$@"
}

create_kind_cluster() {
  if kind get clusters 2>/dev/null | grep -Fxq "${CLUSTER_NAME}"; then
    if [[ "${REUSE_KIND:-0}" == "1" ]]; then
      log "Reusing existing kind cluster ${CLUSTER_NAME}"
    else
      log "Deleting pre-existing kind cluster ${CLUSTER_NAME}"
      kind delete cluster --name "${CLUSTER_NAME}"
      run kind create cluster --name "${CLUSTER_NAME}" --wait 120s
    fi
  else
    run kind create cluster --name "${CLUSTER_NAME}" --wait 120s
  fi

  run kubectl cluster-info
}

build_and_load_images() {
  run docker build -t "${APPROVAL_IMAGE}" -f provider/Dockerfile provider
  run docker build -t "${TOUCH_IMAGE}" -f touch-monitor/Dockerfile touch-monitor
  run kind load docker-image "${APPROVAL_IMAGE}" --name "${CLUSTER_NAME}"
  run kind load docker-image "${TOUCH_IMAGE}" --name "${CLUSTER_NAME}"
}

write_parity_coverage_artifact() {
  local artifact="${ARTIFACT_DIR}/coverage/parity-coverage.json"
  ci_helper write-parity-coverage-artifact "${artifact}"
}

record_upstream_earlywatch_catalog() {
  local upstream_dir="$1"
  local artifact="${ARTIFACT_DIR}/upstream-earlywatch.json"
  ci_helper record-upstream-earlywatch-catalog "${upstream_dir}" "${artifact}" "${EARLYWATCH_REPO_URL}" "${EARLYWATCH_UPSTREAM_COMMIT}"
}

deploy_upstream_earlywatch() {
  if [[ "${DEPLOY_UPSTREAM_EARLYWATCH}" != "1" ]]; then
    cat >"${ARTIFACT_DIR}/upstream-earlywatch.json" <<EOF
{
  "repo_url": "${EARLYWATCH_REPO_URL}",
  "expected_commit": "${EARLYWATCH_UPSTREAM_COMMIT}",
  "skipped": true,
  "reason": "DEPLOY_UPSTREAM_EARLYWATCH=${DEPLOY_UPSTREAM_EARLYWATCH}"
}
EOF
    log "Skipping upstream EarlyWatch deployment because DEPLOY_UPSTREAM_EARLYWATCH=${DEPLOY_UPSTREAM_EARLYWATCH}"
    return 0
  fi

  local upstream_dir="${WORK_DIR}/early-watch-upstream"
  run git clone "${EARLYWATCH_REPO_URL}" "${upstream_dir}"
  run git -C "${upstream_dir}" checkout "${EARLYWATCH_UPSTREAM_COMMIT}"
  record_upstream_earlywatch_catalog "${upstream_dir}"

  run docker build -t "${EARLYWATCH_WEBHOOK_IMAGE}" "${upstream_dir}"
  cat >"${upstream_dir}/Dockerfile.audit-monitor.ci" <<'EOF'
FROM golang:latest AS builder

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY pkg/ pkg/

RUN CGO_ENABLED=0 GOOS=linux go build -a -o audit-monitor ./cmd/audit-monitor

FROM gcr.io/distroless/static:nonroot

WORKDIR /
COPY --from=builder /workspace/audit-monitor .
USER 65532:65532
ENTRYPOINT ["/audit-monitor"]
EOF
  run docker build -t "${EARLYWATCH_AUDIT_IMAGE}" -f "${upstream_dir}/Dockerfile.audit-monitor.ci" "${upstream_dir}"
  run kind load docker-image "${EARLYWATCH_WEBHOOK_IMAGE}" --name "${CLUSTER_NAME}"
  run kind load docker-image "${EARLYWATCH_AUDIT_IMAGE}" --name "${CLUSTER_NAME}"

  (cd "${upstream_dir}" && run go build -o "${WORK_DIR}/watchctl-upstream" ./cmd/watchctl)
  run "${WORK_DIR}/watchctl-upstream" install \
    --image "${EARLYWATCH_WEBHOOK_IMAGE}" \
    --manual-touch \
    --audit-monitor-image "${EARLYWATCH_AUDIT_IMAGE}"

  run kubectl -n early-watch-system rollout status deployment/early-watch-webhook --timeout=180s
  run kubectl -n early-watch-system rollout status deployment/early-watch-audit-monitor --timeout=180s
  run kubectl wait --for=condition=Established crd/changevalidators.earlywatch.io --timeout=120s
  run kubectl wait --for=condition=Established crd/clusterchangevalidators.earlywatch.io --timeout=120s
  run kubectl wait --for=condition=Established crd/manualtouchmonitors.earlywatch.io --timeout=120s
  run kubectl wait --for=condition=Established crd/manualtouchevents.earlywatch.io --timeout=120s

  kubectl get validatingwebhookconfiguration early-watch-validating-webhook -o yaml >"${ARTIFACT_DIR}/upstream-earlywatch-validatingwebhook.yaml"
  kubectl -n early-watch-system get deploy,svc,pods -o wide >"${ARTIFACT_DIR}/upstream-earlywatch-workloads.txt"
  log "Upstream EarlyWatch ${EARLYWATCH_UPSTREAM_COMMIT} deployed successfully"
}

verify_gatekeeper_helm_release() {
  local release_json="${ARTIFACT_DIR}/gatekeeper-helm-release.json"
  local chart_yaml="${ARTIFACT_DIR}/gatekeeper-chart-${GATEKEEPER_CHART_VERSION}.yaml"
  local summary_json="${ARTIFACT_DIR}/gatekeeper-helm-version.json"

  run helm -n gatekeeper-system list --filter '^gatekeeper$' -o json >"${release_json}"
  run helm show chart gatekeeper/gatekeeper --version "${GATEKEEPER_CHART_VERSION}" >"${chart_yaml}"

  ci_helper verify-gatekeeper-release "${release_json}" "${summary_json}" "${GATEKEEPER_CHART_VERSION}"
}

json_patch_gatekeeper_args() {
  local deploy_json="${WORK_DIR}/gatekeeper-deploy.json"
  local patch_json="${WORK_DIR}/gatekeeper-args-patch.json"

  kubectl -n gatekeeper-system get deployment gatekeeper-controller-manager -o json >"${deploy_json}"
  ci_helper gatekeeper-args-patch "${deploy_json}" "${patch_json}"

  run kubectl -n gatekeeper-system patch deployment gatekeeper-controller-manager --type=json --patch-file "${patch_json}"
  run kubectl -n gatekeeper-system rollout status deployment/gatekeeper-controller-manager --timeout=180s
}

ensure_gatekeeper_delete_webhook() {
  local vwc
  vwc="$(kubectl get validatingwebhookconfiguration -o name | grep -E 'gatekeeper.*validating' | head -n1 || true)"
  if [[ -z "${vwc}" ]]; then
    printf 'could not find Gatekeeper ValidatingWebhookConfiguration\n' >&2
    exit 1
  fi

  local vwc_json="${WORK_DIR}/gatekeeper-vwc.json"
  local patch_json="${WORK_DIR}/gatekeeper-vwc-patch.json"
  kubectl get "${vwc}" -o json >"${vwc_json}"
  ci_helper gatekeeper-vwc-patch "${vwc_json}" "${patch_json}"

  if [[ "$(cat "${patch_json}")" != "[]" ]]; then
    run kubectl patch "${vwc}" --type=json --patch-file "${patch_json}"
  else
    log "Gatekeeper validating webhook already includes DELETE"
  fi
}

install_gatekeeper() {
  run helm repo add gatekeeper https://open-policy-agent.github.io/gatekeeper/charts
  run helm repo update gatekeeper

  local helm_args=(upgrade --install gatekeeper gatekeeper/gatekeeper
    --namespace gatekeeper-system
    --create-namespace
    --wait
    --timeout 5m
    --set enableExternalData=true)
  if [[ -n "${GATEKEEPER_CHART_VERSION}" ]]; then
    helm_args+=(--version "${GATEKEEPER_CHART_VERSION}")
  fi
  run helm "${helm_args[@]}"
  verify_gatekeeper_helm_release

  run kubectl -n gatekeeper-system rollout status deployment/gatekeeper-controller-manager --timeout=180s
  run kubectl wait --for=condition=Established crd/providers.externaldata.gatekeeper.sh --timeout=120s
  json_patch_gatekeeper_args
  ensure_gatekeeper_delete_webhook
  wait_for_gatekeeper_webhook
}

wait_for_gatekeeper_webhook() {
  # A rollout can complete while old webhook pods are still terminating and the
  # apiserver may briefly route admission calls to a not-yet-listening endpoint.
  # Exercise a harmless server-side dry-run until the webhook service is usable.
  run kubectl -n gatekeeper-system wait \
    --for=condition=Ready pod \
    -l app=gatekeeper,control-plane=controller-manager \
    --timeout=180s

  local output=""
  local rc=0
  for i in {1..30}; do
    log "Waiting for Gatekeeper webhook admission readiness (attempt ${i}/30)"
    set +e
    output="$(kubectl create namespace gatekeeper-earlywatch-webhook-probe --dry-run=server -o yaml 2>&1 >/dev/null)"
    rc=$?
    set -e
    if [[ "${rc}" -eq 0 ]]; then
      log "Gatekeeper webhook admission is ready"
      return 0
    fi
    sleep 2
  done

  printf 'Gatekeeper webhook did not become ready for admission. Last error:\n%s\n' "${output}" >&2
  return 1
}

create_namespace() {
  local ns="$1"
  if kubectl get namespace "${ns}" >/dev/null 2>&1; then
    log "Namespace ${ns} already exists"
    return 0
  fi

  for i in {1..30}; do
    log "Creating namespace ${ns} (attempt ${i}/30)"
    if kubectl create namespace "${ns}"; then
      return 0
    fi
    if kubectl get namespace "${ns}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done

  printf 'failed to create namespace: %s\n' "${ns}" >&2
  return 1
}

generate_provider_certs() {
  local cert_dir="${WORK_DIR}/certs"
  mkdir -p "${cert_dir}"

  run openssl genrsa -out "${cert_dir}/ca.key" 4096
  run openssl req -x509 -new -nodes -key "${cert_dir}/ca.key" -sha256 -days 7 \
    -subj "/CN=gatekeeper-earlywatch-ci-provider-ca" -out "${cert_dir}/ca.crt"

  generate_service_cert() {
    local name="$1"
    local namespace="$2"
    local out_dir="${cert_dir}/${name}"
    mkdir -p "${out_dir}"

    cat >"${out_dir}/csr.conf" <<EOF_CERT
[req]
prompt = no
distinguished_name = dn
req_extensions = v3_req

[dn]
CN = ${name}.${namespace}.svc

[v3_req]
basicConstraints = CA:FALSE
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = ${name}
DNS.2 = ${name}.${namespace}
DNS.3 = ${name}.${namespace}.svc
DNS.4 = ${name}.${namespace}.svc.cluster.local
EOF_CERT

    run openssl genrsa -out "${out_dir}/tls.key" 2048
    run openssl req -new -key "${out_dir}/tls.key" -out "${out_dir}/tls.csr" -config "${out_dir}/csr.conf"
    run openssl x509 -req -in "${out_dir}/tls.csr" \
      -CA "${cert_dir}/ca.crt" -CAkey "${cert_dir}/ca.key" -CAcreateserial \
      -out "${out_dir}/tls.crt" -days 7 -sha256 \
      -extensions v3_req -extfile "${out_dir}/csr.conf"
  }

  create_namespace gatekeeper-system
  create_namespace early-watch-system
  generate_service_cert approval-verifier gatekeeper-system
  generate_service_cert manual-touch-monitor early-watch-system

  kubectl -n gatekeeper-system create secret tls approval-verifier-tls \
    --cert="${cert_dir}/approval-verifier/tls.crt" \
    --key="${cert_dir}/approval-verifier/tls.key" \
    --dry-run=client -o yaml | kubectl apply -f -
  kubectl -n early-watch-system create secret tls manual-touch-monitor-tls \
    --cert="${cert_dir}/manual-touch-monitor/tls.crt" \
    --key="${cert_dir}/manual-touch-monitor/tls.key" \
    --dry-run=client -o yaml | kubectl apply -f -

  base64 <"${cert_dir}/ca.crt" | tr -d '\n' >"${WORK_DIR}/provider-ca-bundle.txt"
}

generate_approval_keypair() {
  run openssl genrsa -out "${WORK_DIR}/approval-private.pem" 2048
  run openssl rsa -in "${WORK_DIR}/approval-private.pem" -pubout -out "${WORK_DIR}/approval-public.pem"
}

sign_message() {
  local message="$1"
  printf '%s' "${message}" | openssl dgst -sha256 -sign "${WORK_DIR}/approval-private.pem" \
    -sigopt rsa_padding_mode:pss -sigopt rsa_pss_saltlen:-1 | base64 | tr -d '\n'
}

write_local_gator_catalog() {
  local local_catalog="${WORK_DIR}/catalog.yaml"

  # catalog.yaml is committed with raw.githubusercontent.com URLs so releases
  # can be installed remotely. CI must exercise the checked-out PR contents, so
  # rewrite those content URLs to file:// URLs in an isolated temporary catalog.
  python3 - "${ROOT}" "${local_catalog}" <<'PY'
from pathlib import Path
import sys

root = Path(sys.argv[1]).resolve()
out = Path(sys.argv[2])
source = root / "catalog.yaml"
base = "https://raw.githubusercontent.com/sozercan/gatekeeper-earlywatch/main"
root_uri = root.as_uri()

text = source.read_text()
rewritten = text.replace(base, root_uri)
if rewritten == text:
    raise SystemExit(f"did not find expected catalog base URL {base!r} in {source}")
if "https://raw.githubusercontent.com/sozercan/gatekeeper-earlywatch" in rewritten:
    raise SystemExit("catalog still contains remote gatekeeper-earlywatch raw URLs after rewrite")

out.write_text(rewritten)
PY

  printf '%s\n' "${local_catalog}"
}

file_uri() {
  python3 - "$1" <<'PY'
from pathlib import Path
import sys

print(Path(sys.argv[1]).resolve().as_uri())
PY
}

install_stack() {
  log "Applying EarlyWatch runtime manifests (non-policy resources)"
  kubectl apply \
    -f library/gatekeeper-earlywatch/config-sync.yaml \
    -f library/gatekeeper-earlywatch/manual-touch-check/provider.yaml \
    -f provider/manifests/deployment.yaml \
    -f provider/manifests/provider.yaml \
    -f provider/manifests/hardening.yaml \
    -f touch-monitor/manifests/audit-monitor.yaml \
    >"${ARTIFACT_DIR}/runtime-apply.log" 2>&1
  cat "${ARTIFACT_DIR}/runtime-apply.log"

  local local_catalog catalog_uri gator_home
  local_catalog="$(write_local_gator_catalog)"
  catalog_uri="$(file_uri "${local_catalog}")"
  gator_home="${WORK_DIR}/gator"
  mkdir -p "${gator_home}"

  log "Refreshing gator policy catalog from local checkout"
  GATOR_HOME="${gator_home}" GATOR_CATALOG_URL="${catalog_uri}" \
    gator policy update >"${ARTIFACT_DIR}/gator-policy-update.log" 2>&1
  cat "${ARTIFACT_DIR}/gator-policy-update.log"

  log "Installing EarlyWatch default policy bundle with gator"
  GATOR_HOME="${gator_home}" GATOR_CATALOG_URL="${catalog_uri}" \
    gator policy install --bundle earlywatch-default \
    >"${ARTIFACT_DIR}/gator-policy-install.log" 2>&1
  cat "${ARTIFACT_DIR}/gator-policy-install.log"

  run kubectl wait --for=jsonpath='{.status.created}'=true constrainttemplates.templates.gatekeeper.sh --all --timeout=180s

  # Required image overrides for the locally-built provider images. The base
  # manifests use a :latest image, which Kubernetes defaults to imagePullPolicy:
  # Always; reset the policy so kind uses the images loaded into the node.
  kubectl -n gatekeeper-system set image deployment/approval-verifier provider="${APPROVAL_IMAGE}"
  kubectl -n early-watch-system set image deployment/manual-touch-monitor monitor="${TOUCH_IMAGE}"
  run kubectl -n gatekeeper-system patch deployment/approval-verifier --type=json \
    -p='[{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"}]'
  run kubectl -n early-watch-system patch deployment/manual-touch-monitor --type=json \
    -p='[{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"}]'

  local ca_bundle
  ca_bundle="$(cat "${WORK_DIR}/provider-ca-bundle.txt")"
  run kubectl patch provider.externaldata.gatekeeper.sh approval-verifier --type=merge \
    -p "{\"spec\":{\"caBundle\":\"${ca_bundle}\"}}"
  run kubectl patch provider.externaldata.gatekeeper.sh manual-touch-provider --type=merge \
    -p "{\"spec\":{\"caBundle\":\"${ca_bundle}\"}}"

  ci_helper approval-constraint-patch "${WORK_DIR}/approval-public.pem" "${WORK_DIR}/approval-constraint-patch.json"
  run kubectl patch ewapprovalcheck.constraints.gatekeeper.sh require-approval-signature \
    --type=merge --patch-file "${WORK_DIR}/approval-constraint-patch.json"

  run kubectl -n gatekeeper-system rollout status deployment/approval-verifier --timeout=180s
  run kubectl -n early-watch-system rollout status deployment/manual-touch-monitor --timeout=180s

  # Give Gatekeeper a short window to observe patched providers/constraints.
  sleep 5
}

command_output_file() {
  local desc="$1"
  printf '%s/%s.log' "${ARTIFACT_DIR}/assertions" "$(slugify "${desc}")"
}

assert_denied() {
  local desc="$1"
  local expected="$2"
  shift 2
  local outfile
  outfile="$(command_output_file "${desc}")"
  log "ASSERT denied: ${desc}"

  set +e
  local output
  output="$({ "$@"; } 2>&1)"
  local rc=$?
  set -e
  printf '%s\n' "${output}" >"${outfile}"

  if [[ "${rc}" -eq 0 ]]; then
    printf 'expected command to be denied, but it succeeded: %s\n' "${desc}" >&2
    cat "${outfile}" >&2
    return 1
  fi
  if [[ -n "${expected}" && "${output}" != *"${expected}"* ]]; then
    printf 'denial for %s did not include expected text: %s\n' "${desc}" "${expected}" >&2
    cat "${outfile}" >&2
    return 1
  fi
  log "denied as expected: ${desc}"
}

assert_allowed() {
  local desc="$1"
  shift
  local outfile
  outfile="$(command_output_file "${desc}")"
  log "ASSERT allowed: ${desc}"

  set +e
  local output
  output="$({ "$@"; } 2>&1)"
  local rc=$?
  set -e
  printf '%s\n' "${output}" >"${outfile}"

  if [[ "${rc}" -ne 0 ]]; then
    printf 'expected command to be allowed, but it failed: %s\n' "${desc}" >&2
    cat "${outfile}" >&2
    return 1
  fi
  log "allowed as expected: ${desc}"
}

retry_denied() {
  local desc="$1"
  local expected="$2"
  local attempts="$3"
  local delay="$4"
  shift 4
  local outfile
  outfile="$(command_output_file "${desc}")"

  for ((i = 1; i <= attempts; i++)); do
    log "ASSERT denied (attempt ${i}/${attempts}): ${desc}"
    set +e
    local output
    output="$({ "$@"; } 2>&1)"
    local rc=$?
    set -e
    printf '%s\n' "${output}" >"${outfile}"

    if [[ "${rc}" -ne 0 && ( -z "${expected}" || "${output}" == *"${expected}"* ) ]]; then
      log "denied as expected: ${desc}"
      return 0
    fi
    sleep "${delay}"
  done

  printf 'expected command to be denied after retries: %s\n' "${desc}" >&2
  cat "${outfile}" >&2
  return 1
}

create_ci_namespaces() {
  for ns in \
    ci-existing \
    ci-name-ref \
    ci-annotation \
    ci-approval \
    ci-lock \
    ci-manual \
    ci-service-selector \
    ci-data-key \
    production; do
    create_namespace "${ns}"
  done
}

exercise_existing_resources() {
  log "Preparing ExistingResources fixtures"
  kubectl -n ci-existing apply -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: guarded-pod
  labels:
    app: guarded
    tier: production
spec:
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.9
---
apiVersion: v1
kind: Service
metadata:
  name: guarded-service
spec:
  selector:
    app: guarded
  ports:
    - port: 80
      targetPort: 80
YAML

  retry_denied "ExistingResources denies deleting Service with dependent Pod" "dependent Pod" 30 2 \
    kubectl -n ci-existing delete service guarded-service --dry-run=server
}

exercise_name_reference() {
  log "Preparing NameReference fixtures"
  kubectl -n ci-name-ref apply -f - <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: referenced-cm
data:
  config.yaml: ok
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cm-consumer
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cm-consumer
  template:
    metadata:
      labels:
        app: cm-consumer
    spec:
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.9
          envFrom:
            - configMapRef:
                name: referenced-cm
          volumeMounts:
            - name: cfg
              mountPath: /cfg
      volumes:
        - name: cfg
          configMap:
            name: referenced-cm
YAML

  retry_denied "NameReference denies deleting referenced ConfigMap" "still references it" 30 2 \
    kubectl -n ci-name-ref delete configmap referenced-cm --dry-run=server
}

exercise_annotation_check() {
  assert_denied "AnnotationCheck denies Namespace delete without confirmation annotation" "earlywatch.io/confirm-delete=yes is required" \
    kubectl delete namespace ci-annotation --dry-run=server
  run kubectl annotate namespace ci-annotation earlywatch.io/confirm-delete=yes --overwrite
}

exercise_approval_check() {
  log "Preparing ApprovalCheck fixtures"
  kubectl -n ci-approval create configmap missing-delete --from-literal=value=old --dry-run=client -o yaml | kubectl apply -f -
  assert_denied "ApprovalCheck denies ConfigMap delete without approval" "missing delete-approval" \
    kubectl -n ci-approval delete configmap missing-delete --dry-run=server

  local delete_sig
  delete_sig="$(sign_message "v1/namespaces/ci-approval/configmaps/approved-delete")"
  cat <<YAML | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: approved-delete
  namespace: ci-approval
  annotations:
    earlywatch.io/approved: "${delete_sig}"
data:
  value: old
YAML
  assert_allowed "ApprovalCheck allows ConfigMap delete with valid approval" \
    kubectl -n ci-approval delete configmap approved-delete --dry-run=server

  kubectl -n ci-approval create configmap approved-update --from-literal=value=old --dry-run=client -o yaml | kubectl apply -f -
  assert_denied "ApprovalCheck denies ConfigMap update without change approval" "missing change-approval" \
    kubectl -n ci-approval patch configmap approved-update --type=merge \
      -p '{"data":{"value":"new"}}' --dry-run=server

  local update_sig
  update_sig="$(sign_message '{"data":{"value":"new"}}')"
  assert_allowed "ApprovalCheck allows ConfigMap update with valid change approval" \
    kubectl -n ci-approval patch configmap approved-update --type=merge \
      -p "{\"data\":{\"value\":\"new\"},\"metadata\":{\"annotations\":{\"earlywatch.io/change-approved\":\"${update_sig}\"}}}" \
      --dry-run=server
}

exercise_check_lock() {
  log "Preparing CheckLock fixtures"
  kubectl -n ci-lock apply -f - <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: locked
  annotations:
    earlywatch.io/lock: ci
spec:
  replicas: 1
  selector:
    matchLabels:
      app: locked
  template:
    metadata:
      labels:
        app: locked
    spec:
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.9
YAML

  assert_denied "CheckLock denies deleting locked Deployment" "delete denied" \
    kubectl -n ci-lock delete deployment locked --dry-run=server
  assert_denied "CheckLock denies mutating locked Deployment" "update denied" \
    kubectl -n ci-lock annotate deployment locked ci-update=blocked --dry-run=server --overwrite
  assert_allowed "CheckLock allows unlock-only Deployment update" \
    kubectl -n ci-lock annotate deployment locked earlywatch.io/lock- --dry-run=server --overwrite
  retry_denied "CheckLock denies Scale update for locked Deployment" "update denied" 30 2 \
    kubectl -n ci-lock scale deployment locked --replicas=2 --dry-run=server
}

exercise_expression_check() {
  kubectl -n production create configmap expr-target --from-literal=value=1 --dry-run=client -o yaml | kubectl apply -f -
  assert_denied "ExpressionCheck denies ConfigMap delete in production namespace" "not permitted" \
    kubectl -n production delete configmap expr-target --dry-run=server
}

start_manual_touch_port_forward() {
  if [[ -n "${PORT_FORWARD_PID}" ]]; then
    return 0
  fi
  log "Starting port-forward to manual-touch-monitor"
  kubectl -n early-watch-system port-forward svc/manual-touch-monitor 18443:8443 \
    >"${ARTIFACT_DIR}/logs/manual-touch-port-forward.log" 2>&1 &
  PORT_FORWARD_PID=$!

  for _ in {1..30}; do
    if curl -skSf https://127.0.0.1:18443/healthz >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done

  cat "${ARTIFACT_DIR}/logs/manual-touch-port-forward.log" >&2 || true
  printf 'manual-touch-monitor port-forward did not become ready\n' >&2
  return 1
}

post_manual_touch_audit_event() {
  local payload="${WORK_DIR}/manual-touch-audit.json"
  ci_helper manual-touch-audit-event "${payload}" "${MANUAL_TOUCH_USER_AGENT}"
  run curl -skSf -X POST https://127.0.0.1:18443/audit \
    -H 'Content-Type: application/json' \
    --data-binary "@${payload}"
}

exercise_manual_touch_check() {
  log "Preparing ManualTouchCheck fixtures"
  kubectl -n ci-manual apply -f - <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: touched
spec:
  replicas: 1
  selector:
    matchLabels:
      app: touched
  template:
    metadata:
      labels:
        app: touched
    spec:
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.9
YAML

  start_manual_touch_port_forward
  post_manual_touch_audit_event
  retry_denied "ManualTouchCheck denies update after kubectl-like audit touch" "manually touched" 30 2 \
    kubectl -n ci-manual annotate deployment touched manual-touch-ci=blocked --dry-run=server --overwrite
}

exercise_service_pod_selector_check() {
  log "Preparing ServicePodSelectorCheck fixtures"
  kubectl -n ci-service-selector apply -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: selected-pod
  labels:
    app: selector-old
spec:
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.9
---
apiVersion: v1
kind: Service
metadata:
  name: selector-service
spec:
  selector:
    app: selector-old
  ports:
    - port: 80
      targetPort: 80
YAML

  retry_denied "ServicePodSelectorCheck denies Service selector update to zero Pods" "zero matching Pods" 30 2 \
    kubectl -n ci-service-selector patch service selector-service --type=merge \
      -p '{"spec":{"selector":{"app":"selector-new"}}}' --dry-run=server
}

exercise_data_key_safety_check() {
  log "Preparing DataKeySafetyCheck fixtures"
  kubectl -n ci-data-key apply -f - <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: settings
data:
  keep: ok
  drop: still-used
---
apiVersion: v1
kind: Pod
metadata:
  name: key-consumer
spec:
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.9
      env:
        - name: FROM_CONFIG
          valueFrom:
            configMapKeyRef:
              name: settings
              key: drop
YAML

  retry_denied "DataKeySafetyCheck denies dropping still-referenced ConfigMap key" "cannot drop key" 30 2 \
    kubectl -n ci-data-key patch configmap settings --type=json \
      -p '[{"op":"remove","path":"/data/drop"}]' --dry-run=server
}

run_live_assertions() {
  create_ci_namespaces
  exercise_existing_resources
  exercise_name_reference
  exercise_annotation_check
  exercise_approval_check
  exercise_check_lock
  exercise_expression_check
  exercise_manual_touch_check
  exercise_service_pod_selector_check
  exercise_data_key_safety_check
}

main() {
  require_commands
  build_ci_helper
  create_kind_cluster
  write_parity_coverage_artifact
  deploy_upstream_earlywatch
  build_and_load_images
  install_gatekeeper
  generate_provider_certs
  generate_approval_keypair
  install_stack
  run_live_assertions
  log "CI kind parity validation completed successfully"
}

main "$@"
