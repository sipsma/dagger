#!/bin/bash
# all.sh <dump>...: start-up breakdown of each wcprof dump (CLI client): session phases, executed calls by
# outermost executed ancestor, the namespacing window inside module loads, and self-time by op class.
here=$(cd "$(dirname "$0")" && pwd)
for d in "$@"; do
  echo "######## $d"
  python3 "$here/phases.py" "$d"
  python3 "$here/roots.py" "$d"
  python3 "$here/nsstep.py" "$d"
  python3 "$here/sink.py" "$d" 25
done
