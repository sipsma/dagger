#!/bin/bash
# Usage: run.sh <crun binary> <outdir>   (from the directory holding bisect.sh and engine-spec-arm64.json)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd); crun=$1; out=$2; mkdir -p "$out"
{ date -u; uptime; df -h "$HOME" | tail -1; } > "$out/host-before.txt"
dagger -c "container | from alpine:3.22 | with-directory /b/rootfs \$(container | from alpine:3.20 | rootfs) | with-file /spec/engine.json \$(host | file $here/engine-spec-arm64.json) | with-file /crun \$(host | file $crun) | with-file /bisect.sh \$(host | file $here/bisect.sh) | with-env-variable N $(date +%s%N) | with-exec --insecure-root-capabilities -- sh /bisect.sh | stdout" > "$out/bisect.txt" 2> "$out/dagger.log"
{ date -u; uptime; } > "$out/host-after.txt"
cat "$out/bisect.txt"
