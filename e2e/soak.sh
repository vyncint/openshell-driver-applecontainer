#!/bin/sh
# Soak: repeated create -> Ready -> exec -> delete cycles against the live
# gateway + driver, reporting per-cycle and mean create-to-Ready latency.
# Same prerequisites as smoke.sh. Usage: soak.sh [cycles]
set -eu

N="${1:-10}"
sum=0
i=1
while [ "$i" -le "$N" ]; do
  name="soak-$$-$i"
  start=$(python3 -c 'import time; print(time.time())')
  nohup openshell sandbox create --name "$name" -- true >/dev/null 2>&1 &
  cpid=$!

  j=0
  until openshell sandbox list 2>/dev/null | grep -E "^${name}[[:space:]]" | grep -q Ready; do
    j=$((j + 1))
    if [ "$j" -gt 240 ]; then
      echo "soak: FAIL: ${name} never became Ready" >&2
      exit 1
    fi
    sleep 0.5
  done
  dur=$(python3 -c "import time; print(f'{time.time() - $start:.1f}')")

  openshell sandbox exec -n "$name" -- true >/dev/null 2>&1 || {
    echo "soak: FAIL: exec in ${name}" >&2
    exit 1
  }
  openshell sandbox delete "$name" >/dev/null 2>&1 || {
    echo "soak: FAIL: delete ${name}" >&2
    exit 1
  }
  kill "$cpid" 2>/dev/null || true

  echo "soak: cycle ${i}/${N}: create->Ready ${dur}s"
  sum=$(python3 -c "print(f'{$sum + $dur:.1f}')")
  i=$((i + 1))
done

leftover=$(container ls -a --format json | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')
[ "$leftover" -eq 0 ] || { echo "soak: FAIL: ${leftover} VMs left behind" >&2; exit 1; }

echo "soak: PASS (${N} cycles, mean create->Ready $(python3 -c "print(f'{$sum / $N:.1f}')")s, no VMs leaked)"
