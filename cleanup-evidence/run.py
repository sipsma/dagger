#!/usr/bin/env python3
"""Record a serial focused check, its exact source diff, command, and result."""
import datetime
import hashlib
import json
from pathlib import Path
import shlex
import shutil
import subprocess
import sys

name, *command = sys.argv[1:]
dest = Path(__file__).resolve().parent
record = {
    "command": shlex.join(command),
    "cwd": str(Path.cwd()),
    "started_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "head": subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip(),
    "source_tree": subprocess.check_output(["git", "write-tree"], text=True).strip(),
    "free_bytes_before": shutil.disk_usage(dest).free,
}
patch = subprocess.check_output(["git", "diff", "--cached", "--binary", "HEAD"])
(dest / f"{name}.patch").write_bytes(patch)
record["patch_sha256"] = hashlib.sha256(patch).hexdigest()
with (dest / f"{name}.log").open("w") as output:
    print(record["command"], file=output, flush=True)
    result = subprocess.run(command, stdout=output, stderr=subprocess.STDOUT)
record["exit_code"] = result.returncode
record["finished_utc"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
record["free_bytes_after"] = shutil.disk_usage(dest).free
record["log_sha256"] = hashlib.sha256((dest / f"{name}.log").read_bytes()).hexdigest()
(dest / f"{name}.json").write_text(json.dumps(record, indent=2) + "\n")
print(json.dumps(record, indent=2), flush=True)
sys.exit(result.returncode)
