ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1800 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) load=$(cut -d" " -f1-3 /proc/loadavg) :: $(head -1 /out/$n.txt | cut -c1-60)" >> /out/summary.txt; }
D=http://dagger-engine:6060/debug/wcprof/dump
cp -r /ws /wsmall; cd /wsmall
t warm1 -s call gcexp chain --n 1 --salt st --nonce w1
t warm2 -s call gcexp chain --n 1 --salt st --nonce w2
for i in 1 2 3 4 5; do
  curl -sf "$D?flush=1" -o /dev/null
  t start$i -s call gcexp chain --n 1 --salt st --nonce $NONCE-$i
  curl -sf "$D?flush=1" -o /out/start$i.dump || echo "start$i dump failed" >> /out/summary.txt
done
