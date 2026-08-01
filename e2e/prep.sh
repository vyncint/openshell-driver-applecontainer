#!/bin/sh
# Idempotent host preparation for live e2e runs. Requires apple/container
# and network access; run on the development Mac, never in hosted CI.
set -eu

NETWORK="${OSHL_AC_NETWORK:-oshl}"
DEFAULT_IMAGE="${OSHL_AC_DEFAULT_IMAGE:-ghcr.io/nvidia/openshell-community/sandboxes/base:latest}"
SUPERVISOR_IMAGE="${OSHL_AC_SUPERVISOR_IMAGE:-ghcr.io/nvidia/openshell/supervisor:0.0.96}"

echo "prep: ensuring container system is running"
container system status >/dev/null 2>&1 || container system start

echo "prep: ensuring vmnet network ${NETWORK}"
if ! container network ls | awk '{print $1}' | grep -qx "${NETWORK}"; then
  container network create "${NETWORK}"
fi

ensure_image() {
  ref="$1"
  if container image ls --format json | grep -q "\"name\": *\"${ref}\""; then
    echo "prep: image present: ${ref}"
  else
    echo "prep: pulling ${ref}"
    container image pull "${ref}" --platform linux/arm64
  fi
}

ensure_image "${DEFAULT_IMAGE}"
ensure_image "${SUPERVISOR_IMAGE}"

echo "prep: done (supervisor extraction is cached lazily by the driver on first create)"
