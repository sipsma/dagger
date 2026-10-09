#!/bin/bash
# run.sh <label> [bench-dir]: one replay timing run of the gcexp workspace on a fresh dev engine built
# from the dagger/dagger checkout this script lives in. Run from the checkout root.
#
# bench-dir (default ~/gocache-bench) must hold expmod/ (the gcexp workspace) and yq/ (yq v4.49.2).
# The run copies both, applies no-stamp.patch (adds the --no-stamp switch), regenerates the module
# client with the dev engine's built-in Go codegen (dagger generate), then times the replay:
# cold, warm, three no-change runs, three no-change runs without the stamp read, and edit A.
# Result: $HOME/gocache-g9-idmemo/out/<label>-<id>/summary.txt (internal "total" = replay time).
set -eu
label=$1; bench=${2:-$HOME/gocache-bench}
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
id=$(date +%s%N)
base=$HOME/gocache-g9-idmemo; out=$base/out/$label-$id; bundle=$base/bundles/$label-$id
mkdir -p "$out" "$bundle"
cp -a "$bench/expmod" "$bundle/work"
rm -rf "$bundle/work/yq"; cp -a "$bench/yq" "$bundle/work/yq"
(cd "$bundle/work" && rm -rf .git && git init -q && git apply "$here/no-stamp.patch" && git add -A . && git -c user.email=x@x -c user.name=x commit -qm bench)
inner=$(cat "$here/inner-replay.sh"); inner=${inner//LABEL/$label-$id}; inner=${inner//\'/\'\"\'\"\'}
cd "$root"
dagger -c "$(cat <<DSH
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
svc=\$(\$dev | container | with-exposed-port 1234 | with-mounted-cache /var/lib/dagger \$(cache-volume g9-$label-$id) | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-lab","--network-cidr","\$cidr","--debugaddr","0.0.0.0:6060" --use-entrypoint --insecure-root-capabilities)
engine-dev | install-client --client \$(container | from golang:1.26-alpine | with-exec -- apk add --no-cache curl bc git) --service \$svc | with-mounted-directory /w \$(host | directory $bundle) | with-exec -- sh -c 'set -e; unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; cp -r /w /b; cd /b; $inner' | directory /out | export $out
DSH
)" > "$out.log" 2>&1
echo "== $label ($(git rev-parse --short HEAD))"; cat "$out/summary.txt"
