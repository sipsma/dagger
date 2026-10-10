#!/bin/bash
# g23 design-exploration series: one dev engine built from this checkout, one INNER script run in a
# client container bound to it. Run from the root of a dagger/dagger checkout:
#   INNER=inner-realmod.sh hack/gocache/g23/run.sh [outdir]
# Needs: dagger CLI on PATH; $EXPMOD (default ~/gocache-bench/expmod, sipsma/dagger branch
# gocache-bench-gcexp or a g23 prototype branch, with .git) and ~/gocache-bench/yq (yq v4.49.2).
# The dev engine runs with wcprof and a fresh state volume. INNER scripts write /out/summary.txt
# ("<step> wall_ms=<n>") and wcprof dumps; afterwards each dump gets a text report next to it.
# CLASS is the critical-path root class regex for the reports. DRY=1 prints the dagger script.
# DSRC (optional) is a source tree mounted at /d in the client container, e.g. a dagger/dagger
# checkout for inner-scale.sh.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:-$HOME/gocache-g23-$(date -u +%Y%m%dT%H%M%SZ)}
expmod=${EXPMOD:-$HOME/gocache-bench/expmod}; yq=${YQ:-$HOME/gocache-bench/yq}
[ -d "$expmod/.git" ] && [ -d "$yq" ] || { echo "missing $expmod (with .git) or $yq" >&2; exit 2; }
id=$(date +%s%N)
mkdir -p "$out"
steal() { awk '/^cpu /{print $9}' /proc/stat; }
steal0=$(steal)
{ echo "commit $(git rev-parse HEAD)"; echo "expmod $(git -C "$expmod" rev-parse HEAD)"; echo "inner ${INNER:-}"; dagger version 2>&1 | tail -1; uptime; df -h "$HOME" | tail -1; echo "loadavg-start $(cat /proc/loadavg)"; } > "$out/meta.txt"
inner=$(cat "$here/${INNER:?set INNER}"); inner=${inner//\'/\'\"\'\"\'}
script=$(cat <<DSH
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
svc=\$(\$dev | container | with-exposed-port 1234 | with-env-variable _DAGGER_WCPROF 1 | with-mounted-cache /var/lib/dagger \$(cache-volume gocache-g23-$id) | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-lab","--network-cidr","\$cidr","--debugaddr","0.0.0.0:6060" --use-entrypoint --insecure-root-capabilities)
engine-dev | install-client --client \$(container | from alpine:3.20 | with-exec -- apk add --no-cache curl ripgrep git) --service \$svc | with-mounted-directory /w \$(host | directory $expmod) | with-mounted-directory /y \$(host | directory $yq --exclude .git) | with-mounted-directory /d \$(host | directory ${DSRC:-$here/empty} --exclude .git --exclude docs --exclude "**/node_modules") | with-env-variable NONCE $id | with-env-variable G23_ARGS "${G23_ARGS:-}" | with-exec -- sh -c 'set -e; unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; cp -r /w /work; cp -r /y /work/yq; cd /work; $inner' | directory /out | export $out/run
container | from golang:1.26 | with-env-variable CGO_ENABLED 0 | with-directory /src \$(directory | with-file go.mod \$(host | file go.mod) | with-file go.sum \$(host | file go.sum) | with-directory engine/wcprof \$(host | directory engine/wcprof) | with-directory internal/enginelab/wcprofreport \$(host | directory .dagger/modules/engine-lab/wcprof-report)) | with-workdir /src | with-exec -- go build -o /out/wcprof-report ./internal/enginelab/wcprofreport | file /out/wcprof-report | export $out/wcprof-report
DSH
)
if [ -n "${DRY:-}" ]; then echo "$script"; exit 0; fi
dagger -c "$script"
{ uptime; echo "loadavg-end $(cat /proc/loadavg)"; echo "cpu-steal-ticks $(( $(steal) - steal0 )) (USER_HZ, whole slot)"; } >> "$out/meta.txt"
W=$out/wcprof-report
for d in "$out"/run/*.dump; do
  [ -f "$d" ] || continue
  echo "$(basename "$d") exec.processRun=$($W -view classes -kind exec_phase -class '^exec[.]processRun$' "$d" | awk '$NF ~ /^ok:/ && /exec.processRun/ {print $1}')" >> "$out/run/execcounts.txt"
  r=${d%.dump}.reports.txt
  {
    echo "### $(basename "$d")"
    echo "## critpath"; $W -view critpath -class "${CLASS:-.}" -kind call -depth 0 -top 45 "$d"
    echo "## exec phases"; $W -view classes -kind exec_phase -top 25 "$d"
    echo "## lazy ops by duration"; $W -view classes -kind lazy -sort dur -top 20 "$d"
    echo "## calls by self time"; $W -view classes -kind call -top 40 "$d"
    echo "## calls by count"; $W -view classes -kind call -sort count -top 40 "$d"
    echo "## waits"; $W -view waits -top 20 "$d"
    echo "## dang.* steps (g22 timing points)"; $W -view classes -kind session_phase -class '^dang[.]' -sort dur -top 30 "$d"
    echo "## module function executions"; $W -view classes -kind call_exec -class ':' -sort dur -top 30 "$d"
    echo "## module function executions, by child composition"; $W -view breakdown -kind call_exec -class ':' "$d"
    echo "## session phases"; $W -view classes -kind session_phase -sort dur -top 30 "$d"
    echo "## clients"; $W -view clients "$d"
  } > "$r" 2>&1
done
# CPU profiles (INNER=inner-dangcall.sh): text reports next to each profile.
for p in "$out"/run/*.pprof; do
  [ -f "$p" ] || continue
  b=$(basename "$p" .pprof)
  dagger -s -c "container | from golang:1.26 | with-file /p.pprof \$(host | file $p) | with-file /r.sh \$(host | file $here/pprof-dang.sh) | with-exec -- sh /r.sh | stdout" > "$out/run/$b.pprof.txt" 2>&1 || echo "pprof report for $b failed" >> "$out/run/summary.txt"
done
echo "outputs in $out"; cat "$out/run/summary.txt"
