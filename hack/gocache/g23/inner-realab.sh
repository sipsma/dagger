# Today's real Go module (A, github.com/dagger/go main) against the g23 prototype of it (B, the same
# module with fewer module calls per build, at hack/gocache/g23/gomod-proto of this harness's
# commit), on yq, on one fresh dev engine. Runs in the client container; /work is the gcexp
# workspace with yq at ./yq. G23_ARGS: "<order> <B ref>", where order "AB" runs arm A's block first,
# "BA" arm B's, and "check" runs only the equivalence checks.
# Each arm has its own workspace copy and, being a different module, its own go-mod and go-build
# cache volumes, so each arm's first build is a cold go-build volume.
# Per arm: first build, no change, edit A (warm go-build volume), no change again; then the
# equivalence checks: package keys, the root binary's digest, a non-main package's error, test
# names of pkg/yqlib, and the batch binary entries.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
REF_A=${GOMOD_REF_A:-github.com/dagger/go@334136faaa1cd36ac26dfdc648eda907ce99c063}
# G23_ARGS is "<order> <module ref for B>", e.g. "AB github.com/sipsma/dagger/hack/gocache/g23/gomod-proto@<sha>".
set -- $G23_ARGS
order=${1:-AB}
REF_B=${2:?G23_ARGS needs the B module ref}
echo "order $order; A=$REF_A; B=$REF_B" >> /out/summary.txt
for arm in A B; do
  mkdir -p /rm/$arm && cp -r /work/yq /rm/$arm/yq
  (cd /rm/$arm/yq && git init -q && git add -A && git -c user.email=g23@x -c user.name=g23 commit -qm yq)
  eval ref=\$REF_$arm
  printf '[modules.go]\nsource = "%s"\n' "$ref" > /rm/$arm/yq/dagger.toml
done
t() { arm=$1; n=$2; shift 2; s=$(ms); (cd /rm/$arm/yq && timeout 1500 dagger "$@") > /out/$arm-$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$arm-$n.txt; e=$(ms); echo "$arm-$n wall_ms=$((e-s)) :: $(tail -1 /out/$arm-$n.txt | tr '\n' ' ' | cut -c1-100)" >> /out/summary.txt; }
BUILD='go | packages | get . | binary | size'
preload() { t $1 preload -s -c 'go | packages | keys'; t $1 preload-mod -s -c 'go | module . | base | with-directory . $(go | module . | source) | with-exec go,mod,download | sync'; }
cold() { flush; t $1 first -s -c "$BUILD"; dump $1-first; t $1 noop -s -c "$BUILD"; dump $1-noop; }
edited() { flush; t $1 edit -s -c "$BUILD"; dump $1-edit; t $1 noop2 -s -c "$BUILD"; dump $1-noop2; }
check() {
  t $1 keys -s -c 'go | packages | keys'
  t $1 digest -s -c 'go | packages | get . | binary | digest'
  t $1 nonmain -s -c 'go | packages | get pkg/yqlib | binary | size'
  t $1 tests -s -c 'go | packages | get pkg/yqlib | tests | keys'
  t $1 batch -s -c 'go | packages | batch | binary | entries'
}
if [ "$order" = check ]; then
  for arm in A B; do preload $arm; check $arm; done
  for c in keys digest nonmain tests batch; do
    if diff <(grep -v -E 'Full trace|^$|wall_ms' /out/A-$c.txt) <(grep -v -E 'Full trace|^$|wall_ms' /out/B-$c.txt) > /out/diff-$c.txt; then echo "check $c: same" >> /out/summary.txt; else echo "check $c: DIFFERENT" >> /out/summary.txt; fi
  done
  exit 0
fi
first=${order%?}; second=${order#?}
preload A; preload B
cold $first; cold $second
f=pkg/yqlib/operator_add.go
for arm in A B; do
  d=/rm/$arm/yq
  if [ "$(grep -c 'unable to parse duration \[%v\]: %w' $d/$f)" != 1 ]; then echo "edit-a: expected one match in $d/$f" >> /out/summary.txt; exit 0; fi
  sed -i 's/unable to parse duration \[%v\]: %w/unable to parse the duration [%v]: %w/' $d/$f
done
edited $first; edited $second
