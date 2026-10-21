#!/bin/sh
set -eu
export DAGGER_LEAVE_OLD_ENGINE=true
cp -a /prepared /out/prepared
export DAGGER_LEAVE_OLD_ENGINE=true; dagger version > /out/dev-version.txt 2>&1
# All Go runs are inside this golang container. The CLI opens one long-lived
# dev-engine session; each helper uses its loopback GraphQL endpoint/attachables.
export DAGGER_LEAVE_OLD_ENGINE=true; timeout 1500 dagger run /spike9 series --project "$PROJECT" --mode "$MODE" --rounds "$ROUNDS" --parallel "$PARALLEL" --workers 64 --budget-minutes 24 --materialization copy
