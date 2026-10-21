#!/usr/bin/env bash
# One project, one Dagger exec sequence. No dev engine and no cache volumes.
set -euo pipefail
export DAGGER_LEAVE_OLD_ENGINE=true
here=$(cd "$(dirname "$0")" && pwd)
repo=$(git -C "$here" rev-parse --show-toplevel)
out=${1:?usage: run.sh OUTPUT_DIRECTORY [yq|dagger] [functional|timing] [rounds]}
project=${2:-yq}
mode=${3:-functional}
rounds=${4:-1}
image=${SPIKE7_IMAGE:-golang:1.26.6@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6}
parallel=${SPIKE7_PARALLEL:-4}
budget=${SPIKE7_BUDGET_MINUTES:-25}
materialization=${SPIKE7_MATERIALIZATION:-copy}
workers=${SPIKE7_WORKERS:-64}
# Resolve before changing directories, including relative override/PATH entries.
dagger_bin=${DAGGER_BIN:-}
if [[ -z "$dagger_bin" ]]; then
  if [[ -e "$HOME/.local/bin/dagger" ]]; then
    dagger_bin="$HOME/.local/bin/dagger"
  else
    dagger_bin=dagger
  fi
fi
dagger_bin=$(command -v "$dagger_bin") || { echo "Dagger CLI not found" >&2; exit 2; }
[[ -f "$dagger_bin" && -x "$dagger_bin" ]] || { echo "Dagger CLI is not executable: $dagger_bin" >&2; exit 2; }
dagger_bin=$(readlink -f "$dagger_bin")
case "$project" in
  yq) source=${YQ:-$HOME/gocache-bench/yq} ;;
  dagger) source=${DSRC:-$repo} ;;
  *) echo "unknown project" >&2; exit 2 ;;
esac
case "$mode" in functional|timing) ;; *) exit 2 ;; esac
[[ -d "$source" && -f "$source/go.mod" ]] || { echo "missing source: $source" >&2; exit 2; }
[[ "$rounds" =~ ^[1-9][0-9]*$ && "$parallel" =~ ^[1-9][0-9]*$ && "$budget" =~ ^[1-9][0-9]*$ && "$workers" =~ ^[1-9][0-9]*$ ]] || exit 2
case "$materialization" in copy|direct) ;; *) exit 2 ;; esac
# Keep generated Dagger shell arguments unambiguous. No eval.
for value in "$here" "$source" "$out" "$image"; do
  [[ "$value" =~ ^[a-zA-Z0-9_./:@+-]+$ ]] || { echo "unsupported shell path/image: $value" >&2; exit 2; }
done
[[ ! -e "$out" ]] || { echo "output must be new: $out" >&2; exit 2; }
mkdir -p "$out"
out=$(cd "$out" && pwd)
commit=$(git -C "$repo" rev-parse HEAD)
source_commit=$(git -C "$source" rev-parse HEAD)
if [[ "$project" == yq ]]; then
  [[ "$source_commit" == 7ccaf8e700ce99eb3f0f6cef7f5930a0b3c827cd ]] || { echo "expected yq v4.49.2" >&2; exit 2; }
fi
git -C "$source" diff --quiet
git -C "$source" diff --cached --quiet
nonce=$(date -u +%Y%m%dT%H%M%S)-$$
{
  echo "harness=$commit source=$source_commit image=$image nonce=$nonce"
  echo "project=$project mode=$mode rounds=$rounds"
  echo "dagger_bin=$dagger_bin"
  uptime
  df -h "$HOME"
} > "$out/host-meta.txt"
script=$(cat <<DSH
tool=\$(container | from $image | with-env-variable GOMAXPROCS 4 | with-env-variable GOCACHE /tool-cache | with-env-variable GOTMPDIR /tool-tmp | with-env-variable TMPDIR /tool-tmp | with-env-variable GOTOOLCHAIN local | with-directory /tool \$(host | directory $here) | with-workdir /tool | with-exec -- sh -eu -c 'mkdir -p /tool-tmp; go test -race -count=1 ./...; CGO_ENABLED=0 go build -o /spike7 .; rm -rf /tool-cache /tool-tmp' | file /spike7)
container | from $image | with-directory /seed \$(host | directory $source --exclude .git --exclude "**/node_modules" --exclude .item4-results --exclude .g22-results) | with-file /spike7 \$tool | with-exec -- /spike7 prepare --out /out/prepared | with-env-variable SPIKE7_NONCE $nonce | with-env-variable SPIKE7_IMAGE $image | with-env-variable SPIKE7_COMMIT $commit | with-env-variable SPIKE7_SOURCE_COMMIT $source_commit | with-exec -- sh -c '/spike7 series --project $project --mode $mode --rounds $rounds --parallel $parallel --workers $workers --budget-minutes $budget --materialization $materialization > /out/runner.log 2>&1; rc=\$?; echo \$rc > /out/exitcode; df -h > /out/disk.txt; cat /out/runner.log; exit 0' | directory /out | export $out/run
DSH
)
printf '%s\n' "$script" > "$out/invocation.dsh"
# Keep any CLI-generated lockfile with the artifacts, outside the checkout.
cd "$out"
# Enforce the shared-host load rule even for this functional check. The optional
# wrapper is supplied by the caller, not required by the linux-vm harness.
uptime
load1=$(awk '{print $1}' /proc/loadavg)
if awk -v n="$load1" 'BEGIN {exit !(n <= 16)}'; then
  echo "engine=cli-default" >> "$out/host-meta.txt"
  export DAGGER_LEAVE_OLD_ENGINE=true; "$dagger_bin" version >> "$out/host-meta.txt" 2>&1
  export DAGGER_LEAVE_OLD_ENGINE=true; "$dagger_bin" -m core -c "$script" > "$out/dagger.log" 2>&1
else
  [[ "$mode" == functional && -n "${SPIKE7_CLOUD_WRAPPER:-}" ]] || { echo "load1=$load1 > 16; reschedule" >&2; exit 3; }
  # The explicit cloud flag remains even if the wrapper attempts its local
  # fallback. The shim also honors overrides whose filename is not "dagger".
  mkdir -p "$out/cli-bin"
  ln -s "$dagger_bin" "$out/cli-bin/dagger"
  export PATH="$out/cli-bin:$PATH"
  echo "engine=cloud" >> "$out/host-meta.txt"
  export DAGGER_LEAVE_OLD_ENGINE=true; "$SPIKE7_CLOUD_WRAPPER" g24-spike7-version --engine=cloud version >> "$out/host-meta.txt" 2>&1
  export DAGGER_LEAVE_OLD_ENGINE=true; "$SPIKE7_CLOUD_WRAPPER" g24-spike7 --engine=cloud -m core -c "$script" > "$out/dagger.log" 2>&1
fi
cat "$out/run/summary.txt"
[[ $(cat "$out/run/exitcode") == 0 && -f "$out/run/PASS" ]]
echo "PASS: $out/run"
