#!/bin/bash
# Cold-build per-exec overhead series for the gcexp replay (yq v4.49.2, 263 packages).
# Run from the root of a dagger/dagger checkout:  hack/gocache/cold-series/run.sh [outdir]
# Needs: dagger CLI on PATH; ~/gocache-bench/expmod (sipsma/dagger branch gocache-bench-gcexp, with .git)
# and ~/gocache-bench/yq (yq v4.49.2 source). Env: CONCS (default "8 16 32 32 16 8").
# One dev engine (engine-dev, built from this checkout) runs as a service with wcprof and a fresh
# state volume; one warm-up replay, then one cold replay (fresh salt) per CONCS entry, each with a wcprof
# dump. Outputs land in <outdir>/run/; afterwards a text report per run is written next to each dump
# (cold*.reports.txt). DRY=1 prints the generated dagger script and exits.
# INNER=inner-plain.sh runs plain cold `go build` vs cold replay instead (ORDER, default
# "plain replay replay plain").
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:-$HOME/gocache-cold-series-$(date -u +%Y%m%dT%H%M%SZ)}
concs=${CONCS:-8 16 32 32 16 8}
expmod=$HOME/gocache-bench/expmod; yq=$HOME/gocache-bench/yq
[ -d "$expmod/.git" ] && [ -d "$yq" ] || { echo "missing $expmod (with .git) or $yq" >&2; exit 2; }
id=$(date +%s%N)
mkdir -p "$out"
{ echo "commit $(git rev-parse HEAD)"; dagger version 2>&1 | tail -1; uptime; } > "$out/meta.txt"
inner=$(cat "$here/${INNER:-inner.sh}"); inner=${inner//\'/\'\"\'\"\'}
script=$(cat <<DSH
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
svc=\$(\$dev | container | with-exposed-port 1234 | with-env-variable _DAGGER_WCPROF 1 | with-mounted-cache /var/lib/dagger \$(cache-volume gocache-cold-$id) | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-lab","--network-cidr","\$cidr","--debugaddr","0.0.0.0:6060" --use-entrypoint --insecure-root-capabilities)
engine-dev | install-client --client \$(container | from alpine:3.20 | with-exec -- apk add --no-cache curl) --service \$svc | with-mounted-directory /w \$(host | directory $expmod) | with-mounted-directory /y \$(host | directory $yq --exclude .git) | with-env-variable NONCE $id | with-env-variable CONCS "$concs" | with-env-variable ORDER "${ORDER:-plain replay replay plain}" | with-exec -- sh -c 'set -e; unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; cp -r /w /work; cp -r /y /work/yq; cd /work; $inner' | directory /out | export $out/run
container | from golang:1.26 | with-env-variable CGO_ENABLED 0 | with-directory /src \$(directory | with-file go.mod \$(host | file go.mod) | with-file go.sum \$(host | file go.sum) | with-directory engine/wcprof \$(host | directory engine/wcprof) | with-directory internal/enginelab/wcprofreport \$(host | directory .dagger/modules/engine-lab/wcprof-report)) | with-workdir /src | with-exec -- go build -o /out/wcprof-report ./internal/enginelab/wcprofreport | file /out/wcprof-report | export $out/wcprof-report
DSH
)
if [ -n "${DRY:-}" ]; then echo "$script"; exit 0; fi
dagger -c "$script"
uptime >> "$out/meta.txt"
W=$out/wcprof-report
for d in "$out"/run/cold*.dump; do
  r=${d%.dump}.reports.txt
  {
    echo "### $(basename "$d")"
    echo "## critpath of the gcexp call"; $W -view critpath -class '^gcexp:Gcexp[.](replay|plain)$' -kind call -depth 0 -top 45 "$d"
    echo "## exec phases"; $W -view classes -kind exec_phase -top 25 "$d"
    echo "## lazy ops by duration"; $W -view classes -kind lazy -sort dur -top 20 "$d"
    echo "## calls by self time"; $W -view classes -kind call -top 25 "$d"
    echo "## waits"; $W -view waits -top 20 "$d"
  } > "$r" 2>&1
done
echo "outputs in $out"; cat "$out/run/summary.txt"
