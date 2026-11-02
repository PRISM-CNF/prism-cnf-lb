#!/usr/bin/env bash
set -euo pipefail

NS_NAMESPACE="${NS_NAMESPACE:-nse-ovs-lb}"
NSM_SYSTEM_NAMESPACE="${NSM_SYSTEM_NAMESPACE:-nsm-system}"
CLIENT_POD_PREFIX="${CLIENT_POD_PREFIX:-nsc-ovs-client-}"
LB_POD_PREFIX="${LB_POD_PREFIX:-nse-ovs-lb-}"
SERVER_POD_PREFIX="${SERVER_POD_PREFIX:-nse-ovs-server-}"
CLIENT_IFACE="${CLIENT_IFACE:-nsm-1}"
SERVER_IFACE="${SERVER_IFACE:-nsm-1}"
CLIENT_CONTAINER="${CLIENT_CONTAINER:-client}"
CLIENT_CMD_CONTAINER="${CLIENT_CMD_CONTAINER:-cmd-nsc}"
LB_CONTAINER="${LB_CONTAINER:-nse}"
SERVER_CONTAINER="${SERVER_CONTAINER:-server}"
LB_RULES_PATH="${LB_RULES_PATH:-/etc/nse-lb/rules.yaml}"
HTTP_PORT="${HTTP_PORT:-8080}"
HTTP_SERVER_ROOT="${HTTP_SERVER_ROOT:-/tmp/nse-lb-http}"
IPERF_PORT="${IPERF_PORT:-5201}"
WAIT_TIMEOUT_SECONDS="${WAIT_TIMEOUT_SECONDS:-60}"
PROOF_WINDOW_SECONDS="${PROOF_WINDOW_SECONDS:-3}"
PROOF_CONN_WAIT_SECONDS="${PROOF_CONN_WAIT_SECONDS:-10}"
ARTIFACT_DIR="${ARTIFACT_DIR:-}"
KUBECTL="${KUBECTL:-kubectl}"
DEFAULT_VIP="${DEFAULT_VIP:-172.16.4.10}"
EXPECT_RESULT="success"
PRINT_TC="true"
VIP_INPUT=""
BACKEND_IP_INPUT=""

forward_tcp_pref=110
reverse_tcp_pref=210

usage() {
  cat <<'EOF'
Usage: ./scripts/verify_same_node_chain.sh [--expect-success|--expect-fail] [--vip <ip>] [--backend-ip <ip>] [--artifact-dir <path>] [--proof-window-seconds <n>] [--no-print-tc]

Verify the same-node NSM OVS LB chain and prove the current single-VIP LB behavior
with direct-backend health, forward/reverse path counters, tc hit counters, and
client/server socket snapshots.

Options:
  --expect-success             Expect VIP access to succeed under the current profile. Default.
  --expect-fail                Expect VIP access to fail under the current profile.
  --vip <ip>                   Override VIP instead of reading it from the LB pod rules file.
  --backend-ip <ip>            Override backend IP instead of reading it from the server interface.
  --artifact-dir <path>        Persist raw proof artifacts into the given directory.
  --proof-window-seconds <n>   Keep the proof iperf3 flow open long enough for snapshots. Default: 3
  --no-print-tc                Skip printing the full tc dumps in stdout.
  -h, --help                   Show this help text.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --expect-success)
      EXPECT_RESULT="success"
      shift
      ;;
    --expect-fail)
      EXPECT_RESULT="fail"
      shift
      ;;
    --vip)
      VIP_INPUT="${2:-}"
      shift 2
      ;;
    --backend-ip)
      BACKEND_IP_INPUT="${2:-}"
      shift 2
      ;;
    --artifact-dir)
      ARTIFACT_DIR="${2:-}"
      shift 2
      ;;
    --proof-window-seconds)
      PROOF_WINDOW_SECONDS="${2:-}"
      shift 2
      ;;
    --no-print-tc)
      PRINT_TC="false"
      shift
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

tmpdir="$(mktemp -d)"

die() {
  echo "ERROR: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

run_kubectl() {
  "${KUBECTL}" "$@"
}

sanitize_name() {
  local value="$1"
  [[ "$value" =~ ^[A-Za-z0-9._:-]+$ ]] || die "unexpected name: $value"
}

trim() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value}"
}

fmt_count() {
  local value="$1"
  local sign=""
  local digits="$value"
  if [[ "${digits}" == -* ]]; then
    sign="-"
    digits="${digits#-}"
  fi
  local out=""
  while ((${#digits} > 3)); do
    out=",${digits: -3}${out}"
    digits="${digits:0:${#digits}-3}"
  done
  printf '%s%s%s' "${sign}" "${digits}" "${out}"
}

fmt_bytes_gib() {
  awk -v value="$1" 'BEGIN { printf "%.6f GiB", value / 1024 / 1024 / 1024 }'
}

kv_get() {
  local file="$1"
  local key="$2"
  awk -F'=' -v key="$key" '$1 == key { print $2; exit }' "$file"
}

delta_from_files() {
  local before_file="$1"
  local after_file="$2"
  local key="$3"
  local before_value after_value

  before_value="$(kv_get "$before_file" "$key")"
  after_value="$(kv_get "$after_file" "$key")"
  [[ -n "${before_value}" ]] || die "missing before counter ${key} in ${before_file}"
  [[ -n "${after_value}" ]] || die "missing after counter ${key} in ${after_file}"

  echo $((after_value - before_value))
}

persist_artifact() {
  local source_file="$1"
  local target_name="$2"
  if [[ -z "${ARTIFACT_DIR}" ]]; then
    return 0
  fi

  mkdir -p "${ARTIFACT_DIR}"
  cp "${source_file}" "${ARTIFACT_DIR}/${target_name}"
}

get_running_pod_by_prefix() {
  local namespace="$1"
  local prefix="$2"
  run_kubectl get pods -n "${namespace}" --no-headers \
    -o custom-columns=NAME:.metadata.name,PHASE:.status.phase \
    | awk -v prefix="${prefix}" '$1 ~ ("^" prefix) && $2 == "Running" { print $1; exit }'
}

get_forwarder_pod_on_node() {
  local node_name="$1"
  run_kubectl get pods -n "${NSM_SYSTEM_NAMESPACE}" --no-headers \
    -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,NODE:.spec.nodeName \
    | awk -v node_name="${node_name}" '$1 ~ /^forwarder-ovs-/ && $2 == "Running" && $3 == node_name { print $1; exit }'
}

require_container() {
  local namespace="$1"
  local pod_name="$2"
  local container_name="$3"
  if ! run_kubectl get pod -n "${namespace}" "${pod_name}" \
    -o jsonpath='{range .spec.containers[*]}{.name}{"\n"}{end}' | grep -Fxq "${container_name}"; then
    die "container ${container_name} not found in pod ${namespace}/${pod_name}"
  fi
}

pod_sh() {
  local namespace="$1"
  local pod_name="$2"
  local container_name="$3"
  local command="$4"
  run_kubectl exec -n "${namespace}" "${pod_name}" -c "${container_name}" -- sh -eu -c "${command}"
}

list_named_links() {
  local namespace="$1"
  local pod_name="$2"
  local container_name="$3"
  local pattern="$4"
  pod_sh "${namespace}" "${pod_name}" "${container_name}" "
    ip -o link show | awk -F': ' '
      {
        name = \$2
        sub(/ .*/, \"\", name)
        sub(/@.*/, \"\", name)
        if (name ~ /${pattern}/) {
          print \$0
        }
      }'
  "
}

resolve_server_iface() {
  local namespace="$1"
  local pod_name="$2"
  local container_name="$3"
  local preferred_iface="${4:-}"

  if [[ -n "${preferred_iface}" ]] && pod_sh "${namespace}" "${pod_name}" "${container_name}" \
    "ip -o link show dev '${preferred_iface}' >/dev/null 2>&1"; then
    printf '%s\n' "${preferred_iface}"
    return 0
  fi

  local link_line
  link_line="$(trim "$(list_named_links "${namespace}" "${pod_name}" "${container_name}" '^nse-ovs-lb-' | head -n1)")"
  [[ -n "${link_line}" ]] || return 1

  parse_link_name "${link_line}"
}

parse_link_name() {
  local line="$1"
  local token
  token="$(printf '%s\n' "${line}" | awk -F': ' '{print $2}' | awk '{print $1}')"
  token="${token%:}"
  printf '%s' "${token%%@*}"
}

parse_link_peer_ifindex() {
  local line="$1"
  local token
  token="$(printf '%s\n' "${line}" | awk -F': ' '{print $2}' | awk '{print $1}')"
  token="${token%:}"
  printf '%s\n' "${token}" | sed -n 's/.*@if\([0-9][0-9]*\)$/\1/p'
}

capture_netdev_stats() {
  local namespace="$1"
  local pod_name="$2"
  local container_name="$3"
  local iface_name="$4"
  sanitize_name "${iface_name}"
  pod_sh "${namespace}" "${pod_name}" "${container_name}" \
    "for key in rx_bytes rx_packets tx_bytes tx_packets; do printf '%s=%s\n' \"\$key\" \"\$(cat /sys/class/net/${iface_name}/statistics/\$key)\"; done"
}

capture_ofport_stats() {
  local forwarder_pod="$1"
  local ofport="$2"
  sanitize_name "${ofport}"
  run_kubectl exec -n "${NSM_SYSTEM_NAMESPACE}" "${forwarder_pod}" -- \
    ovs-ofctl dump-ports br-nsm "${ofport}" \
    | awk '
        /port [0-9]+: rx pkts=/ {
          gsub(/,/, "")
          for (i = 1; i <= NF; i++) {
            if ($i ~ /^pkts=/) {
              value = $i
              sub(/^pkts=/, "", value)
              rx_packets = value
            } else if ($i ~ /^bytes=/) {
              value = $i
              sub(/^bytes=/, "", value)
              rx_bytes = value
            }
          }
        }
        /tx pkts=/ {
          gsub(/,/, "")
          for (i = 1; i <= NF; i++) {
            if ($i ~ /^pkts=/) {
              value = $i
              sub(/^pkts=/, "", value)
              tx_packets = value
            } else if ($i ~ /^bytes=/) {
              value = $i
              sub(/^bytes=/, "", value)
              tx_bytes = value
            }
          }
        }
        END {
          printf "rx_bytes=%s\nrx_packets=%s\ntx_bytes=%s\ntx_packets=%s\n", rx_bytes, rx_packets, tx_bytes, tx_packets
        }'
}

save_snapshot() {
  local file_path="$1"
  shift
  "$@" >"${file_path}"
}

forwarder_port_name_for_ifindex() {
  local links_file="$1"
  local ifindex="$2"
  awk -F': ' -v ifindex="${ifindex}" '
    $1 == ifindex {
      split($2, fields, " ")
      name = fields[1]
      sub(/:$/, "", name)
      sub(/@.*/, "", name)
      print name
      exit
    }' "${links_file}"
}

port_conn_id() {
  local port_name="$1"
  printf '%s' "${port_name}" | sed -E 's/^tap(src|dst)-//'
}

lookup_ofport() {
  local forwarder_pod="$1"
  local port_name="$2"
  sanitize_name "${port_name}"
  trim "$(run_kubectl exec -n "${NSM_SYSTEM_NAMESPACE}" "${forwarder_pod}" -- ovs-vsctl get Interface "${port_name}" ofport)"
}

render_leg_line() {
  local label="$1"
  local pod_name="$2"
  local iface_name="$3"
  local detail="$4"
  printf "%-16s %-42s %-24s %s\n" "${label}" "${pod_name}" "${iface_name}" "${detail}"
}

stop_http_servers() {
  if [[ -z "${server_pod:-}" ]]; then
    return 0
  fi

  pod_sh "${NS_NAMESPACE}" "${server_pod}" "${SERVER_CONTAINER}" "
    for pidfile in \"${HTTP_SERVER_ROOT}\"/*.pid; do
      [ -f \"\$pidfile\" ] || continue
      kill \"\$(cat \"\$pidfile\")\" 2>/dev/null || true
    done
    rm -rf \"${HTTP_SERVER_ROOT}\"
  " >/dev/null 2>&1 || true
}

stop_iperf_server() {
  if [[ -z "${server_pod:-}" ]]; then
    return 0
  fi

  pod_sh "${NS_NAMESPACE}" "${server_pod}" "${SERVER_CONTAINER}" "
    pkill -x iperf3 2>/dev/null || true
  " >/dev/null 2>&1 || true
}

start_http_server_loop() {
  stop_http_servers
  pod_sh "${NS_NAMESPACE}" "${server_pod}" "${SERVER_CONTAINER}" "
    rm -rf \"${HTTP_SERVER_ROOT}\"
    mkdir -p \"${HTTP_SERVER_ROOT}\"
    cat >\"${HTTP_SERVER_ROOT}/loop-response.sh\" <<'EOF'
#!/bin/sh
printf '%b' 'HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Length: 14\r\n\r\nnse-ovs-lb-ok\n'
EOF
    chmod +x \"${HTTP_SERVER_ROOT}/loop-response.sh\"
    nc -lk -p \"${HTTP_PORT}\" -e \"${HTTP_SERVER_ROOT}/loop-response.sh\" >/tmp/nse-lb-http.log 2>&1 &
    echo \$! >\"${HTTP_SERVER_ROOT}/loop.pid\"
    sleep 1
  "
}

start_iperf_server() {
  stop_iperf_server
  pod_sh "${NS_NAMESPACE}" "${server_pod}" "${SERVER_CONTAINER}" "
    iperf3 -s -B '${backend_ip}' -p '${IPERF_PORT}' -D
    sleep 1
    ss -ltn sport = :${IPERF_PORT} | grep -F '${backend_ip}:${IPERF_PORT}' >/dev/null
  "
}

run_http_request_sync() {
  local target_ip="$1"
  local request_payload="$2"
  run_kubectl exec -n "${NS_NAMESPACE}" "${client_pod}" -c "${CLIENT_CONTAINER}" -- \
    sh -eu -c "printf '%b' '${request_payload}' | nc -w '$((PROOF_WINDOW_SECONDS + 8))' '${target_ip}' '${HTTP_PORT}'"
}

start_iperf_request_async() {
  local target_ip="$1"
  local output_file="$2"
  local proof_runtime_seconds=$((PROOF_WINDOW_SECONDS + 2))
  run_kubectl exec -n "${NS_NAMESPACE}" "${client_pod}" -c "${CLIENT_CONTAINER}" -- \
    sh -eu -c "timeout '$((proof_runtime_seconds + PROOF_CONN_WAIT_SECONDS + 5))' iperf3 -c '${target_ip}' -p '${IPERF_PORT}' -t '${proof_runtime_seconds}'" \
    >"${output_file}" 2>&1 &
  request_pid=$!
}

capture_ss_snapshot() {
  local namespace="$1"
  local pod_name="$2"
  local container_name="$3"
  local output_file="$4"
  pod_sh "${namespace}" "${pod_name}" "${container_name}" "ss -tnH state all 2>/dev/null || true" >"${output_file}"
}

wait_for_ss_match() {
  local namespace="$1"
  local pod_name="$2"
  local container_name="$3"
  local pattern="$4"
  local timeout_seconds="$5"
  local output_file="$6"
  local deadline=$((SECONDS + timeout_seconds))

  while ((SECONDS < deadline)); do
    capture_ss_snapshot "${namespace}" "${pod_name}" "${container_name}" "${output_file}"
    if grep -Fq "${pattern}" "${output_file}"; then
      return 0
    fi
    sleep 1
  done

  capture_ss_snapshot "${namespace}" "${pod_name}" "${container_name}" "${output_file}"
  return 1
}

ensure_no_ss_match() {
  local namespace="$1"
  local pod_name="$2"
  local container_name="$3"
  local pattern="$4"
  local timeout_seconds="$5"
  local output_file="$6"
  local deadline=$((SECONDS + timeout_seconds))

  while ((SECONDS < deadline)); do
    capture_ss_snapshot "${namespace}" "${pod_name}" "${container_name}" "${output_file}"
    if grep -Fq "${pattern}" "${output_file}"; then
      return 1
    fi
    sleep 1
  done

  capture_ss_snapshot "${namespace}" "${pod_name}" "${container_name}" "${output_file}"
  return 0
}

capture_tc_filter_stats() {
  local namespace="$1"
  local pod_name="$2"
  local container_name="$3"
  local iface_name="$4"
  local output_file="$5"
  sanitize_name "${iface_name}"
  pod_sh "${namespace}" "${pod_name}" "${container_name}" \
    "tc -s filter show dev '${iface_name}' ingress 2>/dev/null || true" >"${output_file}"
}

tc_pref_packets_from_file() {
  local file_path="$1"
  local pref="$2"
  awk -v pref="${pref}" '
    $0 ~ ("pref " pref " ") {
      in_filter = 1
      awaiting_stats = 0
      next
    }
    in_filter && /^filter pref / {
      in_filter = 0
      awaiting_stats = 0
    }
    in_filter && /Action statistics:/ {
      awaiting_stats = 1
      next
    }
    in_filter && awaiting_stats && /Sent / {
      for (i = 1; i <= NF; i++) {
        if ($i == "pkt" && i > 1) {
          print $(i - 1)
          found = 1
          exit
        }
      }
      awaiting_stats = 0
    }
    END {
      if (!found) {
        print 0
      }
    }' "${file_path}"
}

trap 'stop_http_servers; stop_iperf_server; if [[ -n "${request_pid:-}" ]]; then kill "${request_pid}" 2>/dev/null || true; wait "${request_pid}" 2>/dev/null || true; fi; if [[ -n "${client_pod:-}" && -n "${vip:-}" ]]; then pod_sh "${NS_NAMESPACE}" "${client_pod}" "${CLIENT_CONTAINER}" "ip neigh del \"${vip}\" dev \"${CLIENT_IFACE}\" 2>/dev/null || true; ip route del \"${vip}/32\" dev \"${CLIENT_IFACE}\" 2>/dev/null || true" >/dev/null 2>&1 || true; fi; rm -rf "${tmpdir}"' EXIT

need_cmd awk
need_cmd grep
need_cmd "${KUBECTL}"
need_cmd sed

request_payload="GET / HTTP/1.1\r\nHost: lb.local\r\nConnection: close\r\n\r\n"
request_pid=""

client_pod="$(get_running_pod_by_prefix "${NS_NAMESPACE}" "${CLIENT_POD_PREFIX}")"
lb_pod="$(get_running_pod_by_prefix "${NS_NAMESPACE}" "${LB_POD_PREFIX}")"
server_pod="$(get_running_pod_by_prefix "${NS_NAMESPACE}" "${SERVER_POD_PREFIX}")"

[[ -n "${client_pod}" ]] || die "running client pod not found in namespace ${NS_NAMESPACE}"
[[ -n "${lb_pod}" ]] || die "running lb pod not found in namespace ${NS_NAMESPACE}"
[[ -n "${server_pod}" ]] || die "running server pod not found in namespace ${NS_NAMESPACE}"

client_node="$(trim "$(run_kubectl get pod -n "${NS_NAMESPACE}" "${client_pod}" -o jsonpath='{.spec.nodeName}')")"
[[ -n "${client_node}" ]] || die "unable to determine node for pod ${client_pod}"
forwarder_pod="$(get_forwarder_pod_on_node "${client_node}")"
[[ -n "${forwarder_pod}" ]] || die "running forwarder-ovs pod not found on node ${client_node}"

require_container "${NS_NAMESPACE}" "${client_pod}" "${CLIENT_CONTAINER}"
require_container "${NS_NAMESPACE}" "${client_pod}" "${CLIENT_CMD_CONTAINER}"
require_container "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}"
require_container "${NS_NAMESPACE}" "${server_pod}" "${SERVER_CONTAINER}"

wait_for_lb_interfaces() {
  local deadline=$((SECONDS + WAIT_TIMEOUT_SECONDS))
  local interface_count bridge_line

  while ((SECONDS < deadline)); do
    interface_count="$(list_named_links "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" '^nse-ovs-' | awk 'END { print NR + 0 }')"
    bridge_line="$(trim "$(list_named_links "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" '^nsmbr' | head -n1)")"
    if [[ "${interface_count}" == "2" && -n "${bridge_line}" ]]; then
      return 0
    fi
    sleep 1
  done

  return 1
}

wait_for_lb_interfaces || die "lb pod did not expose 2 data interfaces and 1 bridge within ${WAIT_TIMEOUT_SECONDS}s"

client_link_line="$(pod_sh "${NS_NAMESPACE}" "${client_pod}" "${CLIENT_CONTAINER}" "ip -o link show dev '${CLIENT_IFACE}'")"
client_iface="$(parse_link_name "${client_link_line}")"
client_peer_ifindex="$(parse_link_peer_ifindex "${client_link_line}")"
[[ -n "${client_iface}" ]] || die "unable to parse client interface from: ${client_link_line}"
[[ -n "${client_peer_ifindex}" ]] || die "unable to parse client peer ifindex from: ${client_link_line}"

client_ip="$(pod_sh "${NS_NAMESPACE}" "${client_pod}" "${CLIENT_CONTAINER}" "ip -o -4 addr show dev '${client_iface}' | awk '{print \$4}' | cut -d/ -f1 | head -n1")"
[[ -n "${client_ip}" ]] || die "unable to determine client IP on ${client_iface}"

mapfile -t lb_leg_lines < <(list_named_links "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" '^nse-ovs-')
[[ "${#lb_leg_lines[@]}" -eq 2 ]] || die "expected 2 LB data interfaces, got ${#lb_leg_lines[@]}"
lb_bridge_line="$(list_named_links "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" '^nsmbr' | head -n1)"
lb_bridge="$(parse_link_name "${lb_bridge_line}")"
[[ -n "${lb_bridge}" ]] || die "unable to determine LB bridge interface"

lb_leg_a="$(parse_link_name "${lb_leg_lines[0]}")"
lb_leg_a_peer_ifindex="$(parse_link_peer_ifindex "${lb_leg_lines[0]}")"
lb_leg_b="$(parse_link_name "${lb_leg_lines[1]}")"
lb_leg_b_peer_ifindex="$(parse_link_peer_ifindex "${lb_leg_lines[1]}")"

[[ -n "${lb_leg_a_peer_ifindex}" ]] || die "unable to parse LB leg A peer ifindex"
[[ -n "${lb_leg_b_peer_ifindex}" ]] || die "unable to parse LB leg B peer ifindex"

server_iface_hint="$(resolve_server_iface "${NS_NAMESPACE}" "${server_pod}" "${SERVER_CONTAINER}" "${SERVER_IFACE}")" \
  || die "unable to determine server data interface"
server_link_line="$(pod_sh "${NS_NAMESPACE}" "${server_pod}" "${SERVER_CONTAINER}" "ip -o link show dev '${server_iface_hint}'")"
server_iface="$(parse_link_name "${server_link_line}")"
server_peer_ifindex="$(parse_link_peer_ifindex "${server_link_line}")"
[[ -n "${server_iface}" ]] || die "unable to parse server interface from: ${server_link_line}"
[[ -n "${server_peer_ifindex}" ]] || die "unable to parse server peer ifindex from: ${server_link_line}"

backend_ip="${BACKEND_IP_INPUT:-$(trim "$(pod_sh "${NS_NAMESPACE}" "${server_pod}" "${SERVER_CONTAINER}" "ip -o -4 addr show dev '${server_iface_hint}' | awk '{print \$4}' | cut -d/ -f1 | head -n1")")}"
[[ -n "${backend_ip}" ]] || die "unable to determine backend IP"

lb_mode="$(trim "$(pod_sh "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" "awk -F': *' '/^mode:/{print \$2; exit}' '${LB_RULES_PATH}'")")"
vip="${VIP_INPUT:-$(trim "$(pod_sh "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" "awk -F': *' '/^vip:/{print \$2; exit}' '${LB_RULES_PATH}' 2>/dev/null || true")")}"
if [[ -z "${vip}" ]]; then
  vip="${DEFAULT_VIP}"
fi

server_mac="$(trim "$(pod_sh "${NS_NAMESPACE}" "${server_pod}" "${SERVER_CONTAINER}" "cat /sys/class/net/${server_iface_hint}/address")")"
[[ -n "${server_mac}" ]] || die "unable to determine server MAC"

forwarder_links_file="${tmpdir}/forwarder-links.txt"
run_kubectl exec -n "${NSM_SYSTEM_NAMESPACE}" "${forwarder_pod}" -- ip -o link show >"${forwarder_links_file}"
persist_artifact "${forwarder_links_file}" "forwarder-links.txt"

client_forwarder_port="$(forwarder_port_name_for_ifindex "${forwarder_links_file}" "${client_peer_ifindex}")"
server_forwarder_port="$(forwarder_port_name_for_ifindex "${forwarder_links_file}" "${server_peer_ifindex}")"
lb_leg_a_forwarder_port="$(forwarder_port_name_for_ifindex "${forwarder_links_file}" "${lb_leg_a_peer_ifindex}")"
lb_leg_b_forwarder_port="$(forwarder_port_name_for_ifindex "${forwarder_links_file}" "${lb_leg_b_peer_ifindex}")"

[[ -n "${client_forwarder_port}" ]] || die "unable to map client peer ifindex ${client_peer_ifindex} to forwarder port"
[[ -n "${server_forwarder_port}" ]] || die "unable to map server peer ifindex ${server_peer_ifindex} to forwarder port"
[[ -n "${lb_leg_a_forwarder_port}" ]] || die "unable to map LB leg A peer ifindex ${lb_leg_a_peer_ifindex} to forwarder port"
[[ -n "${lb_leg_b_forwarder_port}" ]] || die "unable to map LB leg B peer ifindex ${lb_leg_b_peer_ifindex} to forwarder port"

client_conn_id="$(port_conn_id "${client_forwarder_port}")"
server_conn_id="$(port_conn_id "${server_forwarder_port}")"
leg_a_conn_id="$(port_conn_id "${lb_leg_a_forwarder_port}")"
leg_b_conn_id="$(port_conn_id "${lb_leg_b_forwarder_port}")"

if [[ "${leg_a_conn_id}" == "${client_conn_id}" ]]; then
  lb_ingress_leg="${lb_leg_a}"
  lb_ingress_forwarder_port="${lb_leg_a_forwarder_port}"
  lb_egress_leg="${lb_leg_b}"
  lb_egress_forwarder_port="${lb_leg_b_forwarder_port}"
elif [[ "${leg_b_conn_id}" == "${client_conn_id}" ]]; then
  lb_ingress_leg="${lb_leg_b}"
  lb_ingress_forwarder_port="${lb_leg_b_forwarder_port}"
  lb_egress_leg="${lb_leg_a}"
  lb_egress_forwarder_port="${lb_leg_a_forwarder_port}"
else
  die "unable to match LB ingress leg to client connection id ${client_conn_id}"
fi

if [[ "$(port_conn_id "${lb_egress_forwarder_port}")" != "${server_conn_id}" ]]; then
  die "LB egress leg does not map to server connection id ${server_conn_id}"
fi

client_ofport="$(lookup_ofport "${forwarder_pod}" "${client_forwarder_port}")"
lb_ingress_ofport="$(lookup_ofport "${forwarder_pod}" "${lb_ingress_forwarder_port}")"
lb_egress_ofport="$(lookup_ofport "${forwarder_pod}" "${lb_egress_forwarder_port}")"
server_ofport="$(lookup_ofport "${forwarder_pod}" "${server_forwarder_port}")"

start_http_server_loop

backend_output_file="${tmpdir}/backend.out"
if ! run_http_request_sync "${backend_ip}" "${request_payload}" >"${backend_output_file}" 2>&1; then
  cat "${backend_output_file}"
  die "direct backend request failed"
fi
grep -q '200 OK' "${backend_output_file}" || {
  cat "${backend_output_file}"
  die "direct backend request did not return 200 OK"
}
persist_artifact "${backend_output_file}" "backend-direct-http.txt"

stop_http_servers
start_iperf_server

pod_sh "${NS_NAMESPACE}" "${client_pod}" "${CLIENT_CONTAINER}" "
  ip route replace '${vip}/32' dev '${CLIENT_IFACE}'
  ip neigh replace '${vip}' lladdr '${server_mac}' nud permanent dev '${CLIENT_IFACE}'
"

client_before="${tmpdir}/client-before.env"
lb_ingress_before="${tmpdir}/lb-ingress-before.env"
lb_egress_before="${tmpdir}/lb-egress-before.env"
server_before="${tmpdir}/server-before.env"
client_ofport_before="${tmpdir}/client-ofport-before.env"
lb_ingress_ofport_before="${tmpdir}/lb-ingress-ofport-before.env"
lb_egress_ofport_before="${tmpdir}/lb-egress-ofport-before.env"
server_ofport_before="${tmpdir}/server-ofport-before.env"
lb_ingress_tc_before="${tmpdir}/lb-ingress-tc-before.txt"
lb_egress_tc_before="${tmpdir}/lb-egress-tc-before.txt"

save_snapshot "${client_before}" capture_netdev_stats "${NS_NAMESPACE}" "${client_pod}" "${CLIENT_CONTAINER}" "${client_iface}"
save_snapshot "${lb_ingress_before}" capture_netdev_stats "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" "${lb_ingress_leg}"
save_snapshot "${lb_egress_before}" capture_netdev_stats "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" "${lb_egress_leg}"
save_snapshot "${server_before}" capture_netdev_stats "${NS_NAMESPACE}" "${server_pod}" "${SERVER_CONTAINER}" "${server_iface}"
save_snapshot "${client_ofport_before}" capture_ofport_stats "${forwarder_pod}" "${client_ofport}"
save_snapshot "${lb_ingress_ofport_before}" capture_ofport_stats "${forwarder_pod}" "${lb_ingress_ofport}"
save_snapshot "${lb_egress_ofport_before}" capture_ofport_stats "${forwarder_pod}" "${lb_egress_ofport}"
save_snapshot "${server_ofport_before}" capture_ofport_stats "${forwarder_pod}" "${server_ofport}"
capture_tc_filter_stats "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" "${lb_ingress_leg}" "${lb_ingress_tc_before}"
capture_tc_filter_stats "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" "${lb_egress_leg}" "${lb_egress_tc_before}"

vip_output_file="${tmpdir}/vip.out"
client_ss_file="${tmpdir}/client-ss.txt"
server_ss_file="${tmpdir}/server-ss.txt"

start_iperf_request_async "${vip}" "${vip_output_file}"

client_ss_pattern="${vip}:${IPERF_PORT}"
server_ss_pattern="${backend_ip}:${IPERF_PORT}"

sleep 1
capture_ss_snapshot "${NS_NAMESPACE}" "${client_pod}" "${CLIENT_CONTAINER}" "${client_ss_file}"
capture_ss_snapshot "${NS_NAMESPACE}" "${server_pod}" "${SERVER_CONTAINER}" "${server_ss_file}"

set +e
wait "${request_pid}"
request_rc=$?
set -e

vip_result="fail"
if [[ "${request_rc}" == "0" ]]; then
  vip_result="success"
fi

case "${EXPECT_RESULT}" in
  success)
    [[ "${vip_result}" == "success" ]] || {
      cat "${vip_output_file}" >&2
      die "expected VIP request to succeed, but it failed"
    }
    ;;
  fail)
    [[ "${vip_result}" == "fail" ]] || die "expected VIP request to fail, but it succeeded"
    ;;
esac

client_after="${tmpdir}/client-after.env"
lb_ingress_after="${tmpdir}/lb-ingress-after.env"
lb_egress_after="${tmpdir}/lb-egress-after.env"
server_after="${tmpdir}/server-after.env"
client_ofport_after="${tmpdir}/client-ofport-after.env"
lb_ingress_ofport_after="${tmpdir}/lb-ingress-ofport-after.env"
lb_egress_ofport_after="${tmpdir}/lb-egress-ofport-after.env"
server_ofport_after="${tmpdir}/server-ofport-after.env"
lb_ingress_tc_after="${tmpdir}/lb-ingress-tc-after.txt"
lb_egress_tc_after="${tmpdir}/lb-egress-tc-after.txt"

save_snapshot "${client_after}" capture_netdev_stats "${NS_NAMESPACE}" "${client_pod}" "${CLIENT_CONTAINER}" "${client_iface}"
save_snapshot "${lb_ingress_after}" capture_netdev_stats "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" "${lb_ingress_leg}"
save_snapshot "${lb_egress_after}" capture_netdev_stats "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" "${lb_egress_leg}"
save_snapshot "${server_after}" capture_netdev_stats "${NS_NAMESPACE}" "${server_pod}" "${SERVER_CONTAINER}" "${server_iface}"
save_snapshot "${client_ofport_after}" capture_ofport_stats "${forwarder_pod}" "${client_ofport}"
save_snapshot "${lb_ingress_ofport_after}" capture_ofport_stats "${forwarder_pod}" "${lb_ingress_ofport}"
save_snapshot "${lb_egress_ofport_after}" capture_ofport_stats "${forwarder_pod}" "${lb_egress_ofport}"
save_snapshot "${server_ofport_after}" capture_ofport_stats "${forwarder_pod}" "${server_ofport}"
capture_tc_filter_stats "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" "${lb_ingress_leg}" "${lb_ingress_tc_after}"
capture_tc_filter_stats "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" "${lb_egress_leg}" "${lb_egress_tc_after}"

persist_artifact "${client_before}" "client-before.env"
persist_artifact "${lb_ingress_before}" "lb-ingress-before.env"
persist_artifact "${lb_egress_before}" "lb-egress-before.env"
persist_artifact "${server_before}" "server-before.env"
persist_artifact "${client_after}" "client-after.env"
persist_artifact "${lb_ingress_after}" "lb-ingress-after.env"
persist_artifact "${lb_egress_after}" "lb-egress-after.env"
persist_artifact "${server_after}" "server-after.env"
persist_artifact "${client_ofport_before}" "client-ofport-before.env"
persist_artifact "${lb_ingress_ofport_before}" "lb-ingress-ofport-before.env"
persist_artifact "${lb_egress_ofport_before}" "lb-egress-ofport-before.env"
persist_artifact "${server_ofport_before}" "server-ofport-before.env"
persist_artifact "${client_ofport_after}" "client-ofport-after.env"
persist_artifact "${lb_ingress_ofport_after}" "lb-ingress-ofport-after.env"
persist_artifact "${lb_egress_ofport_after}" "lb-egress-ofport-after.env"
persist_artifact "${server_ofport_after}" "server-ofport-after.env"
persist_artifact "${lb_ingress_tc_before}" "lb-ingress-tc-before.txt"
persist_artifact "${lb_egress_tc_before}" "lb-egress-tc-before.txt"
persist_artifact "${lb_ingress_tc_after}" "lb-ingress-tc-after.txt"
persist_artifact "${lb_egress_tc_after}" "lb-egress-tc-after.txt"
persist_artifact "${client_ss_file}" "client-ss.txt"
persist_artifact "${server_ss_file}" "server-ss.txt"
persist_artifact "${vip_output_file}" "vip-iperf.txt"

client_tx_bytes_delta="$(delta_from_files "${client_before}" "${client_after}" "tx_bytes")"
client_tx_packets_delta="$(delta_from_files "${client_before}" "${client_after}" "tx_packets")"
client_rx_bytes_delta="$(delta_from_files "${client_before}" "${client_after}" "rx_bytes")"
client_rx_packets_delta="$(delta_from_files "${client_before}" "${client_after}" "rx_packets")"

lb_ingress_rx_bytes_delta="$(delta_from_files "${lb_ingress_before}" "${lb_ingress_after}" "rx_bytes")"
lb_ingress_rx_packets_delta="$(delta_from_files "${lb_ingress_before}" "${lb_ingress_after}" "rx_packets")"
lb_ingress_tx_bytes_delta="$(delta_from_files "${lb_ingress_before}" "${lb_ingress_after}" "tx_bytes")"
lb_ingress_tx_packets_delta="$(delta_from_files "${lb_ingress_before}" "${lb_ingress_after}" "tx_packets")"

lb_egress_rx_bytes_delta="$(delta_from_files "${lb_egress_before}" "${lb_egress_after}" "rx_bytes")"
lb_egress_rx_packets_delta="$(delta_from_files "${lb_egress_before}" "${lb_egress_after}" "rx_packets")"
lb_egress_tx_bytes_delta="$(delta_from_files "${lb_egress_before}" "${lb_egress_after}" "tx_bytes")"
lb_egress_tx_packets_delta="$(delta_from_files "${lb_egress_before}" "${lb_egress_after}" "tx_packets")"

server_rx_bytes_delta="$(delta_from_files "${server_before}" "${server_after}" "rx_bytes")"
server_rx_packets_delta="$(delta_from_files "${server_before}" "${server_after}" "rx_packets")"
server_tx_bytes_delta="$(delta_from_files "${server_before}" "${server_after}" "tx_bytes")"
server_tx_packets_delta="$(delta_from_files "${server_before}" "${server_after}" "tx_packets")"

client_ofport_rx_bytes_delta="$(delta_from_files "${client_ofport_before}" "${client_ofport_after}" "rx_bytes")"
client_ofport_rx_packets_delta="$(delta_from_files "${client_ofport_before}" "${client_ofport_after}" "rx_packets")"
client_ofport_tx_bytes_delta="$(delta_from_files "${client_ofport_before}" "${client_ofport_after}" "tx_bytes")"
client_ofport_tx_packets_delta="$(delta_from_files "${client_ofport_before}" "${client_ofport_after}" "tx_packets")"

lb_ingress_ofport_rx_bytes_delta="$(delta_from_files "${lb_ingress_ofport_before}" "${lb_ingress_ofport_after}" "rx_bytes")"
lb_ingress_ofport_rx_packets_delta="$(delta_from_files "${lb_ingress_ofport_before}" "${lb_ingress_ofport_after}" "rx_packets")"
lb_ingress_ofport_tx_bytes_delta="$(delta_from_files "${lb_ingress_ofport_before}" "${lb_ingress_ofport_after}" "tx_bytes")"
lb_ingress_ofport_tx_packets_delta="$(delta_from_files "${lb_ingress_ofport_before}" "${lb_ingress_ofport_after}" "tx_packets")"

lb_egress_ofport_rx_bytes_delta="$(delta_from_files "${lb_egress_ofport_before}" "${lb_egress_ofport_after}" "rx_bytes")"
lb_egress_ofport_rx_packets_delta="$(delta_from_files "${lb_egress_ofport_before}" "${lb_egress_ofport_after}" "rx_packets")"
lb_egress_ofport_tx_bytes_delta="$(delta_from_files "${lb_egress_ofport_before}" "${lb_egress_ofport_after}" "tx_bytes")"
lb_egress_ofport_tx_packets_delta="$(delta_from_files "${lb_egress_ofport_before}" "${lb_egress_ofport_after}" "tx_packets")"

server_ofport_rx_bytes_delta="$(delta_from_files "${server_ofport_before}" "${server_ofport_after}" "rx_bytes")"
server_ofport_rx_packets_delta="$(delta_from_files "${server_ofport_before}" "${server_ofport_after}" "rx_packets")"
server_ofport_tx_bytes_delta="$(delta_from_files "${server_ofport_before}" "${server_ofport_after}" "tx_bytes")"
server_ofport_tx_packets_delta="$(delta_from_files "${server_ofport_before}" "${server_ofport_after}" "tx_packets")"

forward_tcp_hits_before="$(tc_pref_packets_from_file "${lb_ingress_tc_before}" "${forward_tcp_pref}")"
forward_tcp_hits_after="$(tc_pref_packets_from_file "${lb_ingress_tc_after}" "${forward_tcp_pref}")"
reverse_tcp_hits_before="$(tc_pref_packets_from_file "${lb_egress_tc_before}" "${reverse_tcp_pref}")"
reverse_tcp_hits_after="$(tc_pref_packets_from_file "${lb_egress_tc_after}" "${reverse_tcp_pref}")"
forward_tcp_hits_delta=$((forward_tcp_hits_after - forward_tcp_hits_before))
reverse_tcp_hits_delta=$((reverse_tcp_hits_after - reverse_tcp_hits_before))

if ((client_tx_bytes_delta <= 0)); then
  die "client transmitted no payload during verification"
fi

if [[ "${EXPECT_RESULT}" == "success" ]]; then
  ((lb_ingress_rx_bytes_delta > 0)) || die "LB ingress counters did not move during success verification"
  ((lb_egress_tx_bytes_delta > 0)) || die "LB egress counters did not move during success verification"
  ((server_rx_bytes_delta > 0)) || die "server received no traffic during success verification"
  ((client_ofport_rx_bytes_delta > 0)) || die "forwarder client ofport counters did not move"
  ((lb_ingress_ofport_tx_bytes_delta > 0)) || die "forwarder LB ingress ofport counters did not move"
  ((lb_egress_ofport_rx_bytes_delta > 0)) || die "forwarder LB egress ofport counters did not move"
  ((server_ofport_tx_bytes_delta > 0)) || die "forwarder server ofport counters did not move"
  ((forward_tcp_hits_delta > 0)) || die "forward TCP rewrite rule did not record any packet hits"
  ((reverse_tcp_hits_delta > 0)) || die "reverse TCP rewrite rule did not record any packet hits"
  grep -Fq "connected to ${vip} port ${IPERF_PORT}" "${vip_output_file}" || die "iperf proof output does not mention VIP ${vip}:${IPERF_PORT}"
else
  if grep -q "pref ${forward_tcp_pref}" "${lb_ingress_tc_after}" || grep -q "pref ${reverse_tcp_pref}" "${lb_egress_tc_after}"; then
    die "LB rewrite filters are still present under fail/empty profile"
  fi
  grep -Fq "connected to ${vip} port ${IPERF_PORT}" "${vip_output_file}" && die "iperf proof output unexpectedly mentions VIP ${vip}:${IPERF_PORT}"
fi

tc_dump_file="${tmpdir}/tc.txt"
{
  echo "== LB tc qdisc =="
  for iface_name in "${lb_ingress_leg}" "${lb_egress_leg}"; do
    echo "[${iface_name}]"
    pod_sh "${NS_NAMESPACE}" "${lb_pod}" "${LB_CONTAINER}" "tc qdisc show dev '${iface_name}' || true"
  done
  echo
  echo "== LB tc filters with counters =="
  echo "[${lb_ingress_leg}]"
  cat "${lb_ingress_tc_after}"
  echo
  echo "[${lb_egress_leg}]"
  cat "${lb_egress_tc_after}"
} >"${tc_dump_file}"
persist_artifact "${tc_dump_file}" "tc-summary.txt"

if [[ "${EXPECT_RESULT}" == "success" ]]; then
  grep -q "${vip}" "${tc_dump_file}" || die "tc filter evidence does not mention VIP ${vip}"
  grep -q "${backend_ip}" "${tc_dump_file}" || die "tc filter evidence does not mention backend IP ${backend_ip}"
fi

echo "== Context =="
echo "Namespace           : ${NS_NAMESPACE}"
echo "Expectation         : ${EXPECT_RESULT}"
echo "Node                : ${client_node}"
echo "Forwarder pod       : ${forwarder_pod}"
echo "Client pod          : ${client_pod} (${client_iface} ${client_ip})"
echo "LB pod              : ${lb_pod} (bridge ${lb_bridge}, ingress ${lb_ingress_leg}, egress ${lb_egress_leg})"
echo "Server pod          : ${server_pod} (${server_iface} ${backend_ip})"
echo "LB mode             : ${lb_mode:-unknown}"
echo "VIP                 : ${vip}"
echo "Server MAC          : ${server_mac}"
echo

echo "== Proof Summary =="
echo "Direct backend HTTP : success"
echo "VIP iperf expectation: ${EXPECT_RESULT}"
echo "VIP iperf result    : ${vip_result}"
echo

echo "== Rewrite Proof =="
echo "Client ss snapshot  :"
cat "${client_ss_file}"
echo
echo "Server ss snapshot  :"
cat "${server_ss_file}"
echo
echo "VIP iperf output    :"
cat "${vip_output_file}"
echo
echo "Forward TCP hits    : before=${forward_tcp_hits_before} after=${forward_tcp_hits_after} delta=${forward_tcp_hits_delta}"
echo "Reverse TCP hits    : before=${reverse_tcp_hits_before} after=${reverse_tcp_hits_after} delta=${reverse_tcp_hits_delta}"
echo

echo "== Path Proof =="
echo "Forward data path: client -> forwarder -> lb ingress -> bridge -> lb egress -> forwarder -> server"
render_leg_line "Client TX" "${client_pod}" "${client_iface}" "+$(fmt_bytes_gib "${client_tx_bytes_delta}") / +$(fmt_count "${client_tx_packets_delta}") pkts"
render_leg_line "FWD RX" "${forwarder_pod}" "${client_forwarder_port}/${client_ofport}" "+$(fmt_bytes_gib "${client_ofport_rx_bytes_delta}") / +$(fmt_count "${client_ofport_rx_packets_delta}") pkts"
render_leg_line "FWD TX" "${forwarder_pod}" "${lb_ingress_forwarder_port}/${lb_ingress_ofport}" "+$(fmt_bytes_gib "${lb_ingress_ofport_tx_bytes_delta}") / +$(fmt_count "${lb_ingress_ofport_tx_packets_delta}") pkts"
render_leg_line "Ingress RX" "${lb_pod}" "${lb_ingress_leg}" "+$(fmt_bytes_gib "${lb_ingress_rx_bytes_delta}") / +$(fmt_count "${lb_ingress_rx_packets_delta}") pkts"
render_leg_line "Bridge" "${lb_pod}" "${lb_bridge}" "local handoff inside inline NSE"
render_leg_line "Egress TX" "${lb_pod}" "${lb_egress_leg}" "+$(fmt_bytes_gib "${lb_egress_tx_bytes_delta}") / +$(fmt_count "${lb_egress_tx_packets_delta}") pkts"
render_leg_line "FWD RX" "${forwarder_pod}" "${lb_egress_forwarder_port}/${lb_egress_ofport}" "+$(fmt_bytes_gib "${lb_egress_ofport_rx_bytes_delta}") / +$(fmt_count "${lb_egress_ofport_rx_packets_delta}") pkts"
render_leg_line "FWD TX" "${forwarder_pod}" "${server_forwarder_port}/${server_ofport}" "+$(fmt_bytes_gib "${server_ofport_tx_bytes_delta}") / +$(fmt_count "${server_ofport_tx_packets_delta}") pkts"
render_leg_line "Server RX" "${server_pod}" "${server_iface}" "+$(fmt_bytes_gib "${server_rx_bytes_delta}") / +$(fmt_count "${server_rx_packets_delta}") pkts"
echo

echo "Reverse path: server -> forwarder -> lb egress -> bridge -> lb ingress -> forwarder -> client"
render_leg_line "Server TX" "${server_pod}" "${server_iface}" "+$(fmt_bytes_gib "${server_tx_bytes_delta}") / +$(fmt_count "${server_tx_packets_delta}") pkts"
render_leg_line "FWD RX" "${forwarder_pod}" "${server_forwarder_port}/${server_ofport}" "+$(fmt_bytes_gib "${server_ofport_rx_bytes_delta}") / +$(fmt_count "${server_ofport_rx_packets_delta}") pkts"
render_leg_line "FWD TX" "${forwarder_pod}" "${lb_egress_forwarder_port}/${lb_egress_ofport}" "+$(fmt_bytes_gib "${lb_egress_ofport_tx_bytes_delta}") / +$(fmt_count "${lb_egress_ofport_tx_packets_delta}") pkts"
render_leg_line "Egress RX" "${lb_pod}" "${lb_egress_leg}" "+$(fmt_bytes_gib "${lb_egress_rx_bytes_delta}") / +$(fmt_count "${lb_egress_rx_packets_delta}") pkts"
render_leg_line "Bridge" "${lb_pod}" "${lb_bridge}" "local handoff inside inline NSE"
render_leg_line "Ingress TX" "${lb_pod}" "${lb_ingress_leg}" "+$(fmt_bytes_gib "${lb_ingress_tx_bytes_delta}") / +$(fmt_count "${lb_ingress_tx_packets_delta}") pkts"
render_leg_line "FWD RX" "${forwarder_pod}" "${lb_ingress_forwarder_port}/${lb_ingress_ofport}" "+$(fmt_bytes_gib "${lb_ingress_ofport_rx_bytes_delta}") / +$(fmt_count "${lb_ingress_ofport_rx_packets_delta}") pkts"
render_leg_line "FWD TX" "${forwarder_pod}" "${client_forwarder_port}/${client_ofport}" "+$(fmt_bytes_gib "${client_ofport_tx_bytes_delta}") / +$(fmt_count "${client_ofport_tx_packets_delta}") pkts"
render_leg_line "Client RX" "${client_pod}" "${client_iface}" "+$(fmt_bytes_gib "${client_rx_bytes_delta}") / +$(fmt_count "${client_rx_packets_delta}") pkts"
echo

if [[ "${PRINT_TC}" == "true" ]]; then
  cat "${tc_dump_file}"
  echo
fi

echo "LB verification: PASS"
