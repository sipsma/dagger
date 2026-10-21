#!/bin/bash
# g24 hybrid spike on one fresh dev engine built from this checkout (so the tool's Go SDK, taken
# from sdk/go here, matches the engine). A client container bound to it runs inner.sh, which builds
# the hybrid tool and runs bench.sh in the Go image. Run from the root of a dagger/dagger checkout:
#   hack/gocache/g24/hybrid/run.sh [outdir]
# Needs the dagger CLI on PATH and yq v4.49.2 at $YQ (default ~/gocache-bench/yq). G24_ROUNDS
# (default 3) sets the rounds. INNER picks the client script (default inner.sh; inner-diag.sh is
# the cache-hit diagnostic). WCPROF=1 turns on the engine profile and writes a report beside each
# dump. DRY=1 prints the dagger script.
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(git -C "$here" rev-parse --show-toplevel)
out=${1:-$HOME/gocache-g24-hybrid-$(date -u +%Y%m%dT%H%M%SZ)}
yq=${YQ:-$HOME/gocache-bench/yq}
[ -f "$yq/go.mod" ] || { echo "missing yq at $yq" >&2; exit 2; }
id=$(date +%s%N)
mkdir -p "$out"
steal() { awk '/^cpu /{print $9}' /proc/stat; }
steal0=$(steal)
{ echo "commit $(git -C "$repo" rev-parse HEAD)"; echo "yq $(git -C "$yq" rev-parse HEAD 2>/dev/null || echo no-git)"; dagger version 2>&1 | tail -1; uptime; df -h "$HOME" | tail -1; echo "loadavg-start $(cat /proc/loadavg)"; } > "$out/meta.txt"
inner=$(cat "$here/${INNER:-inner.sh}"); inner=${inner//\'/\'\"\'\"\'}
prof=""; debug=""
if [ -n "${WCPROF:-}" ]; then prof="| with-env-variable _DAGGER_WCPROF 1 "; debug=',"--debugaddr","0.0.0.0:6060"'; fi
script=$(cat <<DSH
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
svc=\$(\$dev | container | with-exposed-port 1234 $prof| with-mounted-cache /var/lib/dagger \$(cache-volume gocache-g24-hybrid-$id) | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-lab","--network-cidr","\$cidr"$debug --use-entrypoint --insecure-root-capabilities)
engine-dev | install-client --client \$(container | from alpine:3.20 | with-exec -- apk add --no-cache bash curl) --service \$svc | with-mounted-directory /w \$(host | directory $here) | with-mounted-directory /sdk \$(host | directory $repo/sdk/go) | with-mounted-directory /y \$(host | directory $yq --exclude .git) | with-env-variable DAGGER_LEAVE_OLD_ENGINE true | with-env-variable NONCE $id | with-env-variable G24_ROUNDS "${G24_ROUNDS:-3}" | with-exec -- sh -c 'set -e; unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; $inner' | directory /out | export $out/run
DSH
)
if [ -n "${WCPROF:-}" ]; then
  script+=$'\n'"container | from golang:1.26 | with-env-variable CGO_ENABLED 0 | with-directory /src \$(directory | with-file go.mod \$(host | file $repo/go.mod) | with-file go.sum \$(host | file $repo/go.sum) | with-directory engine/wcprof \$(host | directory $repo/engine/wcprof) | with-directory internal/enginelab/wcprofreport \$(host | directory $repo/.dagger/modules/engine-lab/wcprof-report)) | with-workdir /src | with-exec -- go build -o /out/wcprof-report ./internal/enginelab/wcprofreport | file /out/wcprof-report | export $out/wcprof-report"
fi
if [ -n "${DRY:-}" ]; then echo "$script"; exit 0; fi
dagger -c "$script"
{ uptime; echo "loadavg-end $(cat /proc/loadavg)"; echo "cpu-steal-ticks $(( $(steal) - steal0 )) (USER_HZ, whole slot)"; } >> "$out/meta.txt"
if [ -n "${WCPROF:-}" ]; then
  W=$out/wcprof-report
  for d in "$out"/run/*.dump; do
    [ -f "$d" ] || continue
    {
      echo "### $(basename "$d")"
      echo "## exec phases"; $W -view classes -kind exec_phase -top 30 "$d"
      echo "## lazy ops by self"; $W -view classes -kind lazy -top 30 "$d"
      echo "## lazy ops by duration"; $W -view classes -kind lazy -sort dur -top 30 "$d"
      echo "## calls by self time"; $W -view classes -kind call -top 50 "$d"
      echo "## calls by count"; $W -view classes -kind call -sort count -top 50 "$d"
      echo "## waits"; $W -view waits -top 30 "$d"
    } > "${d%.dump}.reports.txt" 2>&1
  done
fi
echo "outputs in $out"; cat "$out/run/summary.txt"
