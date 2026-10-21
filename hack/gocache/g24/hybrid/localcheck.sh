#!/bin/bash
# Local functional check of the g24 hybrid spike on the shared engine: builds the tool, then runs
# bench.sh for one round. Correctness only; no timing conclusions. Waits for load1 <= 16.
export DAGGER_LEAVE_OLD_ENGINE=true
set -euo pipefail
WT=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
OUT=${1:?outdir}
IMG='golang:1.26.6@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6'
until [ "$(awk '{print int($1)}' /proc/loadavg)" -le 16 ]; do sleep 30; done
echo "start $(date -u +%T) load $(cut -d' ' -f1 /proc/loadavg)"
mkdir -p "$OUT"
cd "$OUT"
~/.local/bin/dagger -c "
tool=\$(container | from $IMG | with-directory /h \$(host | directory $WT/hack/gocache/g24/hybrid/tool) | with-directory /h/sdk \$(host | directory $WT/sdk/go) | with-workdir /h | with-env-variable CGO_ENABLED 0 | with-exec -- sh -c 'go mod tidy && go build -o /hybrid .' | file /hybrid)
container | from $IMG | with-directory /seed \$(host | directory /home/exedev/gocache-research/yq) | with-file /usr/local/bin/hybrid \$tool | with-file /bench.sh \$(host | file $WT/hack/gocache/g24/hybrid/bench.sh) | with-env-variable NONCE chk-$(date +%s) | with-env-variable IMG $IMG | with-env-variable ROUNDS 1 | with-exec -- sh -c 'sh /bench.sh; echo exit=\$?' | directory /out | export $OUT/run
" > "$OUT/dagger.log" 2>&1 || echo "dagger rc=$?"
cat "$OUT/run/summary.txt" 2>/dev/null || tail -40 "$OUT/dagger.log"
