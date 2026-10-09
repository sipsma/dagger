#!/bin/bash
# Start-up timing, main (A) vs typedef-accessor DoNotCache fix (B).
#
# Usage, from a checkout of this branch:
#   hack/g13-startup-timing/run.sh <gcexp-workspace-dir> <out-dir>
# Env: A, B (commits; default below), SERIES (default "A1 B1 B2 A2"),
#      LARGE=1 to time the large workspace instead (inner-large.sh): a shallow clone of this repo
#      at A, with .git, minus the dagger.toml settings that read host files under ~/.config/dagger.
#
# For each run label it builds a dev engine from that side's commit (git worktree under
# <out-dir>/wt), starts it as a service with fresh state and wcprof on, and runs inner.sh in a
# client container against the gcexp workspace: two warm-up calls, then five timed start-ups of
# `dagger call gcexp chain --n 1`, each with a wcprof dump. Results: <out-dir>/report.txt.
set -u
here=$(cd "$(dirname "$0")" && pwd)
repo=$(git -C "$here" rev-parse --show-toplevel)
small=$(cd "$1" && pwd)
mkdir -p "$2"; out=$(cd "$2" && pwd)
A=${A:-18504114ce9008963885f57a3d98176a08112849}
B=${B:-$(git -C "$here" rev-parse HEAD~1)}
for side in A B; do
  [ -d "$out/wt/$side" ] || git -C "$repo" worktree add -q --detach "$out/wt/$side" "${!side}"
done
innerf=$here/inner.sh; wb=""
if [ "${LARGE:-}" = 1 ]; then
  innerf=$here/inner-large.sh
  if [ ! -d "$out/bigws/.git" ]; then
    git init -q "$out/bigws" && git -C "$out/bigws" fetch -q --depth 1 "file://$(git -C "$repo" rev-parse --git-common-dir | xargs realpath)" "$A" \
      && git -C "$out/bigws" checkout -q --detach FETCH_HEAD && sed -i '/file:\/\/~\/.config\/dagger/d' "$out/bigws/dagger.toml"
  fi
  wb="| with-mounted-directory /wb \$(host | directory $out/bigws)"
fi
inner=$(cat "$innerf"); inner=${inner//\'/\'\"\'\"\'}
report=$out/report.txt
echo "A=$A B=$B series=${SERIES:-A1 B1 B2 A2}" | tee -a "$report"
for l in ${SERIES:-A1 B1 B2 A2}; do
  side=${l:0:1}; id=$(date +%s%N)
  echo "== $l $(git -C "$out/wt/$side" rev-parse --short HEAD) start $(date -u +%T) | $(uptime)" | tee -a "$report"
  (cd "$out/wt/$side" && timeout 3600 dagger -c "
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
svc=\$(\$dev | container | with-exposed-port 1234 | with-env-variable _DAGGER_WCPROF 1 | with-mounted-cache /var/lib/dagger \$(cache-volume g13-$l-$id) | as-service --args=\"--addr\",\"tcp://0.0.0.0:1234\",\"--network-name\",\"dagger-lab\",\"--network-cidr\",\"\$cidr\",\"--debugaddr\",\"0.0.0.0:6060\" --use-entrypoint --insecure-root-capabilities)
engine-dev | install-client --client \$(container | from alpine:3.20 | with-exec -- apk add --no-cache curl) --service \$svc | with-mounted-directory /ws \$(host | directory $small) $wb | with-env-variable NONCE $id | with-exec -- sh -c 'set -e; unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; $inner' | directory /out | export $out/$l
") > "$out/$l.log" 2>&1
  echo "== $l rc=$? end $(date -u +%T) | $(uptime)" | tee -a "$report"
  cat "$out/$l/summary.txt" | tee -a "$report"
  python3 "$here/counts.py" "$out/$l"/start*.dump 2>&1 | tee -a "$report"
done
