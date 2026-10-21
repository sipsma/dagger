#!/usr/bin/env python3
"""Summarize existing spike9 outputs. Never launches builds."""
import json
import statistics
import sys
from pathlib import Path

root = Path(sys.argv[1])
for project in ('yq', 'dagger'):
    path = root / project / 'results.jsonl'
    if not path.exists():
        continue
    rows = [json.loads(line) for line in path.read_text().splitlines()]
    print(f'{project}: {len(rows)} builds')
    if any(row['Mode'] != 'timing' or 'WallNS' not in row for row in rows):
        print('  functional evidence only; timing comparison suppressed')
        continue
    for phase in ('first', 'edit', 'noop'):
        arms = {arm: [r for r in rows if r['Arm'] == arm and r['Phase'] == phase]
                for arm in ('plain', 'engine')}
        if any(not v for v in arms.values()):
            print(f'  {phase}: incomplete')
            continue
        med = {arm: statistics.median(r['WallNS'] for r in rs) / 1e9
               for arm, rs in arms.items()}
        print(f"  {phase}: plain={med['plain']:.6f}s engine={med['engine']:.6f}s "
              f"delta={med['engine']-med['plain']:+.6f}s ratio={med['engine']/med['plain']:.3f} "
              f"n={len(arms['plain'])}/{len(arms['engine'])}")
        plain_rounds = {r['Round']: r for r in arms['plain']}
        pairs = [(r['WallNS'], plain_rounds[r['Round']]['WallNS'])
                 for r in arms['engine'] if r['Round'] in plain_rounds]
        print(f"    paired median delta={statistics.median((e-p)/1e9 for e,p in pairs):+.6f}s "
              f"ratio={statistics.median(e/p for e,p in pairs):.3f}")
        for r in arms['engine']:
            a = r['Analysis']; kinds = a['by_kind']; g = kinds['get']; p = kinds.get('put', {})
            ms = lambda obj, key: obj.get(key, 0) / 1e6
            print(f"    r{r['Round']}: get={g['count']} hit={g['hits']} miss={g['misses']} put={p.get('count',0)} "
                  f"get-hit p50/p95={ms(g,'GetHitP50NS'):.3f}/{ms(g,'GetHitP95NS'):.3f}ms "
                  f"get-miss={ms(g,'GetMissP50NS'):.3f}/{ms(g,'GetMissP95NS'):.3f}ms "
                  f"put={ms(p,'PutP50NS'):.3f}/{ms(p,'PutP95NS'):.3f}ms "
                  f"nonempty-export={ms(g,'ExportP50NS'):.3f}/{ms(g,'ExportP95NS'):.3f}ms "
                  f"export-bytes={g.get('MaterializedBytes',0)} "
                  f"put-bytes={p.get('BodyBytes',0)} "
                  f"RPCs={sum(s['EngineRequests'] for s in kinds.values())} "
                  f"mapped={a['visibility']['direct_or_known_subkey_fraction']:.3f} "
                  f"offline-analysis={r.get('AnalysisNS',0)/1e9:.6f}s")
print('Real remote service lookups/pulls: UNMEASURED. Per-request intervals overlap; do not sum them into wall time.')
