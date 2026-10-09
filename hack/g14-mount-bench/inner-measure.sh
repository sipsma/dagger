# One measured run on a primed engine: 4 no-change replays, 3 hot fanouts.
t() { n=$1; shift; s=$(date +%s.%N); timeout 1500 dagger -s "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(date +%s.%N); echo "$n wall=$(echo "$e - $s" | bc)s :: $(head -1 /out/$n.txt | cut -c1-120)" >> /out/summary.txt; }
for i in 1 2 3 4; do t replay-noop-$i call gcexp replay --src ./yq --salt g14 --concurrency 16 --nonce $RUNID-$i; done
for i in 1 2 3; do t fanout-hot-$i call gcexp fanout --n 300 --salt g14 --nonce $RUNID-$i; done
