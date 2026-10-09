ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1800 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) load=$(cut -d" " -f1-3 /proc/loadavg) :: $(head -1 /out/$n.txt | cut -c1-60)" >> /out/summary.txt; }
td() { n=$1; curl -sf "$D?flush=1" -o /dev/null; t "$@"; curl -sf "$D?flush=1" -o /out/$n.dump || echo "$n dump failed" >> /out/summary.txt; }
D=http://dagger-engine:6060/debug/wcprof/dump
cp -r /wb /wlarge; cd /wlarge
t warm1 functions
t warm2 call engine-dev --help
for i in 1 2 3; do
  td start-fn$i functions
  td start-help$i call engine-dev --help
done
