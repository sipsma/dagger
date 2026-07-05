# Recording cache-equivalence facts — scoping note for Erik's review

Cache Performance Analysis Track B follow-up · 2026-07-05 · analysis only, no
code written. Every claim below was checked against the actual V34 capture
files (`/tmp/whatif-cal2/`), and the empirical section changes the picture
substantially from what we assumed going in — please read §2 before §3.

## 1. The question

The calibration drifts (+235% withExec, +246% module build) were attributed
to run-unstable recipe digests: the warm run hits through the engine's
equivalence knowledge (e-graph), that knowledge is never recorded, so warm
hits cannot transfer onto the cold trace's digests and the simulator rightly
refuses. Proposed fix: record the equivalence facts. This note scopes what to
record — and reports that on the fresh captures, the premise is only half
true, in a way that changes what is worth building.

## 2. What the captures actually show (checked, not assumed)

**Finding A — the identity linkage is ALREADY recorded natively, unused.**
Every call op, hit or executed, ends with the cache result object's ID:
`profOp.EndWithResult(outcome, profResultID(res))` at `dagql/cache.go:3741`
(`profResultID` = the shared result's ID, `dagql/wcprof_hooks.go:66`). The
analyzer already carries it (`wcanalyze.Op.ResultID`, `graph.go:27`) and
nothing consumes it. These IDs are per cache store, monotonic
(`cache.go:1277`), and preserved across restarts by the persistence import
(`cache_persistence_import.go:384`). Checked on the withExec captures — the
run-unstable `Container.from` call carries the SAME result ID in both runs:

```
COLD  Container.from  xxh3:9382969610723944  executed  rid=6801  2184ms
COLD  Container.from  xxh3:bd838c8a8919c204  executed  rid=6801     0ms
WARM  Container.from  xxh3:076fc01455d1bae4  executed  rid=6801   658ms
WARM  Container.from  xxh3:bd838c8a8919c204  hit       rid=6801     0ms
```

**Finding B — the "untransferred warm hits" were the wrong suspects.** I
extracted every warm hit whose digest is absent from the cold trace and
joined on result ID: withExec 4 such hits, module build 22 — and ZERO join
to any cold-run result. Their result IDs (6807–9494, 8009–8168) are all
higher than anything the cold run created: they are hits on results the WARM
run itself produced (intra-warm derived values). No cross-run equivalence
HIT exists in either capture.

**Finding C — the run-unstable chains RE-EXECUTE warm; there is no hit to
transfer.** The table above: the unstable from-call re-ran in the warm run
(outcome executed, 658ms — the registry-resolve half re-paid), and only
afterwards was its result unified with the cold run's object (same rid
6801). The module build is the same shape, without even the unification:

```
COLD  ModuleSource.asModule  xxh3:338d592281b016ac  executed  rid=5305  14357ms
WARM  ModuleSource.asModule  xxh3:f83e04ad7a3680be  executed  rid=8167     24ms
```

The warm outer chain re-executes cheaply (24–32ms) because its heavy INNER
calls hit (the stable inner digests the general rule already exploits); the
outer results are distinct objects.

**Consequence for the drift arithmetic.** withExec: simulated counterfactual
2.59s vs warm 773ms = 1.82s gap; the from-call alone accounts for
2184ms − 658ms ≈ 1.53s of it — the difference between the COLD price and the
WARM price of the same unavoidable re-run (the recipe differs every run, so
no cache can ever hit it; warm re-runs it cheaper, presumably registry/
content locality below dagql). The rest is session-phase variance. Module
build: same story at smaller scale (522.5ms vs 151ms, mostly session/serve
phases plus cheap outer-chain re-runs). **No recorded fact can close a
cold-price-vs-warm-price gap on work both runs actually execute** — the
simulator replays the cold recording at cold prices by design. The remaining
drift is not an equivalence-data gap; it is (a) recipe instability making
the engine re-execute, and (b) the calibration metric comparing a cold-priced
simulation against a warm-priced reality for that re-executed work.

## 3. What is actually worth building, in order of value

**3a. The root-cause question first (needs your knowledge of this code): WHY
are these recipes run-unstable?** `Container.from` of the same address
produces digest `9382…` cold and `076f…` warm, while its sibling `bd83…` is
stable across runs; the `ModuleSource` chain differs every run. If the
recipe embeds a run-varying input that could be canonicalized (resolved
manifest pin, session-scoped value, local-source identity), stabilizing it
makes the warm runs REAL hits — the cold regions then elide with the
machinery that already exists, and both calibration residuals collapse
toward the session-phase floor with zero new analyzer work. This is the only
path that actually moves the two headline numbers. I can run the
digest-provenance comparison (the cache debug tracing at
`dagql/cache_debug.go` already prints request digests and inputs) if you
want the divergence pinpointed before deciding.

**3b. Consume the already-recorded result IDs — zero emit, immediate honest
ink.** The calibration and detail reports can join warm↔cold call ops on
ResultID today (pure equality on recorded values): warm hits whose digests
differ transfer when the join lands, and — the case these captures show —
cold calls whose results the warm run RE-DERIVED under another digest get
named in the report for what they are ("recipe-unstable re-run: re-paid warm
at a different price, not cacheable by recipe"), instead of being an
anonymous drift component. This converts most of the remaining +235%/+246%
into named, itemized lines. Consumption changes only; the join failure mode
is exactly today's behavior (listed untransferred, never guessed).

**3c. Emits, for when equivalence hits DO occur (they didn't here, but the
lookup supports them).** A request can hit an existing result through the
extra-digest or canonical-term paths of `lookupCacheForRequestLocked`
(`dagql/cache_egraph.go:805`; fast recipe path at `:829` needs nothing).
Native traces already record the linkage of such hits (ResultID on the call
op); the gaps are:
  - **OTel parity**: call spans carry no result ID. One integer attr set
    where the outcome stamps land today. Without it this is native-only.
  - **Cross-store portability**: result IDs are meaningless across cache
    stores. The portable key is the content digest, and the one emit point
    with everything in hand is `TeachContentDigest`
    (`dagql/cache_egraph.go:988`) — (result ID, content digest), one
    zero-duration link event per teach; the function already forks a frame
    and re-derives digests, so the cost is noise, and there is already a
    debug hook with this exact signature (`traceTeachContentDigest`,
    `cache_debug.go:558`). Volume bounded by content materializations,
    never by hits.

**What is NOT worth recording** (and why): eq-class merges
(`mergeEqClassesLocked`, `cache_egraph.go:338`) and their repair cascades
(`:375`), term creation, wait-result indexing
(`indexWaitResultInEgraphLocked`, `:1400`), full digest sets of classes —
hot paths under `egraphMu`, unbounded cardinality, and everything the
analyzer needs from them surfaces at the endpoints above at the only moments
that matter (a hit served, a result unified, a content digest taught).
`teachResultIdentityLocked` (`:1264`) has the same information as the hit
choke point but runs with the lock held — wrong place.

## 4. Predicted effect on the V34 numbers — honest version

- **Equivalence facts/joins alone (3b+3c): the headline drifts barely
  move.** No cross-run hits exist in these captures to transfer. The gain is
  explanatory: the report itemizes the residual (re-run price variance
  ~1.5s withExec; session phases ~0.3–0.4s both) instead of presenting an
  opaque +235%. That is worth having — it makes the number trustworthy — but
  it is not a smaller number.
- **Recipe stabilization (3a), if the instability turns out fixable:**
  withExec's from-call becomes a warm hit; its cold 2184ms region elides;
  counterfactual ≈ session floor (~0.4–0.6s) vs warm 773ms — drift drops to
  tens of percent. Module build similarly approaches its session floor.
- **Remaining after both:** session/serve phases (no caching hypothesis
  removes them), scheduling granularity, and cold/warm environmental
  variance on any work that legitimately re-runs.

## 5. Guardrails

- All emits profiling-gated, evaluation behavior untouched, suppression
  counted under the same discipline as the four landed recording changes.
- Every consumption is a counted, printed equality join on recorded values;
  no name matching, no similarity, no fallback when a join misses — the miss
  stays listed exactly as today.
- Nothing samples down silently; if content-teach volume ever matters,
  dedupe per (result ID, digest), never drop.
