import datetime
import json
from pathlib import Path
import re
import shlex
import subprocess
import time

out = Path('/tmp/b1-b2-fix')
log = (out / 'logs/b2-native-schema.log').read_text()
trace_id = re.search(r'Full trace at https://dagger.cloud/dagger/traces/([a-z0-9]+)', log).group(1)
results = []
for name, test in [('b2-native-schema-trace', 'TestRemoteCacheTransferSuite/TestSchemaRecovery'), ('b2-native-cold-boundary', 'TestRemoteCacheTransferSuite/TestSchemaRecoveryCold')]:
    cmd = ['dagger', 'trace', trace_id, '--test', test]
    command = shlex.join(cmd)
    start = datetime.datetime.now(datetime.timezone.utc).isoformat()
    t = time.monotonic()
    metadata = {'name': name, 'command': command, 'started': start, 'head': subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip(), 'log': f'logs/{name}.log'}
    with (out / metadata['log']).open('wb') as output:
        result = subprocess.run(cmd, stdout=output, stderr=subprocess.STDOUT)
    metadata.update(exit_status=result.returncode, seconds=round(time.monotonic()-t, 3), finished=datetime.datetime.now(datetime.timezone.utc).isoformat())
    results.append(metadata)
    (out / 'trace-results.json').write_text(json.dumps(results, indent=2)+'\n')
    (out / 'logs' / f'{name}-command.txt').write_text(f"Head: {metadata['head']}\nCommand: {command}\nStarted: {start}\nFinished: {metadata['finished']}\nElapsed seconds: {metadata['seconds']}\nExit status: {result.returncode}\nLog: {metadata['log']}\n")
    print(f'{name}: exit {result.returncode}', flush=True)
