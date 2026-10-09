# North Star capture (runs in the client container; /work = gcexp workspace with yq at ./yq).
# Plain references (one go build exec, GOCACHE in a fresh cache volume): cold, then no change with the
# warm volume (forced re-run), then edit A with the warm volume (incremental).
# Per-package replays, each profiled: first build (first-c16.dump), no change (noop1.dump), edit A
# (edit-a.dump). A plain preload first pulls the image, loads the module and downloads modules.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-200)" >> /out/summary.txt; }
stamps() { grep -E '^[0-9a-f]{12} [0-9]+ms$' "/out/$1.txt" | cut -d' ' -f1 | sort > "/out/$1.stamps"; }
reran() {
  comm -13 "/out/$1.stamps" "/out/$2.stamps" > "/out/$2.new"
  pk=""; while read -r s; do pk="$pk $(grep -A1 "^$s " "/out/$2.txt" | tail -1)"; done < "/out/$2.new"
  echo "$2 re-ran vs $1: $(wc -l < "/out/$2.new") packages:$pk" >> /out/summary.txt
}
D=http://dagger-engine:6060/debug/wcprof/dump
R="-s call gcexp replay --src ./yq --salt r-$NONCE --concurrency 16"
P="-s call gcexp plain --src ./yq"
V=gocache-ns-$NONCE
t preload $P --volume "" --salt pre-$NONCE --nonce p
t plain-cold $P --volume $V --salt pv-$NONCE --nonce pc
t plain-noop $P --volume $V --salt pv-$NONCE --nonce pn
curl -sf "$D?flush=1" -o /dev/null
t first-c16 $R --nonce f
curl -sf "$D?flush=1" -o /out/first-c16.dump
t noop1 $R --nonce n1
curl -sf "$D?flush=1" -o /out/noop1.dump
# Failures below are recorded, not fatal, so the dumps above are still exported.
f=yq/pkg/yqlib/operator_add.go
if [ "$(grep -c 'unable to parse duration \[%v\]: %w' $f)" = 1 ]; then
  sed -i 's/unable to parse duration \[%v\]: %w/unable to parse the duration [%v]: %w/' $f
  t edit-a $R --nonce ea
  curl -sf "$D?flush=1" -o /out/edit-a.dump || echo "edit-a: dump failed" >> /out/summary.txt
  t plain-edit $P --volume $V --salt pv-$NONCE --nonce pe
  { stamps first-c16 && stamps noop1 && stamps edit-a && reran first-c16 noop1 && reran noop1 edit-a; } || echo "re-run count failed" >> /out/summary.txt
else
  echo "edit-a: expected one match in $f; edit skipped" >> /out/summary.txt
fi
