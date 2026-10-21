#!/bin/sh
set -eu
mkdir /reports
for dump in /in/*.dump; do
  [ -f "$dump" ] || { echo 'no profile dump' >&2; exit 1; }
  name=$(basename "$dump" .dump)
  {
    /report -view classes -kind exec_phase -top 30 "$dump"
    /report -view classes -kind lazy -sort dur -top 25 "$dump"
    /report -view classes -kind call -top 35 "$dump"
    /report -view waits -top 25 "$dump"
  } > "/reports/$name.reports.txt"
done
