# Cache-invalidation tracing: design

Status: revision 2 after adversarial review round 1 (verdict: reject; all accepted findings folded
in) · 2026-07-07 · mechanism claims verified against the code at `4bc3d9404` (the commit this doc's
branch forks from); every claim carries its deciding code path.

## 1. Goal and non-goals

**The user-facing question:** *"why didn't I get a cache hit?"* Given an operation that was not
cached, trace the path: which input was invalidated, through which intermediate inputs, down to the
fundamental root causes — and answer each root cause in the engine's own semantics. The destination
is a Dagger Cloud feature (local engines keep no cross-run history); the stepping stone is an
offline analyzer over profiler captures, built first, whose algorithm the Cloud implementation
ports rather than reinvents. The same machinery answers the cross-run question ("cached in run A
but not in run B") — it is folded into this design, not a separate track. It also eventually serves
remote caching (explaining why a candidate export didn't pay off), but the design optimizes for the
user-facing question first.

**Framing rule (binding, from Erik):** deliberate cache-key scoping — per-client, per-session,
per-call, per-schema implicit inputs — is carefully designed, correct, and elegant. An expected
miss is NOT a defect and this feature never frames it as "instability." *"The engine deliberately
does not cache this across sessions, because X"* is a first-class, semantically correct answer.
Likewise, a result that lived and died with a previous session is an *expected* miss ("things you
should absolutely never cache across sessions" — Erik). Any criticism of engine behavior voiced by
this feature's output or docs must be justified by reading the relevant code in full first.

**Non-goals:** predicting other runs' prices (see whatif design §2); content reuse below the dagql
cache; changing any engine caching semantics; inferring facts the captures don't record (the
analyzer refuses or labels instead — rational-function doctrine, whatif design §1, applies
verbatim).

## 2. Ground truth: how a miss actually happens

The single decision path is `Cache.getOrInitCallInner` → `lookupCacheForRequestLocked`
(`dagql/cache_egraph.go:805-861`), with these terminals, in order:

1. **Pre-lookup:** `DoNotCache` requests never reach lookup (`dagql/cache.go:3765`); a
   recursive-call guard aside, everything else derives the recipe digest (implicit inputs — the
   scope values — hash into it: `result_call_frame.go:633-726`) and the structural inputs.
2. **Exact-digest lookup** over `egraphResultsByDigest` (`cache_egraph.go:658-673`), then
   **extra-digest (equivalence) lookup** (`:674-680`). Candidate collection *silently skips
   TTL-expired results* (`resultExpiredAtLocked`, `:554-559`, applied at `:587, :602`).
3. **Structural-term lookup** (`lookupMatchForCallLocked`, `:683-734`): resolves each input digest
   to its equivalence class; **aborts recording `missingInputIndex`** — the index of the first
   input digest the cache has never seen — else looks up candidates by
   `hash(selfDigest, input eq-classes)`.
4. **Session-resource filtering** (`selectLookupCandidateForSessionLocked`, `:646-656`): a
   semantically-equivalent result is unusable if this session lacks its required resources
   (secrets, sockets — `sessionSatisfiesResourceRequirementsLocked`, `:632-644`).
5. On miss: in-flight **join** if an execution with the same call digest AND the same concurrency
   key is running (`callKey + ConcurrencyKey`, default per-client — `cache.go:1348-1350,
   :3882-3895`), else **execute**; errored executions publish no result (`cache.go:4193-4210`).

Completeness notes (round-1 findings, accepted): (a) a second lookup entry exists — the
digest-only path used by ID/recipe loading (`lookupCacheForDigests`, `cache.go:4030-4118`, called
from `server.go:1402`); its misses surface as ordinary executed calls in captures, but E1 must
classify at BOTH entries so the digest-only path cannot produce unlabeled misses. (b) A selected
hit can still fail while loading its persisted payload (`cache_egraph.go:915-933`,
`cache.go:4094-4112`) — a distinct terminal, `persisted_load_failed`. (c) Unclean-shutdown
persistence resets wipe the whole store at startup (`cache.go:136-183`) — not a lookup terminal,
but it bounds what any "was cached before" statement can claim (see category 2). (d) The
recursive-call guard error is excluded: it is a request error, not a cache miss a user asks about.

The engine already narrates these terminals to a debug-gated tracer
(`cache_debug.go:644-660`: `traceLookupAttempt` / `traceLookupMissNoMatch` / `traceLookupHit`) —
the right seam exists; it is neither capture-borne nor terminal-complete today (it does not
distinguish expired or session-filtered candidates from plain no-match).

## 3. The answer model

### 3.1 The frontier walk

Given uncached op X in a capture, walk X's recorded cache-input digests (`Op.CacheInputs` — both
sources since `bb7625e2c`; ordered exactly as the recipe hash consumes them) downward: an input
that was a hit is a boundary; an input that missed is walked recursively. The
**miss frontier** = the deepest uncached digests whose own inputs are all hits or leaves. The walk
operates on **digest nodes**, not op instances: one digest can legitimately have both executed and
hit calls in one capture (first demand executes, later demands hit). A digest node's status is the
well-defined per-digest summary — *missed-at-first-demand* if any call recorded executed/joined,
*cached-from-the-start* iff every call recorded a hit. Everything
between X and the frontier missed as *Merkle collateral* (its digest changed because an input's
digest changed) and is shown as the path, not the cause. The output is the **ranked frontier**
(there can be several independent origins), never a single guessed origin.

### 3.2 The root-cause taxonomy

Each frontier origin gets exactly one category. Categories 1–2 are first-class correct-behavior
answers. Every category is a pure function of recorded data; where the data cannot decide, the
answer is the stated *undetermined* form, never a guess.

| # | Category | Answer text (shape) | Decided by | Code anchor |
|---|---|---|---|---|
| 1 | **Deliberately scoped** | "not cached across clients/sessions/calls/schemas, by design, because …" (per-scope why-text; `from` tag vs pinned nuance) | scope implicit-input names: recorded in OTel traces inside `dag.call` (`callpbv1.Call.implicitInputs`) but **discarded by today's loader** — surfacing it is Chunk-1 loader work, not new emit; native needs **E2** | `dagql/cache_inputs.go:14-92`; `core/schema/container.go:1015-1046`; `core/schema/modulesource.go:64-65`; loader gap: `wcotel/loader.go:383-439` |
| 2 | **Not retained from a previous run** | "computed in a previous run; no longer in the cache. The engine's designed lifetime mechanisms (session release, pruning, persistence policy/reset) decide retention — which one applied here is not recorded" | same digest recorded executed in a prior capture of the pair/history; no scope inputs. **Deliberately does not claim WHICH mechanism** (round-1 finding accepted: that would be inference — release `cache.go:713-752`, persisted survival `cache_pruning.md`, reset `cache.go:136-183` are all real) | first-class expected-miss family; mechanism text upgradeable only by a future retention fact, refused for now (§4) |
| 3 | **Input changed** | "input #k changed: <digest A> → <digest B>; walk continues into it" | cross-run pairing (§5): paired parent, input k differs | `missingInputIndex` mirrors this in-engine (`cache_egraph.go:709-717`) |
| 4 | **New work** | "first appearance of this call in the available history" | digest absent from every prior capture in scope | — (absence over the available history, stated as such) |
| 5 | **Expired (TTL)** | "a result existed but its TTL had expired" | **terminal fact only** — underivable offline today | `cache_egraph.go:554-559,:587,:602` |
| 6 | **Session-resource filtered** | "an equivalent result exists but requires session resources this session lacks" | **terminal fact only** — underivable offline today | `cache_egraph.go:632-656` |
| 7 | **Engine refuses** | "this call is never cached (do-not-cache)" | recorded outcome | `cache.go:3765`; outcome `do_not_cache` |
| 8 | **Prior attempt failed** | "the previous execution errored; failures are not cached" | prior capture: same digest, failed outcomes only | `cache.go:4193-4210` |
| 9 | **Persisted payload failed to load** | "a cached result was found but its persisted payload could not be loaded" | **terminal fact only (E1)** | `cache_egraph.go:915-933`, `cache.go:4094-4112` |
| — | *Nuances, not categories* | `hit_pending` (recipe cached, first materialization owed) and `joined` (in-flight dedupe) annotate nodes on the path | recorded outcomes | whatif design §3.2 |

**Undetermined form:** with only a single capture and no terminal fact, categories 2/3/4/5/6
collapse to *"no cached result existed under this key; cause not recorded in this capture"* plus
whatever category-1 evidence the call structure itself carries. The design makes that sentence
rarer in three steps (§4), never by inference.

### 3.3 Priced impact

Each frontier origin is priced by the existing what-if simulator: hypothesize the origin's digest
cached, re-simulate, report "this root cause explains N downstream misses costing T wall-clock"
(`wcanalyze` `NewCachedHypothesis`/`ResolveCachedHypothesis`/`RunCachedDetail`, reused as-is —
`cached.go:35-43,:561+`, `cached_report.go:291-315`). The pricing inherits the simulator's answer
contract verbatim: its refusals and `GateErr` propagate into this feature's output (a priced
number never renders when the underlying gate failed — `cached_report.go:42-71,:330-337`). This is
the which/why × how-much join of the two tracks, and it is what ranks the frontier.

## 4. Data plan: have / add-justified / refused

**Have today (both sources unless noted):** `Op.CacheInputs` (ordered input digests);
recipe-digest idents on calls and production; outcomes incl. `hit_pending` + `do_not_cache`;
producer-labeled deferred work; use-markers; result ids (native; per-capture on OTel);
`dag.call` on OTel — the full call AST **including implicit-input names**
(`callpbv1.Call.implicitInputs`; names survive redaction). Caveat (round-1 finding, accepted): it
is recorded in traces but the loader currently discards it and `wcanalyze.Op` carries no call
structure — so category-1 classification on OTel captures is **Chunk-1 loader/graph work** (pure
parsing of recorded data), and native captures need E2. "In the trace" ≠ "available to the
analyzer" until that lands.

**Add, justified (each closes a category the code shows is otherwise unanswerable):**

- **E1 — terminal miss-reason fact** on the call op/span, classified where the engine already
  knows it. Enum (terminal-complete, one per miss, precedence = first terminal reached on the
  decision path of §2): `no_matching_term | input_unknown(k) | expired | session_filtered |
  no_live_candidate | persisted_load_failed | digest_only_miss`. Justification: categories 5/6/9
  are *underivable offline* — candidate collection silently drops expired and session-filtered
  results (`cache_egraph.go:554-559,:577-607,:632-656`) and captures carry no TTL/resource facts;
  `input_unknown(k)` gives the walk an authoritative next hop even in single-capture mode.
  Implementation spec (round-1 gap, accepted): the existing `cache_debug.go:644-660` tracer is
  compile-time disabled and terminal-incomplete — E1 is a REAL capture-schema addition, not a
  tracer toggle: candidate collection counts what it skips instead of dropping silently (small
  lookup change), a miss-reason field on the native op event (`dump.go`/`record.go`) and an
  additive OTel attr, loader/graph preservation, classification at BOTH lookup entries (the
  request path and the digest-only path), and precedence pinned by tests. Volume: one small enum
  (+ one int) on miss calls only. This is old Track-A Option B, argued from the lookup code.
- **E2 — scope-kind fact** on calls whose recipe includes scope inputs (kind: client / session /
  call / schema / from-tag / requested). Justification: makes category 1 authoritative and
  native-complete; today it is derivable only via OTel `dag.call` parsing (named alternative if E2
  is declined — the analyzer then classifies on OTel captures and labels native captures
  "scope not recorded").

- **E3 — cross-source structural-input parity + self identity** (required for the Cloud
  destination's pair mode; round-1 critical finding, accepted): native `CacheInputs` records the
  ordered structural term inputs (module ref included) while OTel `dag.inputs` is a *deduplicated*
  digest list *without* the module ref (`core/telemetry.go:129-136` emitting
  `result_call_frame.go:327-403`, vs native `:839-931`) — the two are NOT the same vector, so
  positional pairing is unsound on OTel captures today. E3 = additive OTel attrs carrying the
  native-parity ordered input vector plus a self-identity tuple (field, type, nth, view), making
  pair mode work on Cloud traces. Until E3 lands, pair mode is **native-first** and the analyzer
  refuses positional pairing on OTel pairs (labels them digest-stable-only) rather than mispairing.

**Refused (with reasons):** retention/eviction event facts (category 2 stays coarse; per Erik,
cross-session non-retention is by-design behavior, not a defect to instrument — revisit only if a
user-facing answer demands finer text); equivalence-fact recording (previously measured as
non-explanatory for this question); any analyzer-side inference in place of either.

## 5. Cross-run diff, folded in

"Cached in run A but not in run B" is the same walk over a **capture pair** (the §7.4 calibration
join machinery reused):

- **Digest-stable nodes** align by digest (their A-side outcome answers directly: hit boundary,
  category 2, or category 8).
- **Changed nodes** pair positionally: under a paired parent, child k of A pairs with child k of B
  when classes match — sound on NATIVE capture pairs, where `CacheInputs` is the ordered
  structural-ref vector the recipe hash consumes (`cache.go:3841-3849` ←
  `result_call_frame.go:839-931`). On OTel pairs this is **unsound today** (deduped, module-less
  `dag.inputs` — see E3) and the analyzer refuses positional pairing there rather than mispairing.
  Class equality is a necessary-not-sufficient guard (round-1 finding accepted: `wcanalyze.Op`
  carries no self digest/AST, so scalar-arg/nth/view changes are invisible to it) — therefore any
  ambiguity (equal classes at multiple unpaired positions, unequal vector lengths without a single
  insertion point) is REFUSED into a "structural change, not pairwise attributable" report line,
  never guessed. E3's self-identity tuple upgrades this. A node that pairs with nothing is
  category 4 (new work) or part of such a structural change, reported as such.
- The walk descends A/B in lockstep from the queried op to the frontier; each origin's category
  text then names the concrete divergence ("input #2 — `Directory.withFile` — content changed").

Single-capture mode remains supported with the §3.2 undetermined form; pair mode is where
categories 2/3/4/8 become decidable. Cloud history generalizes the pair to "recent runs of this
pipeline" without changing the algorithm.

## 6. Surfaces

1. **Offline analyzer first** (this design's implementation): a `why-uncached` mode on both CLIs —
   input: one capture or a pair + a target selector (digest / class / argv, reusing the whatif
   selector machinery); output: ranked frontier, per-origin category + why-text + priced impact +
   the Merkle path. Works on native dumps and Cloud traces (`wccloud.Load`) today.
2. **Cloud destination**: port the converged algorithm — materialize `(digest, inputs, cached,
   outcome, miss-reason)` per op from `otel_traces` (ClickHouse MV), GraphQL
   "traceInvalidation(spanID)" walking the stored DAG, UI rendering the frontier + path.
   Design-level sketch only here; the algorithm must stay the analyzer's, not re-derived in SQL
   (Track A's A→C conclusion, kept). The legacy `Vertex{inputs,cached,history}` product shape is
   precedent, not substrate.

## 7. Relationship to the warm-serving outcome vocabulary (take-3)

The take-3 remote-cache work introduces serving-outcome classification at the lookup/serving
terminals (`hit_live / hit_restored / miss_first / served_from_* / demoted_to_miss`,
`dagql/cache_stats.go` on the take-3 integration branch — per program-coordination description;
file verification pending vm access). The two vocabularies are **complementary axes at the same
terminals**: theirs says *what the serving outcome was*; ours (E1 + the taxonomy) says *why a miss
happened*. Reconciliation requirement (Erik, end-of-program): one terminal classification family —
every `miss_first`/`demoted_to_miss` should be able to carry an E1 reason; neither vocabulary may
fork the other's semantics. This design keeps E1's enum minimal and terminal-anchored specifically
so it can merge into that family.

## 8. Faithfulness and gates

Unchanged doctrine, inherited mechanisms: the analyzer refuses captures the existing gates refuse
(dropped events, suppressed idents, structural violations); every category assignment is a pure
function of recorded data with the deciding datum printed; absence-of-history statements name the
history actually searched; undetermined stays undetermined. No category is ever downgraded to a
guess to make a report prettier.

## 9. Validation catalog (reasoned rows; expected values derived before running)

- **W1** frontier walk on a synthetic three-level miss chain: frontier = the two deepest origins,
  collaterals listed on the path, never as origins.
- **W2** category 1: scoped call (fixture with scope implicit input in `dag.call`) → scoped answer
  with per-scope why-text; digest-pinned `from` fixture → NOT category 1.
- **W3** category 2 vs 4: pair where A executed the digest (2) vs digest absent from A (4).
- **W4** category 3: paired parents, input #k differs → walk descends into k; positional pairing
  asserted against deliberate reordering (classes must match, else structural-change report).
- **W5** categories 5/6: E1 fixtures (expired / session-filtered) → exact answers; same fixtures
  WITHOUT E1 → the undetermined form (never a guessed 5/6).
- **W6** categories 7/8 from outcomes; `hit_pending`/`joined` render as nuances, not origins.
- **W7** priced impact: origin's saving equals the whatif detail run for the same digest.
- **W8** cross-source parity on a dual fixture; Cloud-trace path via `wccloud` end-to-end.
- **W9** refusals: gated capture → the walk refuses; single-capture mode prints the undetermined
  form for a pair-only category.
- **W10** real-workload fixtures: the §7.4 capture pairs — known scoped chains answer category 1,
  known prior-run population answers category 2 (with its mechanism-not-recorded text), the
  from-tag pair's paired prices.
- **W11** duplicate-digest semantics: one digest with executed + hit calls in one capture →
  missed-at-first-demand; all-hit digest → boundary (never walked into).
- **W12** source divergence pinned: the same synthetic run as native and OTel captures — pair mode
  works natively; OTel pair refuses positional pairing pre-E3 with the stated label.
- **W13** structural changes: scalar-arg / nth / view / module-ref change fixtures → pre-E3
  "structural change" refusal (no mispairing); with E3, correct pairs.
- **W14** E1 precedence: fixtures driving each terminal (incl. both lookup entries and
  persisted_load_failed) → exactly one reason each, precedence pinned.
- **W15** mixed-outcome digests across captures (failed-then-executed, do-not-cache-mixed) →
  categories 7/8 decide per the per-digest summary rules, never by single-op sampling.
- **W16** persistence-reset caveat: category 2/4 answer text names the searched history and never
  claims a mechanism (guards the reset/prune/release ambiguity).

## 10. Implementation plan

- **Chunk 1 — the walk + taxonomy core** (`wcanalyze/whymiss.go`): frontier walk over
  `CacheInputs`, single-capture categories (1 via dag.call where present, 7, 8, undetermined),
  ranked output, priced impact via the existing simulator. Rows W1, W2, W6, W7, W9.
- **Chunk 2 — pair mode**: capture-pair join (reuse `cached_calibrate.go` machinery), positional
  pairing, categories 2/3/4. Rows W3, W4, W10, W8.
- **Chunk 3 — E1 emit** (engine): terminal-complete miss-reason fact at the `cache_debug.go` seam,
  both sources, additive-only; analyzer consumption; categories 5/6. Row W5. E2 decision rides
  with it (or the dag.call alternative is promoted, decided at review).
- **Chunk 4 — CLI surface + report** polish; Cloud-destination appendix finalized for handoff.

Per-chunk Codex xhigh review to convergence; commit early/often; integration tests via the
engine-dev-test workflow only.

## 11. Track A's six questions, answered

1. **Surface first:** offline CLI (§6.1), Cloud as destination — unchanged from Track A, now with
   working substrate. 2. **Why-uncached emit:** yes — E1, justified from the lookup code
   (§4); scope-kind E2 proposed with a real recorded-data alternative. 3. **Native parity:** the walk and pair mode are native-FIRST (native has the sound ordered
   inputs); OTel/Cloud gains pair mode via E3 and category 1 via Chunk-1 dag.call parsing —
   per-capture capability stated, never guessed. 4. **Cross-run trigger:** explicit pair first; Cloud
   history generalizes later (§5). 5. **Origin scope:** ranked frontier, priced by the simulator
   (§3.1, §3.3). 6. **Cloud faithfulness bar:** recommend the same refuse-if-unverifiable gate
   with an explicit "incomplete trace" render state — flagged for Erik, does not block chunks 1–3.

## 12. Flagged for Erik (not blocking)

The Cloud faithfulness bar (Q6); E2 vs dag.call-parsing as the category-1 authority; and whether
category 2's answer text should eventually name the lifetime mechanism (would need retention
facts — refused for now per §4).
