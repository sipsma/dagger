# Prime a variant's engine state: cold replay and cold fanout (not measured).
t() { n=$1; shift; s=$(date +%s.%N); timeout 2400 dagger -s "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(date +%s.%N); echo "$n wall=$(echo "$e - $s" | bc)s :: $(head -1 /out/$n.txt | cut -c1-120)" >> /out/summary.txt; }
t prime-replay call gcexp replay --src ./yq --salt g14 --concurrency 16 --nonce prime
t prime-fanout call gcexp fanout --n 300 --salt g14 --nonce prime
