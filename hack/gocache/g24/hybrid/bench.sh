# g24 hybrid spike: plain go build against go build -toolexec hybrid, where every compile, asm and
# link runs as a Dagger exec and Dagger's own exec cache is the build cache.
# Runs in the Go image with nested Dagger: /seed is yq, /usr/local/bin/hybrid the tool, $IMG the
# image the actions run in (the same one), $NONCE a per-run salt, $ROUNDS the rounds.
# Each arm: a cold first build (fresh caches, so a cold standard library), item 4's edit A, then
# no change. plain keeps Go's local GOCACHE across its three builds (the speed reference). hybrid
# gives Go an empty GOCACHE on every build, so every action reaches the daemon and its hits come
# only from Dagger's cache; each round has its own salt, so rounds start cold. One daemon per build,
# started before the timer (its start is logged separately). KEYS=1 also logs each action's
# arguments, environment and input digests (extra queries; for functional checks, not timing).
set -eu
OUT=/out; mkdir -p $OUT /gotmp
ROUNDS=${ROUNDS:-3}
ms() { date +%s%3N; }
export GOTMPDIR=/gotmp
echo "image $IMG; rounds $ROUNDS; nonce $NONCE; $(go version); nproc $(nproc)" > $OUT/summary.txt
(cd /seed && go mod download) > $OUT/download.log 2>&1
daemon_start() { # $1 = label, $2 = salt
  rm -f /run/h.sock
  s=$(ms)
  keys=""; [ -n "${KEYS:-}" ] && keys="-keys $OUT/$1.keys.jsonl"
  hybrid daemon -sock /run/h.sock -image "$IMG" -gotmpdir /gotmp -salt "$2" -log $OUT/$1.actions.jsonl $keys > $OUT/$1.daemon.log 2>&1 &
  dpid=$!
  i=0
  until grep -q ready $OUT/$1.daemon.log 2>/dev/null; do
    i=$((i+1)); [ $i -gt 600 ] && { echo "$1 daemon not ready" >> $OUT/summary.txt; return 1; }
    kill -0 $dpid 2>/dev/null || { echo "$1 daemon exited" >> $OUT/summary.txt; return 1; }
    sleep 0.1
  done
  echo "$1 daemon_start_ms=$(( $(ms) - s ))" >> $OUT/summary.txt
}
daemon_stop() { kill $dpid 2>/dev/null || true; wait $dpid 2>/dev/null || true; }
build() { # $1 = label, $2 = arm
  rc=0
  rm -f /gotmp/out
  if [ "$2" = hybrid ]; then
    daemon_start $1 "$NONCE-$salt" || return 0
    gc=$(mktemp -d /gotmp/gocache.XXXXXX)
    s=$(ms)
    GOCACHE=$gc go build -toolexec "/usr/local/bin/hybrid wrap -sock /run/h.sock" -o /gotmp/out . > $OUT/$1.log 2>&1 || rc=$?
    e=$(ms)
    daemon_stop
    rm -rf $gc
  else
    s=$(ms)
    go build -o /gotmp/out . > $OUT/$1.log 2>&1 || rc=$?
    e=$(ms)
  fi
  v=$(/gotmp/out --version 2>&1 | head -1 || true)
  echo "$1 wall_ms=$((e-s)) rc=$rc :: $v" >> $OUT/summary.txt
}
r=1
while [ $r -le $ROUNDS ]; do
  if [ $((r % 2)) = 1 ]; then arms="plain hybrid"; else arms="hybrid plain"; fi
  for arm in $arms; do
    salt=r$r
    rm -rf /b && cp -r /seed /b && cd /b
    export GOCACHE=/gotmp/plain-cache-r$r
    rm -rf $GOCACHE
    build r$r-$arm-first $arm
    sed -i 's/unable to parse duration \[%v\]: %w/unable to parse the duration [%v]: %w/' pkg/yqlib/operator_add.go
    build r$r-$arm-edit $arm
    build r$r-$arm-noop $arm
    cd /
  done
  r=$((r + 1))
done
