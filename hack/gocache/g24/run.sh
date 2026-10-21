#!/bin/bash
# g24 step 4: what one uncached call costs. One dev engine built from this checkout, with wcprof
# and a fresh state volume; one INNER script runs in a client container bound to it, with the
# callcost module (hack/gocache/g24/callcost) at /w. Run from the root of a dagger/dagger checkout:
#   INNER=inner-callcost.sh hack/gocache/g24/run.sh [outdir]
# Needs only the dagger CLI on PATH. INNER scripts write /out/summary.txt and wcprof dumps; each
# dump gets a text report next to it. CLASS is the critical-path root class regex for the reports.
# DRY=1 prints the dagger script.
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:-$HOME/gocache-g24-$(date -u +%Y%m%dT%H%M%SZ)}
id=$(date +%s%N)
mkdir -p "$out"
steal() { awk '/^cpu /{print $9}' /proc/stat; }
steal0=$(steal)
{ echo "commit $(git rev-parse HEAD)"; echo "inner ${INNER:-}"; dagger version 2>&1 | tail -1; uptime; df -h "$HOME" | tail -1; echo "loadavg-start $(cat /proc/loadavg)"; } > "$out/meta.txt"
inner=$(cat "$here/${INNER:?set INNER}"); inner=${inner//\'/\'\"\'\"\'}
script=$(cat <<DSH
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
svc=\$(\$dev | container | with-exposed-port 1234 | with-env-variable _DAGGER_WCPROF 1 | with-mounted-cache /var/lib/dagger \$(cache-volume gocache-g24-$id) | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-lab","--network-cidr","\$cidr","--debugaddr","0.0.0.0:6060" --use-entrypoint --insecure-root-capabilities)
engine-dev | install-client --client \$(container | from alpine:3.20 | with-exec -- apk add --no-cache curl git) --service \$svc | with-mounted-directory /w \$(host | directory $here/callcost) | with-env-variable DAGGER_LEAVE_OLD_ENGINE true | with-env-variable NONCE $id | with-env-variable G24_ROUNDS "${G24_ROUNDS:-3}" | with-exec -- sh -c 'set -e; unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; cp -r /w /work; cd /work; $inner' | directory /out | export $out/run
container | from golang:1.26 | with-env-variable CGO_ENABLED 0 | with-directory /src \$(directory | with-file go.mod \$(host | file go.mod) | with-file go.sum \$(host | file go.sum) | with-directory engine/wcprof \$(host | directory engine/wcprof) | with-directory internal/enginelab/wcprofreport \$(host | directory .dagger/modules/engine-lab/wcprof-report)) | with-workdir /src | with-exec -- go build -o /out/wcprof-report ./internal/enginelab/wcprofreport | file /out/wcprof-report | export $out/wcprof-report
DSH
)
if [ -n "${DRY:-}" ]; then echo "$script"; exit 0; fi
dagger -c "$script"
{ uptime; echo "loadavg-end $(cat /proc/loadavg)"; echo "cpu-steal-ticks $(( $(steal) - steal0 )) (USER_HZ, whole slot)"; } >> "$out/meta.txt"
W=$out/wcprof-report
for d in "$out"/run/*.dump; do
  [ -f "$d" ] || continue
  r=${d%.dump}.reports.txt
  {
    echo "### $(basename "$d")"
    echo "## critpath"; $W -view critpath -class "${CLASS:-^callcost:Callcost[.]run\$}" -kind call -depth 0 -top 45 "$d"
    echo "## exec phases"; $W -view classes -kind exec_phase -top 30 "$d"
    echo "## lazy ops by duration"; $W -view classes -kind lazy -sort dur -top 20 "$d"
    echo "## calls by self time"; $W -view classes -kind call -top 50 "$d"
    echo "## calls by count"; $W -view classes -kind call -sort count -top 50 "$d"
    echo "## waits"; $W -view waits -top 20 "$d"
  } > "$r" 2>&1
done
echo "outputs in $out"; cat "$out/run/summary.txt"
