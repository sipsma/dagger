#!/bin/bash
# Timing harness for the dagql shared egraph lock change (measurement only, not for merge).
#
# usage: hack/g11-timing/run.sh <A-sha> <B-sha> [order] [workload]
#   order: letters A/B, default ABBAABBA (8 runs).
#   workload: plain (default; ~/gocache-bench/expmod) or memo-nostamp (the gcexp workspace
#   committed here under memo-ws/, whose generated Go client carries a per-instance ID memo
#   prototype, and `gcexp replay --no-stamp`, which skips reading the per-package stamp file;
#   together they approximate the other replay fixes having landed).
# Each run builds the engine from that commit's checkout (engine-dev), starts it as a fresh
# Dagger service with an empty cache, and runs the workload in a client container against it:
# a cold yq replay (one exec per Go package), three no-change replays, a one-line edit
# ("edit A"), then six warm `dagger call` start-ups.
# Needs: the dagger CLI, ~/gocache-bench/expmod (gcexp workspace) and ~/gocache-bench/yq (v4.49.2).
# Output: $G11_TIMING_DIR (default ~/g11-timing)/out/<label>-<id>/summary.txt, plus a combined
# summary on stdout.
set -u
A=$1; B=$2; ORDER=${3:-ABBAABBA}; WORKLOAD=${4:-plain}
here=$(cd "$(dirname "$0")" && pwd)
repo=$(git -C "$here" rev-parse --show-toplevel)
base=${G11_TIMING_DIR:-$HOME/g11-timing}
mkdir -p "$base/out"
work=$base/work
rm -rf "$work"
case $WORKLOAD in
  plain) cp -a "$HOME/gocache-bench/expmod" "$work"; extra="" ;;
  memo-nostamp)
    cp -a "$here/memo-ws" "$work"
    git -C "$work" init -q && git -C "$work" add -A && git -C "$work" -c user.name=g11 -c user.email=g11@localhost commit -qm memo-ws
    extra="--no-stamp" ;;
  *) echo "unknown workload $WORKLOAD"; exit 2 ;;
esac
rm -rf "$work/yq"; cp -a "$HOME/gocache-bench/yq" "$work/yq"; rm -rf "$work/yq/.git"
for x in A B; do
  sha=${!x}
  wt=$base/wt-$x
  if [ ! -d "$wt" ]; then git -C "$repo" worktree add --detach "$wt" "$sha"; else git -C "$wt" checkout -q --detach "$sha"; fi
done
inner=$(cat "$here/inner.sh"); inner=${inner//EXTRA/$extra}
na=0; nb=0
for (( i=0; i<${#ORDER}; i++ )); do
  x=${ORDER:$i:1}
  if [ "$x" = A ]; then na=$((na+1)); n=$na; else nb=$((nb+1)); n=$nb; fi
  label=$x-$n; id=$(date +%s%N)
  body=${inner//LABEL/$label-$id}
  body=${body//\'/\'\"\'\"\'}
  script=$(cat <<DSH
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
svc=\$(\$dev | container | with-exposed-port 1234 | with-mounted-cache /var/lib/dagger \$(cache-volume g11-timing-$label-$id) | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-lab","--network-cidr","\$cidr","--debugaddr","0.0.0.0:6060" --use-entrypoint --insecure-root-capabilities)
engine-dev | install-client --client \$(container | from alpine:3.20 | with-exec -- apk add --no-cache curl) --service \$svc | with-mounted-directory /w \$(host | directory $work) | with-exec -- sh -c 'set -e; unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; cp -r /w /work; cd /work; $body' | directory /out | export $base/out/$label-$id
DSH
)
  if [ -n "${G11_DRYRUN:-}" ]; then echo "--- $label in $base/wt-$x"; echo "$script"; continue; fi
  start=$(date -u +%H:%M:%S)
  (cd "$base/wt-$x" && timeout 1800 dagger -c "$script") > "$base/out/$label-$id.log" 2>&1
  echo "== $label rc=$? commit=$(git -C "$base/wt-$x" rev-parse --short HEAD) start=$start end=$(date -u +%H:%M:%S)"
  cat "$base/out/$label-$id/summary.txt" 2>/dev/null
done
