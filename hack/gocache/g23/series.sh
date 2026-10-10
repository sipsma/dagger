#!/bin/bash
# A series of g23 slots, one fresh dev engine each, in the given order of arms.
#   ARMS="A B B A" INNER=inner-realmod.sh hack/gocache/g23/series.sh <outdir>
# Each slot runs run.sh with G23_ARGS set to its arm, followed by G23_EXTRA if set; outputs land
# in <outdir>/<n>-<arm>/.
# Then prints every slot's summary.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:?outdir}
mkdir -p "$out"
n=0
for arm in ${ARMS:?set ARMS}; do
  n=$((n+1))
  G23_ARGS="$arm${G23_EXTRA:+ $G23_EXTRA}" "$here/run.sh" "$out/$n-$arm" > "$out.$n-$arm.log" 2>&1 || echo "slot $n-$arm failed: see $out.$n-$arm.log"
done
for d in "$out"/*/; do echo "== $d"; cat "$d/run/summary.txt" 2>/dev/null; cat "$d/run/execcounts.txt" 2>/dev/null; done
