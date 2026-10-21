#!/bin/sh
set -eu
export DAGGER_LEAVE_OLD_ENGINE=true
mkdir -p /out
echo "mode=$MODE nonce=$NONCE" > /out/summary.txt
export DAGGER_LEAVE_OLD_ENGINE=true; dagger version > /out/dev-version.txt 2>&1
if [ "$MODE" = functional ]; then
  export DAGGER_LEAVE_OLD_ENGINE=true; timeout 900 dagger call check --salt "$NONCE" > /out/check.txt 2> /out/check.log
  cat /out/check.txt >> /out/summary.txt
  printf '\n' >> /out/summary.txt
  curl -sf 'http://dagger-engine:6060/debug/wcprof/dump?flush=1' -o /dev/null
  # Exercise every measurement arm; no elapsed times in functional mode.
  for kind in view withfiles export-dir bind-dir export-file bind-file workdir sealed exec-one exec-two; do
    export DAGGER_LEAVE_OLD_ENGINE=true; timeout 120 dagger call run --kind "$kind" --n 2 --c 2 --count 5 --size 1024 --salt "$NONCE-$kind" --timing=false > "/out/$kind.json" 2> "/out/$kind.log"
    cat "/out/$kind.json" >> /out/summary.txt
    printf '\n' >> /out/summary.txt
    cat "/out/$kind.json" >> /out/results.jsonl
    printf '\n' >> /out/results.jsonl
  done
  # Exercise the same report pipeline as timing mode, without a timing series.
  curl -sf 'http://dagger-engine:6060/debug/wcprof/dump?flush=1' -o /out/functional.dump
  touch /out/PASS
  exit 0
fi
# Three rounds; reverse each comparison on even rounds. 27-minute inner cap
# includes module/preparation. The host slot must allow dev-engine build too.
start=$(date +%s)
case_run() {
  name=$1; kind=$2; n=$3; c=$4; count=$5; size=$6
  now=$(date +%s)
  if [ $((now-start)) -ge 1500 ]; then echo "INCOMPLETE: 25-minute series budget" >> /out/summary.txt; exit 4; fi
  curl -sf 'http://dagger-engine:6060/debug/wcprof/dump?flush=1' -o /dev/null
  export DAGGER_LEAVE_OLD_ENGINE=true; timeout 120 dagger call run --kind "$kind" --n "$n" --c "$c" --count "$count" --size "$size" --salt "$NONCE-$name" --timing=true > "/out/$name.json" 2> "/out/$name.log"
  # stdout holds the module's JSON; progress goes to a separate log.
  printf '%s :: ' "$name" >> /out/summary.txt
  cat "/out/$name.json" >> /out/summary.txt
  printf '\n' >> /out/summary.txt
  cat "/out/$name.json" >> /out/results.jsonl
  printf '\n' >> /out/results.jsonl
  if [ "$r" = 1 ]; then curl -sf 'http://dagger-engine:6060/debug/wcprof/dump?flush=1' -o "/out/$name.dump"; fi
}
for r in 1 2 3; do
  for c in 1 8; do
    for count in 5 30 260 1400; do
      n=12
      [ "$count" -ge 260 ] && n=4
      kinds='withfiles view'; [ "$r" = 2 ] && kinds='view withfiles'
      for kind in $kinds; do case_run "r$r-c$c-a$count-$kind" "$kind" "$n" "$c" "$count" 524288; done
    done
    for size in 1024 1048576; do
      kinds='export-file bind-file workdir sealed exec-two exec-one'
      [ "$r" = 2 ] && kinds='exec-one exec-two sealed workdir bind-file export-file'
      for kind in $kinds; do case_run "r$r-c$c-s$size-$kind" "$kind" 30 "$c" 1 "$size"; done
    done
    for count in 5 30; do
      kinds='export-dir bind-dir'; [ "$r" = 2 ] && kinds='bind-dir export-dir'
      for kind in $kinds; do case_run "r$r-c$c-a$count-$kind" "$kind" 12 "$c" "$count" 524288; done
    done
  done
done
touch /out/PASS
