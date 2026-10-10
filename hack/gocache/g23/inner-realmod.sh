# E0: today's real Go module (github.com/dagger/go main) on yq, against plain go build and the
# per-package replay, all on the same fresh dev engine. Runs in the client container; /work is the
# gcexp workspace with yq at ./yq.
# The real module builds yq's root main package with one go build exec over the module source, with
# its own go-mod and go-build cache volumes. Before measuring: the module is loaded, its scanner
# helper is built, its base image is pulled and yq's modules are downloaded into its go-mod volume,
# so the measured first build is a cold go-build volume, like plain-cold.
# Steps, each timed as a whole dagger invocation:
#   realmod-first, realmod-noop (nothing changed), realmod-edit (edit A, warm go-build volume),
#   realmod-noop2 (no change after the edit);
#   plain-cold / plain-noop / plain-edit (gcexp plain with a fresh GOCACHE volume; noop forced);
#   replay-first / replay-noop / replay-edit (gcexp replay, driver v1, stamps off), for reference.
# ORDER=A runs the real-module block before the plain block, ORDER=B after it (ABBA across slots).
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(tail -1 /out/$n.txt | tr '\n' ' ' | cut -c1-160)" >> /out/summary.txt; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
order=${G23_ARGS:-A}
echo "order $order" >> /out/summary.txt
GOMOD=${GOMOD_REF:-github.com/dagger/go@334136faaa1cd36ac26dfdc648eda907ce99c063}
# The real-module workspace: a copy of yq in its own git repository (a workspace's root is found by
# walking up to .git), with the module installed.
mkdir -p /rm && cp -r /work/yq /rm/yq
(cd /rm/yq && git init -q && git add -A && git -c user.email=g23@x -c user.name=g23 commit -qm yq)
printf '[modules.go]\nsource = "%s"\n' "$GOMOD" > /rm/yq/dagger.toml
BUILD='go | packages | get . | binary | size'
rm_() { (cd /rm/yq && t "$@"); }
R="-s call gcexp replay --src ./yq --salt r-$NONCE --concurrency 16 --stamps=false"
P="-s call gcexp plain --src ./yq"
V=gocache-g23-$NONCE
# Preloads: the real module (load, scanner helper and scan, base image, yq's module downloads into
# its go-mod volume; its go-build volume stays cold for yq's packages), then gcexp (load, golang
# image, module downloads, pack tool).
rm_ preload-realmod -s -c 'go | packages | keys'
rm_ preload-realmod-mod -s -c 'go | module . | base | with-directory . $(go | module . | source) | with-exec go,mod,download | sync'
t preload $P --volume "" --salt pre-$NONCE --nonce p
t preload-pack -s call gcexp pack-tool
cold_realmod() {
  flush; rm_ realmod-first -s -c "$BUILD"; dump realmod-first
  rm_ realmod-noop -s -c "$BUILD"; dump realmod-noop
}
cold_plain() {
  flush; t plain-cold $P --volume $V --salt pv-$NONCE --nonce pc
  t plain-noop $P --volume $V --salt pv-$NONCE --nonce pn
}
edit_realmod() {
  flush; rm_ realmod-edit -s -c "$BUILD"; dump realmod-edit
  rm_ realmod-noop2 -s -c "$BUILD"; dump realmod-noop2
}
edit_plain() { flush; t plain-edit $P --volume $V --salt pv-$NONCE --nonce pe; }
if [ "$order" = A ]; then cold_realmod; cold_plain; else cold_plain; cold_realmod; fi
flush
t replay-first $R --nonce f
dump replay-first
t replay-noop $R --nonce n1
dump replay-noop
# Edit A in both copies of yq.
f=pkg/yqlib/operator_add.go
for d in /work/yq /rm/yq; do
  if [ "$(grep -c 'unable to parse duration \[%v\]: %w' $d/$f)" != 1 ]; then echo "edit-a: expected one match in $d/$f" >> /out/summary.txt; exit 0; fi
  sed -i 's/unable to parse duration \[%v\]: %w/unable to parse the duration [%v]: %w/' $d/$f
done
if [ "$order" = A ]; then edit_realmod; edit_plain; else edit_plain; edit_realmod; fi
flush
t replay-edit $R --nonce ea
dump replay-edit
