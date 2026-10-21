#!/bin/sh
set -eu
mkdir /reports
for dump in /in/*.dump; do
  [ -f "$dump" ] || continue
  name=$(basename "$dump" .dump)
  {
    /report -view critpath -class '^(spike5|spike-5):Spike5[.]run$' -kind call -depth 0 -top 35 "$dump"
    /report -view classes -kind exec_phase -top 30 "$dump"
    /report -view classes -kind lazy -sort dur -top 25 "$dump"
    /report -view classes -kind call -top 35 "$dump"
    /report -view waits -top 25 "$dump"
  } > "/reports/$name.reports.txt"
done
