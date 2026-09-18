import datetime
import json
import os
from pathlib import Path
import shlex
import subprocess
import time

out = Path('/tmp/b1-b2-fix')
(out / 'logs').mkdir(exist_ok=True)
priv = "env GOPATH=/home/exedev/go GOCACHE=/home/exedev/.cache/go-build go test -p=1 -exec='sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private'"
checks = [
 ('build', 'go build ./...'),
 ('vet', 'go vet ./core/... ./dagql/... ./engine/...'),
 ('unfiltered', priv + ' ./core ./core/schema ./dagql -count=1 -v'),
 ('b1-race-compile', 'go test -race -c ./core -o /tmp/b1-b2-fix/core-race.test'),
 ('b1-race', "sudo -n unshare --mount --propagation private /tmp/b1-b2-fix/core-race.test -test.run '^(TestHTTPStateConcurrentResolveCapture|TestStatelessHTTPProducerIsolation|TestCompletedProducerConcurrentDemand)$' -test.count=1 -test.v"),
 ('b1-race-writer', "sudo -n unshare --mount --propagation private /tmp/b1-b2-fix/core-race.test -test.run '^TestHTTPProducerWriter$' -test.count=1 -test.v"),
 ('b2-race', "go test -p=1 -race ./dagql -run '^(TestValueTransferCapture|TestValueTransferImportPublication|TestValueTransferOfferOwners|TestValueTransferOfferCopyFailure|TestSchemaModuleSelection.*)$' -count=1 -timeout=180s -v"),
 ('b2-selected-chains', priv + " ./core -run '^TestValueTransferParts' -count=1 -timeout=180s -v"),
 ('b2-offer-restart', priv + " ./core -run '^TestValueTransferPersistenceFinalOfferRestart$' -count=1 -timeout=120s -v"),
 ('b1-native-git', "dagger api call engine-dev test --pkg ./core/integration --run='TestGit/(TestGitUncommittedLocal|TestGitUncommittedRemote|TestGitBundleRoundTripAndStockInterop|TestGitBundleImportAfterPrerequisiteRefAdvances|TestGitCommit|TestDiscardGitDir|TestGitDepth|TestGitTags)$'"),
 ('b1-native-http', "dagger api call engine-dev test --pkg ./core/integration --run='TestHTTP/(TestHTTPName|TestHTTPPermissions|TestHTTPChecksum|TestHTTPChecksumMismatch|TestHTTPTimestamp|TestHTTPETag|TestHTTPCachePerSessions|TestHTTPAuth|TestHTTPService)$'"),
 ('b1-native-restart', "dagger api call engine-dev test --pkg ./core/integration --run='TestCachePersistence/TestDiskPersistenceAcrossRestart/eager_producers_survive_restart$'"),
 ('b2-native-schema', "dagger api call engine-dev test --pkg ./core/integration --run='RemoteCacheTransferSuite/TestSchemaRecovery' --timeout=20m --test-verbose"),
 ('diff-check', 'git diff --check f035d2c2a307cdf0ab66025aef147016a02d2e8a..17384b793fe715006ffdbba4b1075354cd7733d8'),
]
results=[]
for name, cmd in checks:
 start = datetime.datetime.now(datetime.timezone.utc).isoformat()
 t = time.monotonic()
 metadata = {'name':name,'command':cmd,'started':start,'head':subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),'log':f'logs/{name}.log'}
 (out/'current.json').write_text(json.dumps(metadata,indent=2)+'\n')
 print(f'START {name}: {cmd}',flush=True)
 with (out/metadata['log']).open('wb') as log:
  result=subprocess.run(cmd,shell=True,executable='/bin/bash',stdout=log,stderr=subprocess.STDOUT)
 metadata.update(exit_status=result.returncode,seconds=round(time.monotonic()-t,3),finished=datetime.datetime.now(datetime.timezone.utc).isoformat())
 results.append(metadata)
 (out/'results.json').write_text(json.dumps(results,indent=2)+'\n')
 (out/'logs'/f'{name}-command.txt').write_text(f"Head: {metadata['head']}\nCommand: {cmd}\nStarted: {start}\nFinished: {metadata['finished']}\nElapsed seconds: {metadata['seconds']}\nExit status: {result.returncode}\nLog: {metadata['log']}\n")
 print(f"END {name}: exit {result.returncode} ({metadata['seconds']}s)",flush=True)
(out/'current.json').unlink()
