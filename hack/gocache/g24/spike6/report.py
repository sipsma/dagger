#!/usr/bin/env python3
"""Report counters in functional mode; timing summaries only for VM data."""
import json
import statistics
import sys
from collections import defaultdict

rows = [json.loads(line) for line in open(sys.argv[1]) if line.strip()]
assert rows and all(row['ok'] for row in rows)
print('Cases:', len(rows), 'calls:', sum(row['n'] for row in rows))
if not any('wall_ms' in row for row in rows):
    print('Functional check only; no timing conclusions.')
    sys.exit(0)
groups = defaultdict(list)
for row in rows:
    key = (row['experiment'], row.get('kind'), row.get('prepared'), row.get('persistent'), row.get('trim'), row['c'])
    groups[key].append(row)
for key, cases in groups.items():
    print(json.dumps({'case':key, 'rounds':len(cases), 'median_wall_ms': statistics.median(r['wall_ms'] for r in cases),
        'median_cpu_ms':statistics.median(r.get('cpu_ms', r.get('worker_cpu_ms')) for r in cases)}))
    if key[0] == 'engine':
        for field in ('total_ms', 'prepare_ms', 'ready_to_result_ms', 'release_rtt_ms'):
            values = [statistics.median(x[field] for x in r['rows']) for r in cases if field in r['rows'][0]]
            if values: print(field, statistics.median(values))
    else:
        for field in ('response_ms','including_exit_ms'):
            print(field, statistics.median(statistics.median(x[field] for w in r['workers'] for x in w['rows']) for r in cases))
print('Do not subtract pooled phase medians or sum overlapping startup/response/exit intervals.')
print('Prepared total wall and CPU include every preparation/refill; ready_to_result also includes the release HTTP request and normal teardown/publication.')
print('Module-pipes is a startup control, with shared filesystem isolation limits; persistent warm latency is not a complete Dagger-call latency.')
