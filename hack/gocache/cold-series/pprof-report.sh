#!/bin/bash
# Text reports of an engine CPU profile (from inner-cpuprof.sh), through a golang container.
# Usage: hack/gocache/cold-series/pprof-report.sh <outdir>   -> <outdir>/run/engine-cpu.{top,cum}.txt
set -euo pipefail
p=$1/run/engine-cpu.pprof
[ -s "$p" ] || { echo "no profile at $p" >&2; exit 2; }
for v in top cum; do
  flag=""; [ $v = cum ] && flag="-cum"
  dagger -c "container | from golang:1.26 | with-file /p \$(host | file $p) | with-exec -- go tool pprof -top $flag -nodecount=80 /p | stdout" > "$1/run/engine-cpu.$v.txt"
done
head -5 "$1/run/engine-cpu.top.txt"
