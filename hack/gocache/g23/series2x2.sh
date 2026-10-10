#!/bin/bash
# Slots on two engines: one fresh dev engine per slot, built from that engine's checkout, so a
# module A/B can be stacked on an engine change. Each slot runs its checkout's run.sh from there:
#   E0=<checkout> E1=<checkout> SLOTS="E0:AC E1:CA E1:AC E0:CA" INNER=inner-realab.sh \
#     G23_EXTRA="<B ref> <C ref>" hack/gocache/g23/series2x2.sh <outdir>
# A slot "E1:CA" runs with G23_ARGS="CA $G23_EXTRA"; its outputs land in <outdir>/<n>-E1-CA/, and
# its meta.txt names the engine commit. Then prints every slot's summary.
set -euo pipefail
mkdir -p "${1:?outdir}"
out=$(cd "$1" && pwd)
n=0
for slot in ${SLOTS:?set SLOTS}; do
  n=$((n+1)); eng=${slot%%:*}; arm=${slot#*:}
  dir=${!eng:?set $eng to a dagger checkout}
  (cd "$dir" && G23_ARGS="$arm${G23_EXTRA:+ $G23_EXTRA}" hack/gocache/g23/run.sh "$out/$n-$eng-$arm") > "$out.$n-$eng-$arm.log" 2>&1 || echo "slot $n-$eng-$arm failed: see $out.$n-$eng-$arm.log"
done
for d in "$out"/*/; do echo "== $d"; head -1 "$d/meta.txt" 2>/dev/null || true; cat "$d/run/summary.txt" 2>/dev/null || true; cat "$d/run/execcounts.txt" 2>/dev/null || true; done
