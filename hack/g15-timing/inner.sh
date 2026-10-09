up() { cut -d' ' -f1 /proc/uptime; }
t() { n=$1; shift; s=$(up); timeout 1500 dagger -s "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(up); echo "$n wall=$(awk -v s=$s -v e=$e 'BEGIN{printf "%d", (e-s)*1000}')ms :: $(head -1 /out/$n.txt | cut -c1-160)" >> /out/summary.txt; curl -s http://dagger-engine:6060/debug/vars | grep -o '"g15_[a-z_]*": [0-9]*' | tr '\n' ' ' > /out/$n.vars || true; [ -s /out/$n.vars ] && echo "$n vars: $(cat /out/$n.vars)" >> /out/summary.txt || true; }
prof() { n=$1; secs=$2; shift 2; curl -s -o /out/$n.cpu "http://dagger-engine:6060/debug/pprof/profile?seconds=$secs" & p=$!; t "$n" "$@"; wait $p; go tool pprof -top -cum -nodecount=100000 /out/$n.cpu 2>/dev/null | grep -E "^Duration|canonicalEquivalentSharedResultLocked|appendDigestResultsLocked|appendOutputEqClassResultsLocked|sharedResultByResultID" | sed "s/^/$n cpu: /" >> /out/summary.txt || true; }
secs() { awk -v w=$(grep "^$1 wall=" /out/summary.txt | sed 's/.*wall=\([0-9]*\)ms.*/\1/') 'BEGIN{printf "%d", w/1000+3}'; }
R="call gcexp replay --src ./yq --salt s-LABEL --concurrency 16"
# gcexp-nostamp.patch: a salt starting with nostamp- skips the per-package stamp file.
RN="call gcexp replay --src ./yq --salt nostamp-LABEL --concurrency 16"
t replay-cold $R --nonce a
t noop-1 $R --nonce b
prof noop-2 $(secs noop-1) $R --nonce c
t noop-3 $R --nonce d
t nostamp-1 $RN --nonce s1
t nostamp-2 $RN --nonce s2
prof nostamp-3 $(secs nostamp-2) $RN --nonce s3
t nostamp-4 $RN --nonce s4
curl -s http://dagger-engine:6060/debug/vars | grep -A60 '"g15_canon_cases"' > /out/cases.txt || true
curl -s http://dagger-engine:6060/debug/vars | grep -A25 '"g15_canon_mismatches"' > /out/mismatches.txt || true
