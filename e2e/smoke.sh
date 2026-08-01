#!/bin/sh
# Live smoke test: create -> Ready -> exec -> policy block -> delete.
#
# Prerequisites (this Mac only, never hosted CI):
#   - the driver is running:   bin/openshell-driver-applecontainer --grpc-endpoint https://<vmnet-gw>:17670
#   - the gateway is running:  openshell-gateway --bind-address 0.0.0.0 --enable-mtls-auth true \
#                                --drivers applecontainer --compute-driver-socket /tmp/oshl-ac/driver.sock
#   - the openshell CLI is registered against the gateway (openshell status works)
set -eu

NAME="${1:-smoke-$$}"

fail() { echo "smoke: FAIL: $*" >&2; exit 1; }

cleanup() {
  openshell sandbox delete "${NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "smoke: creating sandbox ${NAME}"
openshell sandbox create --name "${NAME}" -- true >/dev/null 2>&1 &
CREATE_PID=$!

# Wait for Ready (the create call itself stays attached; poll list instead).
i=0
until openshell sandbox list 2>/dev/null | grep -E "^${NAME}[[:space:]]" | grep -q Ready; do
  i=$((i + 1))
  [ "$i" -gt 120 ] && fail "sandbox never became Ready"
  sleep 1
done
echo "smoke: sandbox is Ready"

OUT="$(openshell sandbox exec -n "${NAME}" -- uname -a 2>/dev/null)" || fail "exec failed"
echo "smoke: exec: ${OUT}"
echo "${OUT}" | grep -q Linux || fail "unexpected exec output"

if openshell sandbox exec -n "${NAME}" -- curl -sS -m 15 https://example.com >/dev/null 2>&1; then
  fail "policy-forbidden egress was NOT blocked"
fi
echo "smoke: forbidden egress blocked as expected"

openshell sandbox delete "${NAME}" >/dev/null 2>&1 || fail "delete failed"
container ls -a | grep -q "oshl-" && fail "container VM left behind"
echo "smoke: deleted cleanly"

kill "${CREATE_PID}" 2>/dev/null || true
trap - EXIT
echo "smoke: PASS"
