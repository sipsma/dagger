# g22: two versions of the official Go module (A and B), each in its own copy of yq on one fresh
# dev engine. Runs in the client container; /work is the gcexp workspace with yq at ./yq.
# GOMOD_REF_A is A's module ref. G23_ARGS: "<order> <B ref>", where order is "AB" or "BA", the
# order the arms' blocks run in.
# Each arm has its own workspace copy and, being a different module, its own go-mod and go-build
# cache volumes, so each arm's first build is a cold go-build volume.
# Per arm: first build, no change, edit A (warm go-build volume), no change again.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
export DAGGER_LEAVE_OLD_ENGINE=true
REF_A=${GOMOD_REF_A:-github.com/dagger/go@334136faaa1cd36ac26dfdc648eda907ce99c063}
# G23_ARGS is "<order> <module ref for B> [<module ref for C> ...]", e.g.
# "AB github.com/sipsma/dagger/hack/gocache/g23/gomod-proto@<sha>".
set -- $G23_ARGS
order=${1:-AB}
REF_B=${2:?G23_ARGS needs the B module ref}
REF_C=${3:-}
REF_D=${4:-}
REF_E=${5:-}
all="A B${REF_C:+ C}${REF_D:+ D}${REF_E:+ E}"
echo "order $order; A=$REF_A; B=$REF_B${REF_C:+; C=$REF_C}${REF_D:+; D=$REF_D}${REF_E:+; E=$REF_E}" >> /out/summary.txt
for arm in $all; do
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
arms=$(echo "$order" | sed 's/./& /g')
for arm in $arms; do preload $arm; done
for arm in $arms; do cold $arm; done
f=pkg/yqlib/operator_add.go
for arm in $all; do
  d=/rm/$arm/yq
  if [ "$(grep -c 'unable to parse duration \[%v\]: %w' $d/$f)" != 1 ]; then echo "edit-a: expected one match in $d/$f" >> /out/summary.txt; exit 0; fi
  sed -i 's/unable to parse duration \[%v\]: %w/unable to parse the duration [%v]: %w/' $d/$f
done
for arm in $arms; do edited $arm; done
