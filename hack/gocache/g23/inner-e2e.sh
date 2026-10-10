# The dagger.io/go module's own checks: `dagger check` at the root of the module tree mounted at /d
# (run.sh's DSRC, a dagger/go checkout), on one fresh dev engine. Runs in the client container. The
# tree gets a git repository of its own, as a checkout has. Lists the checks, then runs them all.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
mkdir -p /m && cp -r /d/. /m && cd /m
git init -q && git add -A && git -c user.email=g23@x -c user.name=g23 commit -qm tree
t() { n=$1; shift; s=$(ms); timeout 5400 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(tail -1 /out/$n.txt | cut -c1-100)" >> /out/summary.txt; }
t list check -l
t check --progress=plain check
