#!/bin/bash
# series.sh <dagger-repo> <outdir> [order]: read-only mount cost comparison.
#   A = main dbfaa800cf, B = A + one-transaction view lease, C = B + shared stat/read mount.
# Primes each variant's engine once (cold replay + cold fanout), then runs the measured
# runs in order (default "A B C C B A"), logging uptime around each run.
# Needs: dagger CLI on the shared engine; gcexp at $BENCH/expmod, yq at $BENCH/yq
# (BENCH defaults to ~/gocache-bench).
set -u
repo=$(cd "$1" && pwd); out=$2; order=${3:-A B C C B A}
here=$(cd "$(dirname "$0")" && pwd)
declare -A sha=([A]=dbfaa800cfba8bcdba155e4c6415bc9225af9dfc [B]=7b568265debc5632d9d6e5057032122985981458 [C]=85ad35a0b4de31cdab36e073bd58623747db4ccf)
mkdir -p "$out"; log=$out/series.log
for v in A B C; do
  [ -d "$out/wt-$v" ] || git -C "$repo" worktree add --detach "$out/wt-$v" "${sha[$v]}" >> "$log" 2>&1
done
run() { v=$1; kind=$2; label=$3
  echo "$(date -u +%T) start $label variant=$v sha=${sha[$v]} uptime: $(uptime)" | tee -a "$log"
  (cd "$out/wt-$v" && "$here/mkrun.sh" "$v" "$here/inner-$kind.sh" "$out/$label" > "$out/$label.dsh" &&
    timeout 3600 dagger -c "$(cat "$out/$label.dsh")" > "$out/$label.log" 2>&1)
  echo "$(date -u +%T) done $label rc=$? uptime: $(uptime)" | tee -a "$log"
  sed "s/^/$label /" "$out/$label/summary.txt" 2>/dev/null | tee -a "$log"
}
for v in A B C; do run $v prime prime-$v; done
i=0; for v in $order; do i=$((i+1)); run $v measure run$i-$v; done
echo "== results (wall seconds) ==" | tee -a "$log"
grep -h " wall=" "$log" | grep "^run" | tee -a "$log"
