t() { n=$1; shift; s=$(date +%s.%N); timeout 1500 dagger -s "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(date +%s.%N); echo "$n wall=$(echo "$e - $s" | bc)s :: $(head -1 /out/$n.txt | cut -c1-120)" >> /out/summary.txt; }
git config --global user.email x@x; git config --global user.name x
cd /b/work
t gen generate -y
echo "memoizedID in gcexp client: $(grep -ro memoizedID .dagger/modules/gcexp/internal/dagger | wc -l)" >> /out/summary.txt
R="call gcexp replay --src ./yq --salt s-LABEL --concurrency 16"
t replay-cold $R --nonce a
t replay-warm $R --nonce b
t noop-1 $R --nonce d1
t noop-2 $R --nonce d2
t noop-3 $R --nonce d3
t nostamp-1 $R --nonce s1 --no-stamp
t nostamp-2 $R --nonce s2 --no-stamp
t nostamp-3 $R --nonce s3 --no-stamp
sed -i "145s/cannot be added to/cannot be added onto/" yq/pkg/yqlib/operator_add.go
t editA $R --nonce e
