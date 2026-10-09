# Read-only mount cost comparison (timing harness, not for merge)

Run from a dagger/dagger checkout of this branch:

    hack/g14-mount-bench/series.sh <path-to-dagger-repo> <outdir> ["A B C C B A"]

Variants: A = main dbfaa800cf; B = A + the one-transaction view lease; C = B + the
shared stat/read mount. Each variant's dev engine keeps its own state across runs;
`prime` does the cold replay and cold fanout once, then each measured run does four
no-change yq replays and three hot 300-exec fanouts. `series.log` has the per-run
wall times and uptime before and after each run.
