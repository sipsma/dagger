#!/usr/bin/env bash
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
probe_path=core/b2_capture_guard_probe_test.go
if test -e "$probe_path"; then
  echo "Refusing to overwrite $probe_path" >&2
  exit 2
fi
trap 'rm -f "$probe_path"' EXIT
cp continuation-evidence/implementation/b2/capture-guard-probe.go.txt "$probe_path"
go test ./core -run '^(TestB2CaptureGuardGap|TestCapturePersistedContainerDirectEvaluation)$' -count=1 -v
