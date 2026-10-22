# g23: plain go build of a project, the reference column beside the go module's: the same tree and
# edits as inner-realpp.sh, in the Go image the go module uses for it (golang:<version>-alpine, the
# version as the module reports it), through core Dagger. Only go build is timed, inside the
# container: its own wall time, with no Dagger call around it. Runs in the client container; /work/yq
# is yq and /d is run.sh's DSRC (a dagger/dagger source tree, for the dagger project).
# G23_ARGS: "<project> [<rounds>]": project "yq" (builds ., edit A in pkg/yqlib) or "dagger" (builds
# ./cmd/dagger, edit D in internal/cloud/auth, then edit T in internal/cmd/dagger); rounds, default 4.
# GOMOD_REF_A names the go module asked for the Go version, so for the image.
# Each round, in one container exec: a fresh copy of the tree and an empty GOCACHE; go mod download,
# untimed, as the module's arms build with their downloads warm; the first build, cold (the standard
# library included), with -debug-actiongraph for Go's own action timing; a no-change build; then each
# edit and a no-change build, with the GOCACHE warm. GOOS and GOARCH are set as the module sets them.
# Untimed, after every round: each first build's action span from its graph.
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
REF=${GOMOD_REF_A:?GOMOD_REF_A names the go module asked for the Go version}
set -- $G23_ARGS
project=${1:?G23_ARGS needs the project}
rounds=${2:-4}
case $project in
  yq) src=/work/yq; pkg=.; name=yq ;;
  dagger) src=/d; pkg=./cmd/dagger; name=dagger ;;
  *) echo "unknown project $project" >> /out/summary.txt; exit 0 ;;
esac
mkdir -p /rm/P && cp -r $src /rm/P/p && rm -f /rm/P/p/dagger.toml /rm/P/p/dagger.lock
(cd /rm/P/p && git init -q && git add -A && git -c gc.auto=0 -c user.email=g23@x -c user.name=g23 commit -qm src)
printf '[modules.go]\nsource = "%s"\n' "$REF" > /rm/P/p/dagger.toml
v=$(cd /rm/P/p && timeout 2400 dagger -s -c 'go | module . | version' 2> /out/plain-version.log | tail -1)
if [ -z "$v" ]; then echo "plain: FAILED, no Go version from the go module" >> /out/summary.txt; exit 0; fi
echo "plain; project $project; rounds $rounds; image golang:$v-alpine (the go module's, from $REF)" >> /out/summary.txt
cat > /rm/plain.sh <<'EOF'
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
edit() { # $1 = file, $2 = sed expression, $3 = the text it changes
  if [ "$(grep -c "$3" "$1")" != 1 ]; then echo "edit: expected one match in $1" >> /out/plain.txt; exit 1; fi
  sed -i "$2" "$1"
}
export GOOS=$(go env GOHOSTOS) GOARCH=$(go env GOHOSTARCH)
mkdir -p /out
for r in $(seq 1 "$ROUNDS"); do
  rm -rf /tmp/src /tmp/gocache /tmp/bin
  cp -r /src /tmp/src
  cd /tmp/src
  export GOCACHE=/tmp/gocache
  go mod download
  t() { n=$1; shift; s=$(ms); go build "$@" -o /tmp/bin/$NAME $PKG; e=$(ms); echo "P$r-$n wall_ms=$((e-s))" >> /out/plain.txt; }
  t first -debug-actiongraph=/out/P$r-first-graph.json
  t noop
  case $PROJECT in
    yq)
      edit pkg/yqlib/operator_add.go 's/unable to parse duration \[%v\]: %w/unable to parse the duration [%v]: %w/' 'unable to parse duration \[%v\]: %w'
      t editA; t editA-noop ;;
    dagger)
      edit internal/cloud/auth/auth.go 's/could not acquire lock on %s: timed out/could not acquire the lock on %s: timed out/' 'could not acquire lock on %s: timed out'
      t editD; t editD-noop
      edit internal/cmd/dagger/artifacts.go 's/addresses select different workspaces: %s and %s/addresses select two different workspaces: %s and %s/' 'addresses select different workspaces: %s and %s'
      t editT; t editT-noop ;;
  esac
done
for r in $(seq 1 "$ROUNDS"); do
  echo "P$r-first actiongraph: $(GOCACHE=/tmp/spancache go run /span.go /out/P$r-first-graph.json)" >> /out/plain.txt
done
EOF
cat > /rm/span.go <<'EOF'
// The span of a go build's actions, from its -debug-actiongraph: first start to last finish.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Println("unreadable:", err)
		return
	}
	var actions []struct{ TimeStart, TimeDone time.Time }
	if err := json.Unmarshal(data, &actions); err != nil {
		fmt.Println("unparsable:", err)
		return
	}
	var first, last time.Time
	n := 0
	for _, a := range actions {
		if a.TimeStart.IsZero() || a.TimeDone.IsZero() {
			continue
		}
		if first.IsZero() || a.TimeStart.Before(first) {
			first = a.TimeStart
		}
		if a.TimeDone.After(last) {
			last = a.TimeDone
		}
		n++
	}
	fmt.Printf("%d actions run, span_ms=%d\n", n, last.Sub(first).Milliseconds())
}
EOF
(cd /rm/P/p && timeout 7200 dagger -s -c "container | from golang:$v-alpine | with-directory /src \$(host | directory . --exclude .git --exclude dagger.toml --exclude dagger.lock) | with-file /plain.sh \$(host | file /rm/plain.sh) | with-file /span.go \$(host | file /rm/span.go) | with-env-variable ROUNDS $rounds | with-env-variable PROJECT $project | with-env-variable PKG $pkg | with-env-variable NAME $name | with-env-variable NONCE $NONCE | with-exec -- sh /plain.sh | directory /out | export /out/plain") > /out/plain.log 2>&1 || echo "plain: FAILED rc=$?" >> /out/summary.txt
cat /out/plain/plain.txt >> /out/summary.txt 2>/dev/null || echo "plain: no plain.txt" >> /out/summary.txt
