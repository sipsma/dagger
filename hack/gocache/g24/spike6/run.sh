#!/usr/bin/env bash
# One fresh dev engine, from this checkout; never a cloud engine.
set -euo pipefail
export DAGGER_LEAVE_OLD_ENGINE=true
here=$(cd "$(dirname "$0")" && pwd)
repo=$(git -C "$here" rev-parse --show-toplevel)
out=${1:?usage: run.sh NEW_OUTPUT_DIRECTORY [functional|timing]}
mode=${2:-functional}
case "$mode" in functional|timing) ;; *) exit 2 ;; esac
[[ ! -e "$out" ]] || { echo "output must be new" >&2; exit 2; }
mkdir -p "$out"
out=$(cd "$out" && pwd)
dagger_bin=${DAGGER_BIN:-}
if [[ -z "$dagger_bin" ]]; then
  if [[ -x "$HOME/.local/bin/dagger" ]]; then dagger_bin=$HOME/.local/bin/dagger; else dagger_bin=dagger; fi
fi
dagger_bin=$(readlink -f "$(command -v "$dagger_bin")")
nonce=$(date -u +%Y%m%dT%H%M%S)-$$
for value in "$here" "$out"; do
  [[ "$value" =~ ^[a-zA-Z0-9_./+-]+$ ]] || { echo "unsupported path" >&2; exit 2; }
done
cd "$repo"
uptime
load1=$(awk '{print $1}' /proc/loadavg)
awk -v n="$load1" 'BEGIN {exit !(n <= 16)}' || { echo "load1=$load1 >16; reschedule" >&2; exit 3; }
{
  echo "commit=$(git rev-parse HEAD) mode=$mode nonce=$nonce dagger=$dagger_bin"
  echo "engine=fresh-dev-from-checkout spike6=1 wcprof=1"
  export DAGGER_LEAVE_OLD_ENGINE=true; "$dagger_bin" version
  uptime
  df -h "$HOME"
} > "$out/host-meta.txt" 2>&1
script=$(cat <<DSH
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
svc=\$(\$dev | container | with-exposed-port 1234 | with-env-variable _DAGGER_SPIKE6 1 | with-env-variable _DAGGER_WCPROF 1 | with-mounted-cache /var/lib/dagger \$(cache-volume gocache-g24-spike6-$nonce) | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-lab","--network-cidr","\$cidr","--debugaddr","0.0.0.0:6060" --use-entrypoint --insecure-root-capabilities)
res=\$(engine-dev | install-client --client \$(container | from alpine:3.20 | with-exec -- apk add --no-cache curl python3) --service \$svc | with-directory /spike \$(host | directory $here) | with-env-variable DAGGER_LEAVE_OLD_ENGINE true | with-env-variable NONCE $nonce | with-env-variable MODE $mode | with-exec -- sh -c 'unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; sh /spike/inner.sh > /out/runner.log 2>&1; echo \$? > /out/exitcode; cat /out/runner.log; exit 0' | directory /out)
\$res | export $out/run
DSH
)
script+=$(cat <<DSH

report=\$(container | from golang:1.26.6 | with-env-variable CGO_ENABLED 0 | with-directory /src \$(directory | with-file go.mod \$(host | file go.mod) | with-file go.sum \$(host | file go.sum) | with-directory engine/wcprof \$(host | directory engine/wcprof) | with-directory internal/enginelab/wcprofreport \$(host | directory .dagger/modules/engine-lab/wcprof-report)) | with-workdir /src | with-exec -- go build -o /wcprof-report ./internal/enginelab/wcprofreport | file /wcprof-report)
container | from alpine:3.20 | with-file /report \$report | with-file /reports.sh \$(host | file $here/reports.sh) | with-mounted-directory /in \$res | with-exec -- sh /reports.sh | directory /reports | export $out/reports
DSH
)
printf '%s\n' "$script" > "$out/invocation.dsh"
uptime
load1=$(awk '{print $1}' /proc/loadavg)
awk -v n="$load1" 'BEGIN {exit !(n <= 16)}' || { echo "load1=$load1 >16; reschedule" >&2; exit 3; }
export DAGGER_LEAVE_OLD_ENGINE=true; "$dagger_bin" -c "$script" > "$out/dagger.log" 2>&1
cat "$out/run/summary.txt"
[[ $(cat "$out/run/exitcode") == 0 && -f "$out/run/PASS" ]]
compgen -G "$out/reports/*.reports.txt" > /dev/null
echo "PASS: $out/run"
