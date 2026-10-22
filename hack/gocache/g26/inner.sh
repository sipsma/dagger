# g26: dagger.io/go at two refs on one project, on one fresh dev engine. A is dagger/go#74's head
# (the standard library and dependencies built per binary in one or two "tier" execs); B builds every
# package, the standard library and dependencies included, in its own exec. Runs in run.sh's client
# container: /y is yq v4.49.2, /d the dagger/dagger tree.
# G26_ARGS: "<order> <A ref> <B ref> <project>": order "AB" or "BA" (ABBA across series), or "check"
# (each arm's warm-up and first build only; not a comparison). project:
#   yq      builds .; edit A in pkg/yqlib; second: a copy of yq under another module path ("twin"),
#           the same dependencies, built after everything else;
#   dagger  builds ./cmd/dagger; edit D in internal/cloud/auth, edit T in internal/cmd/dagger;
#           second: ./cmd/engine from the same tree, built after everything else;
#   testbin builds the -race test binary of ./core/integration as dagger/dagger's CI does (Wolfi
#           base with cgo, mounted at /app, one -X stamp); edit D, and edit I in a test file;
#           second: ./engine/server's test binary, the same way.
# Each arm has its own copy of the tree, holding a one-file package naming the arm, so the scan
# and plan execs of one arm are never the other's. Per arm, untimed: a warm-up that builds a
# throwaway main (or test) package with -trimpath added, which runs the module download and the
# helper tools and shares no compile with the timed builds. Timed: first build, no change, each edit
# and no change again, then the second build. Each build is "binary | size" or "test-binary | size":
# built, not run. wcprof dumps for first builds, edits and second builds.
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
set -- $G26_ARGS
order=${1:?G26_ARGS needs the order}
REF_A=${2:?G26_ARGS needs the A ref}
REF_B=${3:?G26_ARGS needs the B ref}
project=${4:?G26_ARGS needs the project}
STAMP=-ldflags=-X=github.com/dagger/dagger/internal/version/buildinfo.InjectedVCSRevision=g26
WOLFI='container | from cgr.dev/chainguard/wolfi-base | with-exec -- apk add --no-cache go-1.26 build-base git ca-certificates | with-env-variable GOPATH /go | with-env-variable CGO_ENABLED 1'
case $project in
  yq) src=/y; pkg=.; second=. ;;
  dagger|testbin) src=/d ;;
  *) echo "unknown project $project" >> /out/summary.txt; exit 0 ;;
esac
case $project in
  yq|dagger)
    GO='go'; WARMGO='go --build-flags -trimpath'; KIND=binary ;;
  testbin)
    GO="go --base \$($WOLFI) --mount-path /app --build-flags -race,$STAMP"
    WARMGO="go --base \$($WOLFI) --mount-path /app --build-flags -race,$STAMP,-trimpath"; KIND=test-binary ;;
esac
case $project in
  dagger) pkg=cmd/dagger; second=cmd/engine ;;
  testbin) pkg=core/integration; second=engine/server ;;
esac
echo "order $order; project $project; A=$REF_A; B=$REF_B" >> /out/summary.txt
tree() { # $1 = arm, $2 = directory, $3 = source
  mkdir -p $2 && cp -r $3/. $2 && rm -f $2/dagger.toml $2/dagger.lock
  mkdir -p $2/g26arm && printf 'package g26arm\n\nconst Arm = "%s"\n' $1 > $2/g26arm/arm.go
  eval ref=\$REF_$1
  printf '[modules.go]\nsource = "%s"\n' "$ref" > $2/dagger.toml
  (cd $2 && git init -q && git add -A && git -c gc.auto=0 -c user.email=g26@x -c user.name=g26 commit -qm src)
}
for arm in A B; do
  tree $arm /rm/$arm/p $src
  if [ $project = yq ]; then
    # The twin: yq under another module path, so none of its own packages is yq's, with the same
    # go.mod requirements and go.sum.
    tree $arm /rm/$arm/twin $src
    (cd /rm/$arm/twin && sed -i '1s#.*#module example.com/yqtwin/v4#' go.mod &&
      find . -name '*.go' -exec grep -l 'github.com/mikefarah/yq/v4' {} + | xargs -r sed -i 's#github.com/mikefarah/yq/v4#example.com/yqtwin/v4#g' &&
      git add -A && git -c user.email=g26@x -c user.name=g26 commit -qm twin)
  fi
done
t() { arm=$1; n=$2; dir=$3; shift 3; s=$(ms); (cd $dir && timeout 3600 dagger "$@") > /out/$arm-$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$arm-$n.txt; e=$(ms); echo "$arm-$n wall_ms=$((e-s)) :: $(tail -1 /out/$arm-$n.txt | tr '\n' ' ' | cut -c1-100)" >> /out/summary.txt; }
cmd() { printf '%s | packages | get %s | %s | size' "$GO" "$1" "$KIND"; }
build() { flush; t $1 $2 /rm/$1/p -s -c "$(cmd $pkg)"; dump $1-$2; }
warm() {
  d=/rm/$1/p/g26warm; mkdir -p $d
  if [ $KIND = binary ]; then printf 'package main\n\nfunc main() {}\n' > $d/main.go
  else printf 'package g26warm\n\nimport "testing"\n\nfunc TestWarm(t *testing.T) {}\n' > $d/warm_test.go; fi
  t $1 warm /rm/$1/p -s -c "$WARMGO | packages | get g26warm | $KIND | size"
  rm -rf $d
}
if [ "$order" = check ]; then
  for arm in A B; do warm $arm; build $arm check-first; done
  exit 0
fi
edit() { # $1 = name, $2 = file, $3 = sed expression, $4 = the text it changes
  for arm in A B; do
    if [ "$(grep -c "$4" /rm/$arm/p/$2)" != 1 ]; then echo "$1: expected one match in $2" >> /out/summary.txt; exit 0; fi
    sed -i "$3" /rm/$arm/p/$2
  done
  for arm in $arms; do build $arm $1; t $arm $1-noop /rm/$arm/p -s -c "$(cmd $pkg)"; done
}
arms=$(echo "$order" | sed 's/./& /g')
for arm in $arms; do warm $arm; done
for arm in $arms; do build $arm first; t $arm noop /rm/$arm/p -s -c "$(cmd $pkg)"; done
case $project in
  yq) edit editA pkg/yqlib/operator_add.go 's/unable to parse duration \[%v\]: %w/unable to parse the duration [%v]: %w/' 'unable to parse duration \[%v\]: %w' ;;
  dagger|testbin)
    edit editD internal/cloud/auth/auth.go 's/could not acquire lock on %s: timed out/could not acquire the lock on %s: timed out/' 'could not acquire lock on %s: timed out' ;;
esac
case $project in
  dagger) edit editT internal/cmd/dagger/artifacts.go 's/addresses select different workspaces: %s and %s/addresses select two different workspaces: %s and %s/' 'addresses select different workspaces: %s and %s' ;;
  testbin) edit editI core/integration/workspace_compat_test.go 's/unexpected getent output: %q/unexpected getent output %q/' 'unexpected getent output: %q' ;;
esac
# The second build: what another project, or another binary of this one, with the same
# dependencies costs once the first is built.
for arm in $arms; do
  dir=/rm/$arm/p; [ $project = yq ] && dir=/rm/$arm/twin
  flush; t $arm second $dir -s -c "$(cmd $second)"; dump $arm-second
done
