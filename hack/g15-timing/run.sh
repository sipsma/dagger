#!/bin/bash
# Timing harness for the canonical equivalent search change (measurement only, not for merge).
# Adapted from hack/g11-timing on gocache-g11-sharedlock-timing.
#
# usage: hack/g15-timing/run.sh <order> <X=sha>...
#   e.g. hack/g15-timing/run.sh ABBAABBA A=<sha> B=<sha>
#        hack/g15-timing/run.sh C C=<sha>
# Each run builds the engine from that commit's checkout (engine-dev), starts it as a fresh
# Dagger service with an empty cache, and runs inner.sh in a client container against it.
# Needs: the dagger CLI, $G15_BENCH_DIR (default ~/gocache-bench)/expmod (gcexp workspace) and .../yq (v4.49.2).
# gcexp-nostamp.patch makes a replay salt starting with "nostamp-" skip the per-package stamp file.
# Output: $G15_TIMING_DIR (default ~/g15-timing)/out/<label>-<id>/{summary.txt,*.cpu,*.vars}, plus
# each run's summary on stdout.
set -u
ORDER=$1; shift
declare -A SHA
for kv in "$@"; do SHA[${kv%%=*}]=${kv#*=}; done
here=$(cd "$(dirname "$0")" && pwd)
repo=$(git -C "$here" rev-parse --show-toplevel)
base=${G15_TIMING_DIR:-$HOME/g15-timing}
mkdir -p "$base/out"
bench=${G15_BENCH_DIR:-$HOME/gocache-bench}
work=$base/work
rm -rf "$work"; cp -a "$bench/expmod" "$work"
(cd "$work" && patch -p1 < "$here/gcexp-nostamp.patch") || exit 1
rm -rf "$work/yq"; cp -a "$bench/yq" "$work/yq"; rm -rf "$work/yq/.git"
for x in "${!SHA[@]}"; do
  wt=$base/wt-$x
  if [ ! -d "$wt" ]; then git -C "$repo" worktree add --detach "$wt" "${SHA[$x]}"; else git -C "$wt" checkout -q --detach "${SHA[$x]}"; fi
done
inner=$(cat "$here/inner.sh")
declare -A N
for (( i=0; i<${#ORDER}; i++ )); do
  x=${ORDER:$i:1}
  N[$x]=$(( ${N[$x]:-0} + 1 ))
  label=$x-${N[$x]}; id=$(date +%s%N)
  body=${inner//LABEL/$label-$id}
  body=${body//\'/\'\"\'\"\'}
  script=$(cat <<DSH
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
svc=\$(\$dev | container | with-exposed-port 1234 | with-mounted-cache /var/lib/dagger \$(cache-volume g15-timing-$label-$id) | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-lab","--network-cidr","\$cidr","--debugaddr","0.0.0.0:6060" --use-entrypoint --insecure-root-capabilities)
engine-dev | install-client --client \$(container | from golang:1.26-alpine | with-exec -- apk add --no-cache curl) --service \$svc | with-mounted-directory /w \$(host | directory $work) | with-exec -- sh -c 'set -e; unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; cp -r /w /work; cd /work; $body' | directory /out | export $base/out/$label-$id
DSH
)
  if [ -n "${G15_DRYRUN:-}" ]; then echo "--- $label in $base/wt-$x"; echo "$script"; continue; fi
  start=$(date -u +%H:%M:%S)
  (cd "$base/wt-$x" && timeout 2400 dagger -c "$script") > "$base/out/$label-$id.log" 2>&1
  echo "== $label rc=$? commit=$(git -C "$base/wt-$x" rev-parse --short HEAD) start=$start end=$(date -u +%H:%M:%S)"
  cat "$base/out/$label-$id/summary.txt" 2>/dev/null
done
