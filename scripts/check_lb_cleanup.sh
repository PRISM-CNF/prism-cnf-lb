#!/usr/bin/env bash
set -euo pipefail

NS_NAMESPACE="${NS_NAMESPACE:-nse-ovs-lb}"
LB_POD_PREFIX="${LB_POD_PREFIX:-nse-ovs-lb-}"
LB_CONTAINER="${LB_CONTAINER:-nse}"
WAIT_TIMEOUT_SECONDS="${WAIT_TIMEOUT_SECONDS:-60}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-2}"
KUBECTL="${KUBECTL:-kubectl}"

usage() {
  cat <<'EOF'
Usage: ./scripts/check_lb_cleanup.sh [--timeout-seconds <n>] [--poll-seconds <n>]

Wait until the running LB pod has no active session bridge or data interfaces.

Options:
  --timeout-seconds <n> Timeout in seconds. Default: 60
  --poll-seconds <n>    Poll interval in seconds. Default: 2
  -h, --help            Show this help text
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --timeout-seconds)
      WAIT_TIMEOUT_SECONDS="${2:-}"
      shift 2
      ;;
    --poll-seconds)
      POLL_INTERVAL_SECONDS="${2:-}"
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

run_kubectl() {
  "${KUBECTL}" "$@"
}

get_lb_pod() {
  run_kubectl get pods -n "${NS_NAMESPACE}" --no-headers \
    -o custom-columns=NAME:.metadata.name,PHASE:.status.phase \
    | awk -v prefix="${LB_POD_PREFIX}" '$1 ~ ("^" prefix) && $2 == "Running" { print $1; exit }'
}

list_lb_session_links() {
  local pod_name="$1"
  run_kubectl exec -n "${NS_NAMESPACE}" "${pod_name}" -c "${LB_CONTAINER}" -- \
    sh -eu -c "ip -o link show | awk -F': ' '
      {
        name = \$2
        sub(/ .*/, \"\", name)
        sub(/@.*/, \"\", name)
        if (name ~ /^nse-ovs-/ || name ~ /^nsmbr/) {
          print name
        }
      }'"
}

lb_pod="$(get_lb_pod)"
[[ -n "${lb_pod}" ]] || { echo "running lb pod not found in namespace ${NS_NAMESPACE}" >&2; exit 1; }

deadline=$((SECONDS + WAIT_TIMEOUT_SECONDS))
while (( SECONDS < deadline )); do
  active_links="$(list_lb_session_links "${lb_pod}")"
  if [[ -z "${active_links}" ]]; then
    echo "LB cleanup check: PASS"
    echo "LB pod: ${lb_pod}"
    exit 0
  fi
  sleep "${POLL_INTERVAL_SECONDS}"
done

echo "lb cleanup timed out after ${WAIT_TIMEOUT_SECONDS}s" >&2
list_lb_session_links "${lb_pod}" >&2 || true
exit 1
