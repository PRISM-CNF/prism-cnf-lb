#!/usr/bin/env bash
set -euo pipefail

IMAGE_NAME="nse-ovs-lb"
IMAGE_TAG="dev"
IMPORT_IMAGE="true"
OUTPUT_TAR=""

usage() {
  cat <<'EOF'
Usage: ./scripts/build_local_image.sh [--tag <tag>] [--import|--no-import] [--output-tar <path>]

Build the local LB image and optionally import it into containerd.

Options:
  --tag <tag>         Image tag to build. Default: dev
  --import            Import the built image into ctr -n k8s.io after build. Default
  --no-import         Skip containerd import
  --output-tar <path> Save docker image tarball to the given path
  -h, --help          Show this help text

Proxy env vars are forwarded to docker build automatically when present:
  HTTP_PROXY HTTPS_PROXY NO_PROXY http_proxy https_proxy no_proxy
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag)
      IMAGE_TAG="${2:-}"
      shift 2
      ;;
    --import)
      IMPORT_IMAGE="true"
      shift
      ;;
    --no-import)
      IMPORT_IMAGE="false"
      shift
      ;;
    --output-tar)
      OUTPUT_TAR="${2:-}"
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

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
image_ref="${IMAGE_NAME}:${IMAGE_TAG}"

build_cmd=(docker build -t "${image_ref}")
for proxy_var in HTTP_PROXY HTTPS_PROXY NO_PROXY http_proxy https_proxy no_proxy; do
  if [[ -n "${!proxy_var:-}" ]]; then
    build_cmd+=(--build-arg "${proxy_var}=${!proxy_var}")
  fi
done
build_cmd+=("${repo_root}")

echo "== Build =="
echo "Image: ${image_ref}"
if [[ "${#build_cmd[@]}" -gt 4 ]]; then
  echo "Proxy build args: enabled"
fi
"${build_cmd[@]}"

tar_path="${OUTPUT_TAR}"
if [[ -z "${tar_path}" && "${IMPORT_IMAGE}" == "true" ]]; then
  tar_path="$(mktemp "/tmp/${IMAGE_NAME}-${IMAGE_TAG}.XXXXXX.tar")"
fi

if [[ -n "${tar_path}" ]]; then
  echo "== Save =="
  echo "Tar: ${tar_path}"
  docker save "${image_ref}" -o "${tar_path}"
fi

if [[ "${IMPORT_IMAGE}" == "true" ]]; then
  echo "== Import =="
  ctr -n k8s.io images import "${tar_path}"
fi

if [[ -z "${OUTPUT_TAR}" && -n "${tar_path}" ]]; then
  rm -f "${tar_path}"
fi

echo "Done: ${image_ref}"
