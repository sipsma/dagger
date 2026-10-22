#!/bin/bash
# g26 series (from g25's run.sh): one dev engine built from this checkout, one INNER script run in a
# client container bound to it. Run from the root of this checkout:
#   G26_ARGS="<order> <A ref> <B ref> <project>" hack/gocache/g26/run.sh [outdir]
# Needs: dagger CLI on PATH, and yq v4.49.2 at $YQ (default ~/gocache-bench/yq) for the yq project.
# The dev engine runs with wcprof and a fresh state volume. INNER scripts write /out/summary.txt
# ("<step> wall_ms=<n>") and wcprof dumps; afterwards each dump gets a text report next to it.
# CLASS is the critical-path root class regex for the reports. DRY=1 prints the dagger script.
# DSRC is the source tree mounted at /d in the client container: by default this checkout, the
# dagger/dagger tree the series builds.
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:-$HOME/gocache-g26-$(date -u +%Y%m%dT%H%M%SZ)}
root=$(cd "$here/../../.." && pwd)
id=$(date +%s%N)
mkdir -p "$out"
steal() { awk '/^cpu /{print $9}' /proc/stat; }
steal0=$(steal)
{ echo "commit $(git -C "$root" rev-parse HEAD)"; echo "inner ${INNER:-}"; echo "args ${G26_ARGS:-}"; dagger version 2>&1 | tail -1; uptime; df -h "$HOME" | tail -1; echo "loadavg-start $(cat /proc/loadavg)"; } > "$out/meta.txt"
INNER=${INNER:-inner.sh}
yq=${YQ:-$HOME/gocache-bench/yq}
[ -d "$yq" ] || { echo "missing $yq" >&2; exit 2; }
inner=$(cat "$here/$INNER"); inner=${inner//\'/\'\"\'\"\'}
script=$(cat <<DSH
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
svc=\$(\$dev | container | with-exposed-port 1234 | with-env-variable _DAGGER_WCPROF 1 | with-mounted-cache /var/lib/dagger \$(cache-volume gocache-g26-$id) | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-lab","--network-cidr","\$cidr","--debugaddr","0.0.0.0:6060" --use-entrypoint --insecure-root-capabilities)
engine-dev | install-client --client \$(container | from alpine:3.20 | with-exec -- apk add --no-cache curl ripgrep git) --service \$svc | with-mounted-directory /d \$(host | directory ${DSRC:-$root} --exclude .git --exclude docs --exclude "**/node_modules" --exclude hack/gocache) | with-mounted-directory /y \$(host | directory $yq --exclude .git) | with-env-variable DAGGER_LEAVE_OLD_ENGINE true | with-env-variable NONCE $id | with-env-variable G26_ARGS "${G26_ARGS:-}" | with-exec -- sh -c 'set -e; unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out /work; cd /work; $inner' | directory /out | export $out/run
container | from golang:1.26 | with-env-variable CGO_ENABLED 0 | with-directory /src \$(directory | with-file go.mod \$(host | file $root/go.mod) | with-file go.sum \$(host | file $root/go.sum) | with-directory engine/wcprof \$(host | directory $root/engine/wcprof) | with-directory internal/enginelab/wcprofreport \$(host | directory $root/.dagger/modules/engine-lab/wcprof-report)) | with-workdir /src | with-exec -- go build -o /out/wcprof-report ./internal/enginelab/wcprofreport | file /out/wcprof-report | export $out/wcprof-report
DSH
)
if [ -n "${DRY:-}" ]; then echo "$script"; exit 0; fi
(cd "$root" && dagger -c "$script")
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
  } > "$r" 2>&1
done
echo "outputs in $out"; cat "$out/run/summary.txt"
