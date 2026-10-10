# Today's real Go module (A, github.com/dagger/go main) against g23 prototypes of it (B, and
# optionally C and D: the same module with fewer module calls per build, at
# hack/gocache/g23/gomod-proto, gomod-proto-c and gomod-proto-d of this harness's commit), on yq, on
# one fresh dev engine. Runs in the client container; /work is the gcexp workspace with yq at ./yq.
# G23_ARGS: "<order> <B ref> [<C ref> [<D ref>]]",
# where order lists the arms in the order their blocks run ("AB", "BA", "ACB", ...), and "check"
# runs only the equivalence checks, every arm against A.
# Each arm has its own workspace copy and, being a different module, its own go-mod and go-build
# cache volumes, so each arm's first build is a cold go-build volume.
# Per arm: first build, no change, edit A (warm go-build volume), no change again; then the
# equivalence checks: package keys, the root binary's digest, a non-main package's error, test
# names of pkg/yqlib, and the batch binary entries; and otherws, a probe module that discovers
# packages on one workspace and builds them against another (a main package added, a main package
# made a library, a nested go.mod added), where today's module discovers again.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
REF_A=${GOMOD_REF_A:-github.com/dagger/go@334136faaa1cd36ac26dfdc648eda907ce99c063}
# G23_ARGS is "<order> <module ref for B> [<module ref for C> [<module ref for D>]]", e.g.
# "AB github.com/sipsma/dagger/hack/gocache/g23/gomod-proto@<sha>".
set -- $G23_ARGS
order=${1:-AB}
REF_B=${2:?G23_ARGS needs the B module ref}
REF_C=${3:-}
REF_D=${4:-}
all="A B${REF_C:+ C}${REF_D:+ D}"
echo "order $order; A=$REF_A; B=$REF_B${REF_C:+; C=$REF_C}${REF_D:+; D=$REF_D}" >> /out/summary.txt
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
check() {
  t $1 keys -s -c 'go | packages | keys'
  t $1 digest -s -c 'go | packages | get . | binary | digest'
  t $1 nonmain -s -c 'go | packages | get pkg/yqlib | binary | size'
  t $1 tests -s -c 'go | packages | get pkg/yqlib | tests | keys'
  t $1 batch -s -c 'go | packages | batch | binary | entries'
}
# A module calling go with a Workspace other than the one its packages were discovered on.
probe() {
  d=/rm/$1/yq/.g23probe; eval ref=\$REF_$1
  mkdir -p $d
  printf 'name = "wsprobe"\nengineVersion = "v1.0.0-beta.15"\n\n[runtime]\n  source = "dang"\n\n[[dependencies]]\n  name = "go"\n  source = "%s"\n' "$ref" > $d/dagger-module.toml
  cat > $d/main.dang <<'DANG'
"""
Discovers Go packages on one workspace and builds them against another.
"""
type Wsprobe {
  let goMain(message: String!): String! {
    "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"" + message + "\") }\n"
  }

  """
  Package keys from the first workspace, then each build against a changed one.
  """
  otherWorkspace: String! {
    let first = directory
      .withNewFile("go.mod", "module example.com/probe\n\ngo 1.26.1\n")
      .withNewFile("app/app_test.go", "package main\n\nimport \"testing\"\n\nfunc TestApp(t *testing.T) {}\n")
      .withNewFile("cmd/main.go", goMain("cmd"))
      .withNewFile("tool/main.go", goMain("tool"))
    let addMain = first.withNewFile("app/main.go", goMain("app")).asWorkspace
    let dropMain = first.withNewFile("cmd/main.go", "package cmd\n").asWorkspace
    let nested = first.withNewFile("tool/go.mod", "module example.com/tool\n\ngo 1.26.1\n").asWorkspace
    let packages = go(version: "1.26.1").packages(first.asWorkspace)
    let added = { packages.get(key: "app").binary(addMain).digest } rescue { err: Error => "error: " + err.message }
    let dropped = { packages.get(key: "cmd").binary(dropMain).digest } rescue { err: Error => "error: " + err.message }
    let moved = { packages.get(key: "tool").binary(nested).digest } rescue { err: Error => "error: " + err.message }
    "keys: " + packages.keys.join(",") + "\nmain added: " + added + "\nmain dropped: " + dropped + "\nnested go.mod: " + moved + "\n"
  }
}
DANG
  printf '\n[modules.wsprobe]\nsource = "./.g23probe"\n' >> /rm/$1/yq/dagger.toml
  t $1 otherws -s -c 'wsprobe | other-workspace'
}
if [ "$order" = check ]; then
  for arm in $all; do preload $arm; check $arm; probe $arm; done
  for arm in ${all#A }; do
    for c in keys digest nonmain tests batch otherws; do
      if diff <(grep -v -E 'Full trace|^$|wall_ms' /out/A-$c.txt) <(grep -v -E 'Full trace|^$|wall_ms' /out/$arm-$c.txt) > /out/diff-$arm-$c.txt; then echo "check $arm $c: same" >> /out/summary.txt; else echo "check $arm $c: DIFFERENT" >> /out/summary.txt; fi
    done
  done
  exit 0
fi
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
