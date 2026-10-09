# egraph-summary.py <egraph.json>...: result count, payload states, and the largest output eq classes.
import collections, json, sys
for p in sys.argv[1:]:
    g = json.load(open(p))
    rs = g.get('results') or []
    print(f"### {p.split('/')[-1]}: {len(rs)} results, {len(g.get('eq_classes') or [])} eq classes")
    print("  payload states:", dict(collections.Counter(r['payload_state'] for r in rs)))
    byeq = collections.defaultdict(list)
    for r in rs:
        for e in r.get('output_eq_class_ids') or []:
            byeq[e].append(r)
    eqd = {e['eq_class_id']: e['digests'] for e in g.get('eq_classes') or []}
    for e, m in sorted(byeq.items(), key=lambda kv: -len(kv[1]))[:3]:
        print(f"  eq {e}: {len(m)} results, {len(eqd.get(e, []))} digests, {collections.Counter(r['description'] for r in m).most_common(2)}, states {dict(collections.Counter(r['payload_state'] for r in m))}")
