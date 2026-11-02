#!/usr/bin/env bash
set -euo pipefail

NS_NAMESPACE="${NS_NAMESPACE:-nse-ovs-lb}"
CONFIGMAP_NAME="${CONFIGMAP_NAME:-nse-ovs-lb-rules}"
DEPLOYMENT_NAME="${DEPLOYMENT_NAME:-nse-ovs-lb}"
PROFILE=""
WAIT_TIMEOUT="${WAIT_TIMEOUT:-120s}"
POD_FILE_WAIT_SECONDS="${POD_FILE_WAIT_SECONDS:-90}"
RELOAD_SETTLE_SECONDS="${RELOAD_SETTLE_SECONDS:-5}"
RESTART_DEPLOYMENT="false"
KUBECTL="${KUBECTL:-kubectl}"

usage() {
  cat <<'EOF'
Usage: ./scripts/set_lb_profile.sh --profile <baseline|empty> [--restart] [--timeout <duration>]

Update the live LB ConfigMap profile. By default the LB pod hot-reloads the mounted
rules file without restarting the deployment.

Options:
  --profile <name>    Required. baseline or empty
  --restart           Restart the LB deployment after updating the ConfigMap
  --no-restart        Deprecated alias for the default live-reload behavior
  --timeout <value>   Rollout status timeout. Default: 120s
  -h, --help          Show this help text
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile)
      PROFILE="${2:-}"
      shift 2
      ;;
    --restart)
      RESTART_DEPLOYMENT="true"
      shift
      ;;
    --no-restart)
      RESTART_DEPLOYMENT="false"
      shift
      ;;
    --timeout)
      WAIT_TIMEOUT="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

case "${PROFILE}" in
  baseline|empty)
    ;;
  *)
    echo "invalid or missing profile: ${PROFILE:-<empty>}" >&2
    usage >&2
    exit 1
    ;;
esac

run_kubectl() {
  "${KUBECTL}" "$@"
}

current_lb_pod() {
  run_kubectl get pods -n "${NS_NAMESPACE}" \
    -l app=nse-ovs-lb \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true
}

current_rules_content() {
  local pod_name
  pod_name="$(current_lb_pod)"
  [[ -n "${pod_name}" ]] || return 1

  run_kubectl exec -n "${NS_NAMESPACE}" "${pod_name}" -c nse -- \
    sh -c "cat /etc/nse-lb/rules.yaml 2>/dev/null" 2>/dev/null || true
}

wait_for_rules_content() {
  local expected_file="$1"
  local deadline=$((SECONDS + POD_FILE_WAIT_SECONDS))
  local expected_content current_content

  expected_content="$(cat "${expected_file}")"

  while (( SECONDS < deadline )); do
    current_content="$(current_rules_content || true)"
    if [[ "${current_content}" == "${expected_content}" ]]; then
      return 0
    fi
    sleep 2
  done

  return 1
}

write_profile() {
  local profile_name="$1"
  local target_file="$2"

  case "${profile_name}" in
    baseline)
      cat >"${target_file}" <<'EOF'
mode: static_one_to_one
vip: 172.16.4.10
backend_ip: 172.16.4.100
EOF
      ;;
    empty)
      cat >"${target_file}" <<'EOF'
mode: empty
EOF
      ;;
  esac
}

tmp_rules="$(mktemp)"
trap 'rm -f "${tmp_rules}"' EXIT
write_profile "${PROFILE}" "${tmp_rules}"

echo "== Apply ConfigMap =="
echo "Namespace : ${NS_NAMESPACE}"
echo "ConfigMap : ${CONFIGMAP_NAME}"
echo "Profile   : ${PROFILE}"

run_kubectl create configmap "${CONFIGMAP_NAME}" \
  -n "${NS_NAMESPACE}" \
  --from-file=rules.yaml="${tmp_rules}" \
  --dry-run=client -o yaml | run_kubectl apply -f -

if [[ "${RESTART_DEPLOYMENT}" == "true" ]]; then
  echo "== Restart Deployment =="
  run_kubectl rollout restart deployment/"${DEPLOYMENT_NAME}" -n "${NS_NAMESPACE}"
  run_kubectl rollout status deployment/"${DEPLOYMENT_NAME}" -n "${NS_NAMESPACE}" --timeout="${WAIT_TIMEOUT}"
else
  echo "== Wait For Live Reload =="
  if ! wait_for_rules_content "${tmp_rules}"; then
    echo "lb pod did not observe profile=${PROFILE} within ${POD_FILE_WAIT_SECONDS}s" >&2
    exit 1
  fi

  if [[ "${RELOAD_SETTLE_SECONDS}" != "0" ]]; then
    sleep "${RELOAD_SETTLE_SECONDS}"
  fi
fi

echo "== Live Rules =="
run_kubectl get configmap "${CONFIGMAP_NAME}" -n "${NS_NAMESPACE}" -o jsonpath='{.data.rules\.yaml}'
printf '\n'
