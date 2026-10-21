#!/usr/bin/env bash
# Forwardable timing slot: balanced yq series, then dagger if time permits.
set -euo pipefail
export DAGGER_LEAVE_OLD_ENGINE=true
here=$(cd "$(dirname "$0")" && pwd)
out=${1:?usage: linux-vm.sh NEW_OUTPUT_DIRECTORY}
[[ ! -e "$out" ]] || { echo "output must be new" >&2; exit 2; }
mkdir -p "$out"
out=$(cd "$out" && pwd)
start=$SECONDS
export SPIKE7_PARALLEL=12
export SPIKE7_MATERIALIZATION=copy
export SPIKE7_WORKERS=64
# An outer deadline covers downloads, helper compilation and both projects.
# timeout owns only the run.sh/CLI processes it starts, never an engine service.
export DAGGER_LEAVE_OLD_ENGINE=true; SPIKE7_BUDGET_MINUTES=10 timeout --foreground 16m "$here/run.sh" "$out/yq" yq timing 4
remaining=$((28*60-(SECONDS-start)))
if ((remaining < 8*60)); then
  echo "dagger skipped: less than eight minutes remain in the 28-minute slot" > "$out/dagger-SKIPPED.txt"
  exit 0
fi
build_budget=$((remaining/60-2))
export DAGGER_LEAVE_OLD_ENGINE=true; SPIKE7_BUDGET_MINUTES=$build_budget timeout --foreground "${remaining}s" "$here/run.sh" "$out/dagger" dagger timing 2
