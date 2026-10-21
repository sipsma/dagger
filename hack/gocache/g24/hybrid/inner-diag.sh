# g24 hybrid diagnostic, in run.sh's client container (run with WCPROF=1): /w is
# hack/gocache/g24/hybrid, /sdk the Go SDK, /y yq. The question: do the hybrid's no-change builds
# re-run their actions because each build starts a new nested Dagger client, whose uploads from the
# build container don't reuse an earlier client's cache entries? Two arms on yq, each its own salt,
# each a cold first build then two no-change builds, the hybrid as timed (-reuse=true):
#   separate - one dagger invocation, so one container and one nested client, per build (as timed);
#              the engine profile is flushed before each no-change build and dumped after it;
#   shared   - all three builds in one container, so one nested client; one profile dump for the
#              three (flushed before), and the per-build action logs tell the builds apart.
# Report how many execs actually ran (profile exec.workload count) and the no-change builds' times
# and per-action exec times by input count. If shared no-change builds hit on nearly every action at
# ~10 ms whatever the input count, the cause is client-scoped cache identity, not hit cost.
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
IMG='golang:1.26.6@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6'
D=http://dagger-engine:6060/debug/wcprof/dump
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
echo "diagnostic; nonce $NONCE; image $IMG" > /out/summary.txt
cat > /out/tool.dsh <<EOF
container | from $IMG | with-directory /h \$(host | directory /w/tool) | with-directory /h/sdk \$(host | directory /sdk) | with-workdir /h | with-env-variable CGO_ENABLED 0 | with-exec -- sh -c "go mod tidy && go build -o /hybrid ." | file /hybrid | export /out/hybrid
EOF
dagger -c "$(cat /out/tool.dsh)" > /out/tool.log 2>&1 || { echo "tool build failed" >> /out/summary.txt; exit 0; }
run() { # $1 = invocation name, $2 = salt, $3 = build labels
  cat > /out/$1.dsh <<EOF
container | from $IMG | with-directory /seed \$(host | directory /y) | with-workdir /seed | with-exec -- go mod download | with-file /usr/local/bin/hybrid \$(host | file /out/hybrid) | with-file /diag.sh \$(host | file /w/diag-build.sh) | with-env-variable IMG $IMG | with-env-variable BUILDS "$3" | with-env-variable REUSE true | with-env-variable SALT $2 | with-env-variable NONCE $NONCE-$1 | with-exec -- sh /diag.sh | directory /out | export /out/$1
EOF
  dagger -c "$(cat /out/$1.dsh)" > /out/$1.dagger.log 2>&1 || echo "$1: dagger failed" >> /out/summary.txt
  for l in $3; do cat /out/$1/$l.summary >> /out/summary.txt 2>/dev/null || echo "$l: no summary" >> /out/summary.txt; done
}
salt=$NONCE-separate
run separate-first $salt separate-first
for n in 1 2; do
  flush
  run separate-noop$n $salt separate-noop$n
  dump separate-noop$n
done
flush
run shared $NONCE-shared "shared-first shared-noop1 shared-noop2"
dump shared
