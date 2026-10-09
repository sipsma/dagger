#!/bin/bash
# Restart-cost series: no-change gcexp replays on a warm engine vs on the same state after a clean restart,
# for two engine commits. Run from the root of a dagger/dagger checkout of this branch:
#   hack/gocache/restart-series/run.sh [outdir]
# Needs: dagger CLI on PATH; ~/gocache-bench/expmod (with .git) and ~/gocache-bench/yq (yq v4.49.2).
# Env: GC_OFF (default 0), BENCH (default ~/gocache-bench), A, B (engine commits; default main dbfaa800cf and PR 14592 head a4f803ec51), ORDER (default "A B B A"), DRY=1.
# Per run: a dev engine built from that commit runs as a service with wcprof, a debug address and a fresh
# cache volume. Session 1 (prime) and session 2 (restart) share the volume; each session ends with
# `$svc | stop` (SIGTERM, waits), so the engine closes its store cleanly and session 2 restores it.
# Without that stop the session's end SIGKILLs the service and the next start wipes the store.
# Afterwards text reports are written per run (reports.txt); dumps, profiles and egraph snapshots are kept.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd); repo=$(git rev-parse --show-toplevel)
out=${1:-$HOME/gocache-restart-series-$(date -u +%Y%m%dT%H%M%SZ)}
declare -A sha=([A]=${A:-dbfaa800cfba8bcdba155e4c6415bc9225af9dfc} [B]=${B:-a4f803ec514a6d7807239af235d3cd59f1026beb})
order=${ORDER:-A B B A}
bench=${BENCH:-$HOME/gocache-bench}; expmod=$bench/expmod; yq=$bench/yq
[ -d "$expmod/.git" ] && [ -d "$yq" ] || { echo "missing $expmod (with .git) or $yq" >&2; exit 2; }
if [ -z "${DRY:-}" ]; then mkdir -p "$out"; fi; log=$out/series.log
# GC_OFF=1 replaces engine-dev's engine.json with the same content plus "gc":{"enabled":false}, so the
# engine's automatic GC (which prunes whenever the disk has under 20% free) cannot evict cached results
# mid-series. Default 0 keeps engine-dev's config, as in the first two series.
gcfile=""
if [ "${GC_OFF:-0}" = 1 ]; then gcfile='{"registries":{"docker.io":{"mirrors":["mirror.gcr.io"]}},"gc":{"enabled":false}}'; fi
[ -n "${DRY:-}" ] || { echo "harness $(git rev-parse HEAD)"; echo "A ${sha[A]}"; echo "B ${sha[B]}"; echo "order $order"; dagger version 2>&1 | tail -1; uptime; df -h "$HOME" | tail -1; echo "gc_off=${GC_OFF:-0}"; } > "$out/meta.txt"
dsh() { # dsh <inner> <outdir> <volume> <nonce>
  local inner; inner=$(cat "$1"); inner=${inner//\'/\'\"\'\"\'}
  cat <<DSH
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
svc=\$(\$dev | container |${gcfile:+ with-new-file /etc/dagger/engine.json '$gcfile' |} with-exposed-port 1234 | with-env-variable _DAGGER_WCPROF 1 | with-mounted-cache /var/lib/dagger \$(cache-volume $3) | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-lab","--network-cidr","\$cidr","--debugaddr","0.0.0.0:6060" --use-entrypoint --insecure-root-capabilities)
engine-dev | install-client --client \$(container | from alpine:3.20 | with-exec -- apk add --no-cache curl) --service \$svc | with-mounted-directory /w \$(host | directory $expmod) | with-mounted-directory /y \$(host | directory $yq --exclude .git) | with-env-variable NONCE $4 | with-exec -- sh -c 'set -e; unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; cp -r /w /work; cp -r /y /work/yq; cd /work; $inner' | directory /out | export $2
\$svc | stop
DSH
}
if [ -n "${DRY:-}" ]; then dsh "$here/inner-prime.sh" "$out/run1-A/prime" vol nonce; exit 0; fi
for v in A B; do
  [ -d "$out/wt-$v" ] || git -C "$repo" worktree add --detach "$out/wt-$v" "${sha[$v]}" >> "$log" 2>&1
done
k=0
for v in $order; do
  k=$((k+1)); run=run$k-$v; id=$(date +%s%N); mkdir -p "$out/$run"
  for phase in prime restart; do
    echo "$(date -u +%T) start $run $phase sha=${sha[$v]} $(uptime)" | tee -a "$log"
    dsh "$here/inner-$phase.sh" "$out/$run/$phase" "gocache-restart-$id" "$id" > "$out/$run/$phase.dsh"
    (cd "$out/wt-$v" && timeout 1800 dagger -c "$(cat "$out/$run/$phase.dsh")" > "$out/$run/$phase.log" 2>&1) || echo "$run $phase rc=$?" | tee -a "$log"
    sed "s/^/$run /" "$out/$run/$phase/summary.txt" 2>/dev/null | tee -a "$log" || true
  done
done
uptime >> "$out/meta.txt"
# Reports: wcprof-report built from this checkout; pprof and the egraph summary run in containers.
dagger -c "container | from golang:1.26 | with-env-variable CGO_ENABLED 0 | with-directory /src \$(directory | with-file go.mod \$(host | file $repo/go.mod) | with-file go.sum \$(host | file $repo/go.sum) | with-directory engine/wcprof \$(host | directory $repo/engine/wcprof) | with-directory internal/enginelab/wcprofreport \$(host | directory $repo/.dagger/modules/engine-lab/wcprof-report)) | with-workdir /src | with-exec -- go build -o /out/wcprof-report ./internal/enginelab/wcprofreport | file /out/wcprof-report | export $out/wcprof-report" > "$out/report-build.log" 2>&1
W=$out/wcprof-report
for d in "$out"/run*-?; do
  r=$d/reports.txt; : > "$r"
  for dump in "$d"/prime/warm-replay*.dump "$d"/restart/restored-replay*.dump; do
    [ -f "$dump" ] || continue
    { echo "### $(basename "$dump")"
      echo "## critpath"; $W -view critpath -class '^gcexp:Gcexp[.]replay$' -kind call -depth 0 -top 16 "$dump" | sed -n '1,22p'
      echo "## calls by self"; $W -view classes -kind call -top 12 "$dump"
      echo "## internal"; $W -view classes -kind internal -top 4 "$dump"; } >> "$r" 2>&1
  done
done
dagger -c "container | from golang:1.26 | with-directory /r \$(host | directory $out --include 'run*/*/cpu-*.pprof') | with-exec -- sh -c 'cd /r; for p in run*/*/cpu-*.pprof; do echo \"##### \$p flat\"; go tool pprof -top -nodecount=30 \$p 2>&1 | sed -n \"5,40p\"; echo \"##### \$p cum\"; go tool pprof -top -cum -nodecount=70 \$p 2>&1 | sed -n \"7,80p\"; done > /pprof.txt' | file /pprof.txt | export $out/pprof-reports.txt" > "$out/pprof.log" 2>&1
dagger -c "container | from python:3.13-alpine | with-directory /r \$(host | directory $out --include 'run*/*/egraph-*.json') | with-file /s.py \$(host | file $here/egraph-summary.py) | with-exec -- sh -c 'cd /r; for f in run*/*/egraph-*.json; do echo \"== \$f\"; python3 -I /s.py \$f; done > /eg.txt' | file /eg.txt | export $out/egraph-reports.txt" > "$out/egraph.log" 2>&1
echo "== results"; grep -h "wall_ms" "$log"
