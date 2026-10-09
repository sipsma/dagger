t() { n=$1; shift; s=$(cut -d' ' -f1 /proc/uptime); timeout 1500 dagger -s "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(cut -d' ' -f1 /proc/uptime); echo "$n wall=$(awk -v s=$s -v e=$e 'BEGIN{printf "%d", (e-s)*1000}')ms :: $(head -1 /out/$n.txt | cut -c1-160)" >> /out/summary.txt; }
R="call gcexp replay --src ./yq --salt s-LABEL --concurrency 16"
t replay-cold $R --nonce a
t replay-noop1 $R --nonce b
t replay-noop2 $R --nonce c
t replay-noop3 $R --nonce d
cp -a yq/pkg/yqlib/operator_add.go /tmp/a.bak; sed -i "145s/cannot be added to/cannot be added onto/" yq/pkg/yqlib/operator_add.go
t replay-editA $R --nonce e
cp -a /tmp/a.bak yq/pkg/yqlib/operator_add.go
for i in 0 1 2 3 4 5; do t startup-$i call gcexp chain --n 1 --salt st-LABEL --nonce z; done
