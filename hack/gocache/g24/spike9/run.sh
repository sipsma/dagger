#!/usr/bin/env bash
set -euo pipefail
export DAGGER_LEAVE_OLD_ENGINE=true
here=$(cd "$(dirname "$0")" && pwd)
repo=$(git -C "$here" rev-parse --show-toplevel)
out=${1:?usage: run.sh NEW_OUTPUT_DIRECTORY [yq|dagger|all] [functional|timing] [rounds]}
project=${2:-yq}; mode=${3:-functional}; rounds=${4:-3}
case "$project" in yq|dagger|all) ;; *) exit 2 ;; esac
case "$mode" in functional) rounds=1; parallel=${SPIKE9_PARALLEL:-4} ;; timing) parallel=${SPIKE9_PARALLEL:-16} ;; *) exit 2 ;; esac
[[ "$rounds" =~ ^[1-9][0-9]*$ && "$parallel" =~ ^[1-9][0-9]*$ ]] || exit 2
image=golang:1.26.6@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6
yq=${YQ:-$HOME/gocache-bench/yq}
ds=${DSRC:-$repo}
if [[ "$project" != dagger ]]; then
 [[ $(git -C "$yq" rev-parse HEAD) == 7ccaf8e700ce99eb3f0f6cef7f5930a0b3c827cd ]] || { echo 'expected yq v4.49.2' >&2; exit 2; }
 git -C "$yq" diff --quiet; git -C "$yq" diff --cached --quiet
fi
for value in "$here" "$out" "$yq" "$ds"; do [[ "$value" =~ ^[a-zA-Z0-9_./+-]+$ ]] || { echo 'unsupported path' >&2; exit 2; }; done
[[ ! -e "$out" ]] || { echo 'output must be new' >&2; exit 2; }
mkdir -p "$out"; out=$(cd "$out" && pwd)
dag_bin=${DAGGER_BIN:-}
if [[ -z "$dag_bin" ]]; then if [[ -x "$HOME/.local/bin/dagger" ]]; then dag_bin=$HOME/.local/bin/dagger; else dag_bin=dagger; fi; fi
dag_bin=$(readlink -f "$(command -v "$dag_bin")")
nonce=$(date -u +%Y%m%dT%H%M%S)-$$
cd "$repo"
check_load() {
 uptime
 load1=$(awk '{print $1}' /proc/loadavg)
 awk -v n="$load1" 'BEGIN {exit !(n<=16)}' || { echo "load1=$load1 >16; reschedule" >&2; exit 3; }
}
check_load
{
 echo "commit=$(git rev-parse HEAD) project=$project mode=$mode rounds=$rounds nonce=$nonce cli=$dag_bin"
 echo "engine=fresh-dev; fixture-consumer=separate-fresh-dev; real-remote=unmeasured"
 echo "image=$image parallel=$parallel"
 export DAGGER_LEAVE_OLD_ENGINE=true; "$dag_bin" version
 uptime; df -h "$HOME"
} > "$out/host-meta.txt" 2>&1
script=$(cat <<DSH
tool=\$(container | from $image | with-env-variable GOMAXPROCS 4 | with-env-variable GOCACHE /tool-cache | with-env-variable TMPDIR /tool-tmp | with-env-variable GOTMPDIR /tool-tmp | with-directory /src \$(host | directory $here) | with-workdir /src | with-exec -- sh -ec 'mkdir /tool-tmp; go test -race -count=1 ./...; CGO_ENABLED=0 go build -o /spike9 .' | file /spike9)
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
va=\$(cache-volume spike9-fixture-a-$nonce)
vb=\$(cache-volume spike9-fixture-b-$nonce)
svc=\$(\$dev | container | without-env-variable DAGGER_CLOUD_TOKEN | without-env-variable _EXPERIMENTAL_DAGGER_REMOTE_CACHE_URL | with-env-variable _DAGGER_SPIKE9 1 | with-env-variable _DAGGER_TEST_REMOTE_CACHE_FIXTURE_ROOT /fixture | with-mounted-cache /fixture \$va | with-mounted-cache /var/lib/dagger \$(cache-volume spike9-engine-a-$nonce) | with-exposed-port 1234 | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-spike9a","--network-cidr","\$cidr","--debugaddr","0.0.0.0:6060" --use-entrypoint --insecure-root-capabilities)
client=\$(engine-dev | install-client --client \$(container | from $image | with-file /spike9 \$tool | with-file /inner.sh \$(host | file $here/inner.sh) | with-env-variable DAGGER_LEAVE_OLD_ENGINE true | with-env-variable SPIKE9_NONCE $nonce | with-env-variable SPIKE9_IMAGE $image | with-env-variable SPIKE9_COMMIT $(git rev-parse HEAD) | with-env-variable MODE $mode | with-env-variable ROUNDS $rounds | with-env-variable PARALLEL $parallel) --service \$svc)
fixture=\$(\$client | with-exec -- sh -c 'unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; export DAGGER_LEAVE_OLD_ENGINE=true; dagger run /spike9 fixture --mode publish --provider fixture-$nonce --out /out > /out/fixture.log 2>&1; echo \$? > /out/exitcode; cat /out/fixture.log; exit 0' | directory /out)
\$fixture | export $out/producer
DSH
)
projects=$project; [[ "$project" != all ]] || projects='yq dagger'
for p in $projects; do
 source=$ds; [[ "$p" != yq ]] || source=$yq
 source_commit=$(git -C "$source" rev-parse HEAD)
 script+=$(cat <<DSH

res=\$(\$client | with-directory /seed \$(host | directory $source --exclude .git --exclude '**/node_modules' --exclude .g24-results --exclude .item4-results --exclude .g22-results) | with-env-variable SPIKE9_SOURCE_COMMIT $source_commit | with-env-variable PROJECT $p | with-exec -- /spike9 prepare --out /prepared | with-exec -- sh -c 'unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; sh /inner.sh > /out/runner.log 2>&1; echo \$? > /out/exitcode; cat /out/runner.log; exit 0' | directory /out)
\$res | export $out/$p
DSH
)
done
script+=$(cat <<DSH

container | from alpine:3.20 | with-mounted-cache /a \$va | with-mounted-cache /b \$vb | with-env-variable NONCE $nonce | with-exec -- sh -ec 'test -s /a/bundles/entries.json; cp -a /a/bundles /a/blobs /b/' | stdout
devb=\$(engine-dev | increment-subnet)
cidrb=\$(\$devb | network-cidr)
svcb=\$(\$devb | container | without-env-variable DAGGER_CLOUD_TOKEN | without-env-variable _EXPERIMENTAL_DAGGER_REMOTE_CACHE_URL | with-env-variable _DAGGER_SPIKE9 1 | with-env-variable _DAGGER_TEST_REMOTE_CACHE_FIXTURE_ROOT /fixture | with-mounted-cache /fixture \$vb | with-mounted-cache /var/lib/dagger \$(cache-volume spike9-engine-b-$nonce) | with-exposed-port 1234 | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-spike9b","--network-cidr","\$cidrb","--debugaddr","0.0.0.0:6060" --use-entrypoint --insecure-root-capabilities)
consumer=\$(engine-dev | install-client --client \$(container | from $image | with-file /spike9 \$tool | with-env-variable DAGGER_LEAVE_OLD_ENGINE true) --service \$svcb | with-exec -- sh -c 'unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; export DAGGER_LEAVE_OLD_ENGINE=true; dagger run /spike9 fixture --mode consume --provider fixture-$nonce --out /out > /out/fixture.log 2>&1; echo \$? > /out/exitcode; cat /out/fixture.log; exit 0' | directory /out)
\$consumer | export $out/consumer
DSH
)
printf '%s\n' "$script" > "$out/invocation.dsh"
check_load
export DAGGER_LEAVE_OLD_ENGINE=true; "$dag_bin" -c "$script" > "$out/dagger.log" 2>&1
[[ $(cat "$out/producer/exitcode") == 0 && $(cat "$out/consumer/exitcode") == 0 ]]
for p in $projects; do cat "$out/$p/summary.txt"; [[ $(cat "$out/$p/exitcode") == 0 && -f "$out/$p/PASS" ]]; done
echo "PASS: $out"
