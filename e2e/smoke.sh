#!/bin/sh
# Live smoke test: create -> Ready -> exec -> policy block -> stop -> start
# -> exec -> command spec -> delete.
#
# Prerequisites (this Mac only, never hosted CI). The simplest way to satisfy
# them is `openshell-driver-applecontainer setup`, which leaves both services
# running. To drive the pieces by hand instead:
#   - the driver is running:   bin/openshell-driver-applecontainer
#         (the gateway endpoint is auto-derived from the vmnet network; pass
#          --grpc-endpoint https://<vmnet-gw>:17670 only to override it)
#   - the gateway is running:  openshell-gateway --bind-address 0.0.0.0 --enable-mtls-auth true \
#                                --drivers applecontainer --compute-driver-socket /tmp/oshl-ac/driver.sock
#   - the openshell CLI is registered against the gateway (openshell status works)
set -eu

# Every openshell call below is non-interactive. `sandbox exec` forwards the
# caller's stdin into the guest and only finishes when it closes, so a script
# run with an open (never-closing) stdin pipe would hang on the first exec.
exec </dev/null

NAME="${1:-smoke-$$}"
CMD_NAME="${NAME}-cmd"

fail() { echo "smoke: FAIL: $*" >&2; exit 1; }

cleanup() {
  openshell sandbox delete "${NAME}" >/dev/null 2>&1 || true
  openshell sandbox delete "${CMD_NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# wait_phase polls `sandbox list` until NAME shows PHASE (or fails after 120 s).
wait_phase() {
  i=0
  until openshell sandbox list 2>/dev/null | grep -E "^$1[[:space:]]" | grep -q "$2"; do
    i=$((i + 1))
    [ "$i" -gt 120 ] && fail "sandbox $1 never reached $2"
    sleep 1
  done
}

# No command: the sandbox gets the scratch `/bin/bash -l` main process with a
# PTY, which stays alive. (A command such as `true` would run, exit, and take
# the sandbox to Error — main-process exit is terminal upstream.)
# Retried: right after a driver restart the gateway's channel to the driver
# socket may still be reconnecting, and its first RPC fails with a transport
# error before the next one succeeds.
echo "smoke: creating sandbox ${NAME}"
i=0
until openshell sandbox create --detach --name "${NAME}" >/dev/null 2>&1; do
  i=$((i + 1))
  [ "$i" -ge 3 ] && fail "create failed"
  sleep 2
done

wait_phase "${NAME}" Ready
echo "smoke: sandbox is Ready"

OUT="$(openshell sandbox exec -n "${NAME}" -- uname -a 2>/dev/null)" || fail "exec failed"
echo "smoke: exec: ${OUT}"
echo "${OUT}" | grep -q Linux || fail "unexpected exec output"

if openshell sandbox exec -n "${NAME}" -- curl -sS -m 15 https://example.com >/dev/null 2>&1; then
  fail "policy-forbidden egress was NOT blocked"
fi
echo "smoke: forbidden egress blocked as expected"

# Stop / start round trip: the VM powers off and comes back with the same
# identity; the gateway phases are Stopped then Ready.
openshell sandbox stop "${NAME}" >/dev/null 2>&1 || fail "stop failed"
wait_phase "${NAME}" Stopped
container ls -a | grep "oshl-" | grep -q stopped || fail "VM not stopped after sandbox stop"
echo "smoke: sandbox stopped (VM powered off, record kept)"
openshell sandbox start "${NAME}" >/dev/null 2>&1 || fail "start failed"
wait_phase "${NAME}" Ready
OUT="$(openshell sandbox exec -n "${NAME}" -- uname -a 2>/dev/null)" || fail "exec after start failed"
echo "${OUT}" | grep -q Linux || fail "unexpected exec output after start"
echo "smoke: sandbox started again and exec works"

# The canonical command reaches the supervisor losslessly: an argument with a
# space must arrive as one argument. The command writes a marker and then
# stays alive so the sandbox is Ready long enough to read it back.
openshell sandbox create --detach --no-tty --name "${CMD_NAME}" -- /bin/sh -c 'printf "%s" "a b" > /tmp/marker; exec sleep 600' >/dev/null 2>&1 || fail "command sandbox create failed"
wait_phase "${CMD_NAME}" Ready
MARK="$(openshell sandbox exec -n "${CMD_NAME}" -- cat /tmp/marker 2>/dev/null)" || fail "exec in command sandbox failed"
[ "${MARK}" = "a b" ] || fail "command argument boundaries lost: marker=${MARK}"
echo "smoke: canonical command ran with argument boundaries intact"
openshell sandbox delete "${CMD_NAME}" >/dev/null 2>&1 || fail "command sandbox delete failed"

openshell sandbox delete "${NAME}" >/dev/null 2>&1 || fail "delete failed"
container ls -a | grep -q "oshl-" && fail "container VM left behind"
echo "smoke: deleted cleanly"

trap - EXIT
echo "smoke: PASS"
