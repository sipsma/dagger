# g24 hybrid diagnostic: go build -toolexec hybrid of yq, in the Go image with nested Dagger.
# /seed is yq with its modules downloaded, /usr/local/bin/hybrid the tool. $IMG is the actions'
# image, $SALT the cache salt (builds of one arm share it), $REUSE whether the daemon passes on
# archives it produced (true) or uploads every input by content (false). $BUILDS lists the build
# labels to run, in order, in this one container: one nested Dagger client for all of them, each
# with its own daemon and an empty GOCACHE. Writes <label>.summary, <label>.actions.jsonl and logs
# to /out.
set -eu
OUT=/out; mkdir -p $OUT /gotmp
export GOTMPDIR=/gotmp
ms() { date +%s%3N; }
cp -r /seed /b && cd /b
for label in $BUILDS; do
  rm -f /run/h.sock
  hybrid daemon -sock /run/h.sock -image "$IMG" -gotmpdir /gotmp -salt "$SALT" -reuse="$REUSE" -log $OUT/$label.actions.jsonl > $OUT/$label.daemon.log 2>&1 &
  dpid=$!
  i=0
  until grep -q ready $OUT/$label.daemon.log 2>/dev/null; do
    i=$((i+1)); [ $i -gt 600 ] && { echo "$label daemon not ready" > $OUT/$label.summary; exit 0; }
    kill -0 $dpid 2>/dev/null || { echo "$label daemon exited" > $OUT/$label.summary; exit 0; }
    sleep 0.1
  done
  gc=$(mktemp -d /gotmp/gocache.XXXXXX)
  rm -f /gotmp/out
  rc=0
  s=$(ms)
  GOCACHE=$gc go build -toolexec "/usr/local/bin/hybrid wrap -sock /run/h.sock" -o /gotmp/out . > $OUT/$label.log 2>&1 || rc=$?
  e=$(ms)
  kill $dpid 2>/dev/null || true; wait $dpid 2>/dev/null || true
  rm -rf $gc
  v=$(/gotmp/out --version 2>&1 | head -1 || true)
  echo "$label reuse=$REUSE wall_ms=$((e-s)) rc=$rc :: $v" > $OUT/$label.summary
done
