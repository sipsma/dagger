# counts.py <dump>...: per raw wcprof dump, call outcomes for the CLI client (the client with the
# most Function.* calls) and its engine-side span from its first op to the first gcexp call (or its last op end).
import json, sys, collections
for f in sys.argv[1:]:
    L = open(f).read().splitlines(); S = json.loads(L[0])['strings']
    ops = [e for e in map(json.loads, L[1:]) if e.get('e') == 'op']
    by = collections.defaultdict(list)
    for o in ops:
        if o.get('cl'): by[o['cl']].append(o)
    cls = lambda o: S[o.get('c', 0)]
    cli = max(by, key=lambda c: sum(cls(o).startswith('Function.') for o in by[c]))
    co = by[cli]; calls = [o for o in co if o.get('k') == 'call']
    start = min(o['s'] for o in co)
    fn = [o['s'] for o in calls if cls(o).startswith('gcexp')]
    end = min(fn) if fn else max(o['d'] for o in co)
    ex = collections.Counter(cls(o) for o in calls if o.get('o') == 'executed')
    print(f"{f}: span {(end-start)/1e6:.0f} ms; calls {len(calls)} {dict(collections.Counter(o.get('o') for o in calls))}")
    print("   top executed:", ex.most_common(6))
