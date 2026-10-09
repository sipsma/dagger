# Runs inside the client container (from-source CLI, bound to the dev engine as dagger-engine). Outputs in /out.
# Equivalent of PR 14486's heap repro: a changeset whose recipe carries FILES x ~100 KB withNewFile literals,
# then diffStats { path oldPath kind addedLines removedLines } (FILES entries x 5 getters), sampling the
# engine's HeapInuse (expvar memstats) every 200 ms. Per engine: cold (salt a), the same query again (warm),
# then cold with salt b. Everything runs in one session (dagger api with-session, queries POSTed with curl),
# so the base directory's ID stays valid for the later queries.
set -e
cat > /work/body.sh <<'BODY'
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060
heap() { curl -sf $D/debug/vars | tr ',' '\n' | grep -m1 '"HeapInuse"' | sed 's/[^0-9]//g' || echo 0; }
q() { curl -s -u "$DAGGER_SESSION_TOKEN:" -H content-type:application/json --data-binary @"$1" "http://127.0.0.1:$DAGGER_SESSION_PORT/query"; }
FILES=${FILES:-300}
printf '{"query":"{ directory { id } }"}' > /work/base.json
base=$(q /work/base.json | tr -d ' \n' | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
[ -n "$base" ] || { echo "no base directory id: $(q /work/base.json | head -c 300)" >> /out/summary.txt; exit 1; }
gen() {
  f=/work/q-$1.json
  { printf '{"query":"{ directory { '
    i=0; while [ $i -lt $FILES ]; do
      printf 'withNewFile(path: \\"f%03d-%s.txt\\", contents: \\"' $i "$1"; head -c 75000 /dev/urandom | base64 | tr -d '\n'; printf '\\") { '
      i=$((i+1)); done
    printf 'changes(from: \\"%s\\") { diffStats { path oldPath kind addedLines removedLines } }' "$base"
    i=0; while [ $i -lt $FILES ]; do printf ' }'; i=$((i+1)); done
    printf ' } }"}'; } > $f
  echo $f
}
run() {
  n=$1; f=$2
  curl -sf $D/debug/gc -o /dev/null || true
  b=$(heap)
  ( while :; do echo "$(ms) $(heap)" >> /out/$n.heap; sleep 0.2; done ) &
  sp=$!
  s=$(ms); q $f > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms)
  sleep 1; kill $sp; wait $sp 2>/dev/null || true
  peak=$(awk 'BEGIN{m=0} $2>m{m=$2} END{print m}' /out/$n.heap)
  grep -q '"errors"' /out/$n.txt && echo "FAILED graphql errors" >> /out/$n.txt
  echo "$n wall_ms=$((e-s)) heap_before_mb=$((b/1048576)) heap_peak_mb=$((peak/1048576)) growth_mb=$(((peak-b)/1048576)) entries=$(grep -o '"path"' /out/$n.txt | wc -l) :: $(head -c 160 /out/$n.txt | tr '\n' ' ')" >> /out/summary.txt
}
qa=$(gen a-$NONCE); qb=$(gen b-$NONCE)
ls -l $qa >> /out/summary.txt
run cold-a $qa
run warm-a $qa
run cold-b $qb
BODY
timeout 3000 dagger -s api with-session sh /work/body.sh > /out/session.txt 2>&1 || echo "with-session FAILED rc=$?" >> /out/summary.txt
