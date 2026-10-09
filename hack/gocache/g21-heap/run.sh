#!/bin/bash
# Heap repro for PR 14486 (trivial getters skip the recipe digest walk). Run from the root of a dagger/dagger
# checkout: hack/gocache/g21-heap/run.sh [outdir]. Needs only the dagger CLI on PATH. One dev engine
# (engine-dev, built from this checkout, GC at defaults) with a fresh state volume; see inner.sh.
# Env: FILES (default 300). DRY=1 prints the generated dagger script and exits.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:-$HOME/g21-heap-$(date -u +%Y%m%dT%H%M%SZ)}
id=$(date +%s%N)
mkdir -p "$out"
{ echo "commit $(git rev-parse HEAD)"; dagger version 2>&1 | tail -1; uptime; df -h "$HOME" | tail -1; } > "$out/meta.txt"
inner=$(cat "$here/inner.sh"); inner=${inner//\'/\'\"\'\"\'}
script=$(cat <<DSH
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
svc=\$(\$dev | container | with-exposed-port 1234 | with-mounted-cache /var/lib/dagger \$(cache-volume g21-heap-$id) | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-lab","--network-cidr","\$cidr","--debugaddr","0.0.0.0:6060" --use-entrypoint --insecure-root-capabilities)
engine-dev | install-client --client \$(container | from alpine:3.20 | with-exec -- apk add --no-cache curl) --service \$svc | with-env-variable NONCE $id | with-env-variable FILES "${FILES:-300}" | with-exec -- sh -c 'set -e; unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out /work; $inner' | directory /out | export $out/run
DSH
)
if [ -n "${DRY:-}" ]; then echo "$script"; exit 0; fi
dagger -c "$script"
uptime >> "$out/meta.txt"
echo "outputs in $out"; cat "$out/run/summary.txt"
