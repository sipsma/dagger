#!/bin/sh
set -eu
export DAGGER_LEAVE_OLD_ENGINE=true
mkdir -p /out
export DAGGER_LEAVE_OLD_ENGINE=true; dagger version > /out/dev-version.txt 2>&1
curl -sf 'http://dagger-engine:6060/debug/wcprof/dump?flush=1' -o /dev/null
export DAGGER_LEAVE_OLD_ENGINE=true; timeout 1500 dagger run python3 /spike/driver.py > /out/summary.txt 2> /out/driver.log
curl -sf 'http://dagger-engine:6060/debug/wcprof/dump?flush=1' -o /out/series.dump
python3 /spike/report.py /out/results.jsonl > /out/comparison.txt
