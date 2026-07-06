# Cache-invalidation tracing: design

Status: revision 4 after adversarial review rounds 1-3 (verdicts: reject x3, findings accepted
each round) · 2026-07-06 · mechanism claims verified against the code at `4bc3d9404` (the commit this doc's
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
   key is running (`callKey + ConcurrencyKey`, default per-SESSION (`objects.go:604-608`) — `cache.go:1348-1350,
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

Given uncached op X in a capture, walk X's recorded cache-input digests (`Op.CacheInputs`)
downward: an input that was a hit is a boundary; an input that missed is walked recursively.
Source fidelity (round-2 correction): NATIVE CacheInputs is the ordered structural-ref vector the
recipe hash consumes, module ref included (`cache.go:3841-3849` ← `result_call_frame.go:839-931`);
OTel `dag.inputs` is a DEDUPLICATED, module-less digest list (`core/telemetry.go:129-136` ←
`result_call_frame.go:327-403`) — so on OTel captures, pre-E3, even the WALK is partial (round-3
critical, accepted): module-ref edges are absent, and a module-caused miss cannot be walked to its
true frontier. OTel single-capture walks therefore carry an explicit "module edges not recorded;
frontier may be shallow for module-provided calls" caveat, upgraded to a per-node refusal once
Chunk 4's `dag.call` parsing can detect module-bearing calls. Positional pairing needs more still
(§5, E3). The **miss frontier** = the deepest uncached digests whose own inputs are all
hits or leaves. The walk operates on **digest nodes**, not op instances, with a TIME-AWARE status
(round-2 correction — mixes exist in both directions): a digest's status is its FIRST recorded
call's outcome in demand order (StartNS, ties broken by op id — deterministic, pure recorded
data): *missed-at-first-demand* (first call executed/joined) or *cached-at-first-demand* (first
call hit or hit_pending — the recipe was cached; pending is a nuance). Source caveat (round-3,
accepted): OTel deliberately suppresses repeated same-digest call spans
(seen-key suppression, `dagql/telemetry.go:48-63`), so on OTel captures the per-digest evidence is
first-emission-only — the status is computed from what is recorded and labeled as such; native
captures are demand-complete (every caller records a call op). A digest whose later calls REVERSE the first status (hit then
executed, or vice versa) is *context-dependent within the run* — reported as such and walked as a
miss. E1 upgrades SOME reversals to exact causes (expired, session_filtered); reversals caused by
in-run release/collection (`cache.go:713-752`, `:918-945`) surface to E1 only as
no-candidate-remaining, so their answer text stays mechanism-unrecorded, like category 2
(round-3 correction — "precisely classifiable post-E1" was too strong). Everything
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
| 9 | **Hit unusable: persisted payload failed to load** | "a cached result was found but its persisted payload could not be loaded" | **terminal fact only (E1; the hit-unusable arm, not a miss reason)** | `cache_egraph.go:915-933`, `cache.go:4094-4112` |
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

**Have today (both sources unless noted):** `Op.CacheInputs` (native: the ordered structural-ref
vector; OTel: an ordered deduplicated module-less edge list — see §3.1);
recipe-digest idents on calls and production; outcomes incl. `hit_pending` + `do_not_cache`;
producer-labeled deferred work; use-markers; result ids (native; per-capture on OTel);
`dag.call` on OTel — the full call AST **including implicit-input names**
(`callpbv1.Call.implicitInputs`; names survive redaction). Caveat (round-1 finding, accepted): it
is recorded in traces but the loader currently discards it and `wcanalyze.Op` carries no call
structure — so category-1 classification on OTel captures is **Chunk-1 loader/graph work** (pure
parsing of recorded data), and native captures need E2. "In the trace" ≠ "available to the
analyzer" until that lands.

**Add, justified (each closes a category the code shows is otherwise unanswerable):**

- **E1 — terminal lookup-outcome fact** on the call op/span, classified where the engine already
  knows it. Emitted only by calls that PERFORMED a lookup and did not return a usable hit
  (round-3 scope correction: request errors and the recursive guard never reach lookup and are not
  cache explanations; `do_not_cache` never looks up and is already outcome-recorded). Enum
  (one per such call, precedence = first terminal reached on the §2 decision path):
  `no_matching_term | input_unknown(k) | expired | session_filtered | no_live_candidate |
  persisted_load_failed` — the lookup ENTRY (`request | digest_only`) is separate metadata, not an
  enum value (round-3 contradiction fixed). Justification: categories 5/6/9
  are *underivable offline* — candidate collection silently drops expired and session-filtered
  results (`cache_egraph.go:554-559,:577-607,:632-656`) and captures carry no TTL/resource facts;
  `input_unknown(k)` gives the walk an authoritative next hop even in single-capture mode.
  Round-2 refinements (accepted): E1 is a LOOKUP-OUTCOME fact, not "miss calls only" — for
  lookup-performing calls it is emitted on a miss or hit-unusable outcome, covering the miss arms AND the hit-unusable case
  (`persisted_load_failed` fires on a SELECTED hit whose payload load then fails,
  `cache_egraph.go:915-933`); the lookup ENTRY (request path vs digest-only path) is an orthogonal
  flag, not an enum value — specific reasons still apply on digest-only entries.
  Implementation spec (round-1 gap, accepted): the existing `cache_debug.go:644-660` tracer is
  compile-time disabled and terminal-incomplete — E1 is a REAL capture-schema addition, not a
  tracer toggle: candidate collection counts what it skips instead of dropping silently (small
  lookup change), a reason field on the native op event (`dump.go`/`record.go`) and an additive
  OTel attr, loader/graph preservation, both lookup entries, precedence = first terminal reached,
  pinned by tests. Companion micro-emit (round-2 critical finding; round-3 safety constraint): native
  `do_not_cache` calls return before `SetIdent` (`cache.go:3765` vs `:3840`), so category 7 is not
  digest-addressable natively — when profiling is enabled, derive+set the ident on that path
  BEST-EFFORT ONLY: derivation errors are swallowed for execution (that path currently has no such
  failure mode and must not gain one) and counted via the existing suppression-counter pattern;
  category 7 stays class-level for that call when the ident is absent; zero cost when profiling is
  off. Volume: one small enum + entry-flag metadata + optional int, lookup-performing non-hit
  calls only.
- **E2 — scope-kind fact** on calls whose recipe includes scope inputs (kind: client / session /
  call / schema / from-tag / requested). Justification: makes category 1 authoritative and
  native-complete; today it is derivable only via OTel `dag.call` parsing (named alternative if E2
  is declined — the analyzer then classifies on OTel captures and labels native captures
  "scope not recorded").

- **E3 — cross-source pair-mode completion** (round-1 critical finding; re-specified after
  round 2 refuted the tuple form): each source has HALF of what pair mode needs. Native has the
  sound ordered input vector but no call structure (class+digest only); OTel has the full call
  structure — `dag.call` carries arg names, literal shapes, implicit inputs
  (`callpbv1.Call`, incl. everything the self digest consumes, `result_call_frame.go:868-900,
  :1315-1381`) — but a deduplicated module-less input list. So: **E3a** = additive OTel attr
  carrying the native-parity ordered input vector (small; enables positional pairing on Cloud
  traces); **E3b (loader work, no emit)** = parse `dag.call` into canonical self structure on the
  analyzer's op, giving OTel pairs arg-level change attribution ("scalar arg 'platform' differed")
  — a tuple is NOT sufficient (round-2 finding accepted). Native pair mode reports changes at
  digest granularity with the label "arg-level detail available on OTel captures" — a full native
  call-structure emit is REFUSED for now on volume grounds, stated. Until E3a lands, positional
  pairing on OTel pairs is refused (digest-stable analysis only) rather than mispaired; ambiguity
  contract in §5.

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
  ambiguity is REFUSED into a "structural change, not pairwise attributable" report line, never
  guessed. The pairing contract, precisely (round-2 gap; tightened after round 3's duplicate-class
  counterexample): FIRST anchor by digest equality — an order-preserving longest-common-subsequence
  over the two digest vectors (deterministic; digests are exact) — then, between consecutive
  anchors, pair the leftover positions only when they match one-to-one by class in order on both
  sides (equal leftover counts, classes agreeing pairwise); any other shape — unequal leftovers,
  duplicate classes that admit more than one order-preserving matching, crossing anchors — is the
  refusal line. The digest LCS itself must be UNIQUE at occurrence level: repeated equal digests
  that admit multiple maximal anchor sets refuse, ALWAYS (the evidence cannot say which duplicate
  was removed). [AMENDED at Chunk 2, coordinator-ratified: the ratified text carved out an
  exception — "except when the ambiguous interval is byte-identical on both sides and yields no
  report either way" — which is provably VACUOUS: were every position anchored, the embedding
  would be forced (unique), so any ambiguity leaves an unanchored position, and an unanchored
  position always yields a report whose content or attribution depends on the occurrence choice.
  The implemented contract refuses on ambiguity unconditionally; this text is amended to match
  the implementation, not vice versa.] Digest anchoring first is what defeats the greedy-prefix mispair (A=[C:d1, C:d2,
  D:d3] vs B=[C:d2, D:d3]: d2 and d3 anchor, d1 is exposed as the deletion). E3b upgrades
  attribution within pairs; it does not loosen the refusal contract. A node
  that pairs with nothing is category 4 (new work) or part of such a structural change, reported
  as such.
- The walk descends A/B in lockstep from the queried op to the frontier; each origin's category
  text then names the concrete divergence ("input #2 — `Directory.withFile` — content changed").

Single-capture mode remains supported with the §3.2 undetermined form; pair mode is where
categories 2/3/4/8 become decidable. Cloud history generalizes the pair to "recent runs of this
pipeline" without changing the algorithm.

## 6. Surfaces

1. **Offline analyzer first** (this design's implementation): a `why-uncached` mode on both CLIs —
   input: one capture or a pair + a target selector (digest / class / argv, reusing the whatif
   selector machinery); output: ranked frontier, per-origin category + why-text + priced impact +
   the Merkle path. Works on native dumps today; Cloud traces (`wccloud.Load`) support
   single-capture analysis with the §3.1 OTel caveats (module edges absent pre-E3 — global caveat,
   per-node refusal after Chunk 4), and pair/module-complete walks require E3a/E3b; category 1 on
   OTel after Chunk-1 dag.call implicit-input parsing, on native after E2.
2. **Cloud destination**: port the converged algorithm — materialize `(digest, inputs, cached,
   outcome, miss-reason)` per op from `otel_traces` (ClickHouse MV), GraphQL
   "traceInvalidation(spanID)" walking the stored DAG, UI rendering the frontier + path.
   Design-level sketch only here; the algorithm must stay the analyzer's, not re-derived in SQL
   (Track A's A→C conclusion, kept). The legacy `Vertex{inputs,cached,history}` product shape is
   precedent, not substrate.

## 7. Relationship to the warm-serving outcome vocabulary (take-3)

The take-3 remote-cache work introduces serving-outcome classification at the lookup/serving
terminals — VERIFIED against `dagql/cache_stats.go` on the take-3 integration worktree
(`remote-cache-take3-fork-0bdd5926`, HEAD `2924af65ce`): `hit_live | hit_restored | miss_first |
served_from_snapshot | served_from_lazy_form | demoted_to_miss`, classified exactly once at the
terminals (`classifyServeOutcome`), counted per (outcome, field), persisted as a shutdown stats
file. The two vocabularies are **complementary axes at the same terminals**: theirs says *what the
serving outcome was*; ours (E1 + the taxonomy) says *why a miss happened*. Two verified
convergence points: their own comment states a warm engine "legitimately first-misses
session-scoped calls" — the same expected-miss framing this design's categories 1–2 encode; and
their `demoted_to_miss` ("a hit's retained sources were exhausted; the call executed live") is the
serving-side sibling of this design's hit-unusable arm — E1's reasons are the natural "why"
companions to their `miss_first`/`demoted_to_miss`. Reconciliation requirement (Erik,
end-of-program): one terminal classification family; neither vocabulary may fork the other's
semantics. E1's enum stays minimal and terminal-anchored specifically so it can merge into that
family.

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
- **W8** Cloud-trace path via `wccloud` end-to-end (single-capture pre-E3a; pair mode post-E3a on
  a dual fixture).
- **W9** refusals: gated capture → the walk refuses; single-capture mode prints the undetermined
  form for a pair-only category.
- **W10** real-workload fixtures: the §7.4 capture pairs — known scoped chains answer category 1,
  known prior-run population answers category 2 (with its mechanism-not-recorded text), the
  from-tag pair's paired prices.
- **W11** digest-node semantics: first-demand ordering decides (executed-then-hit →
  missed-at-first-demand; hit-then-executed → context-dependent, reported, walked as miss);
  all-hit and all-hit_pending digests → boundaries (pending rendered as nuance).
- **W12** source divergence pinned: the same synthetic run as native and OTel captures — pair mode
  works natively; OTel pair refuses positional pairing pre-E3 with the stated label.
- **W13** structural changes: scalar-arg / nth / view / module-ref change fixtures → native
  digest-granularity change reports; OTel pre-E3a refusal; post-E3a/E3b correct pairs WITH
  arg-level attribution (scalar change named from dag.call structure, not guessed).
- **W14** E1 precedence: fixtures driving each terminal (incl. both lookup entries and
  persisted_load_failed) → exactly one reason each, precedence pinned.
- **W15** mixed-outcome digests across captures (failed-then-executed, do-not-cache-mixed) →
  categories 7/8 decide per the per-digest summary rules, never by single-op sampling.
- **W16b** duplicate-class pairing counterexample pinned: A=[C:d1,C:d2,D:d3] vs B=[C:d2,D:d3] →
  d1 reported as deletion, never paired to d2 (digest anchors win).
- **W16c** OTel module-blind walk: module-caused miss fixture → native walk reaches the true
  frontier; OTel walk pre-E3 stops shallow AND prints the module-edge caveat (post-Chunk-4:
  per-node refusal).
- **W16** persistence-reset caveat: category 2/4 answer text names the searched history and never
  claims a mechanism (guards the reset/prune/release ambiguity).

## 10. Implementation plan

- **Chunk 1 — the walk + taxonomy core** (`wcanalyze/whymiss.go`): frontier walk over
  `CacheInputs`, single-capture categories (1 via dag.call where present, 7, 8, undetermined),
  ranked output, priced impact via the existing simulator. Rows W1, W2, W6, W7, W9.
- **Chunk 2 — pair mode (native)**: capture-pair join (reuse `cached_calibrate.go` machinery),
  positional pairing with the §5 contract, categories 2/3/4. Rows W3, W4, W10.
- **Chunk 3 — E1 emit** (engine): terminal-complete lookup-outcome fact, both sources,
  additive-only, both entries + the do-not-cache ident micro-emit; analyzer consumption;
  categories 5/6/9. Rows W5, W14. E2 decision rides with it.
- **Chunk 4 — E3a + full dag.call structure (OTel pair mode)**: the ordered-input parity attr,
  loader preservation of CANONICAL SELF STRUCTURE (E3b — full args/literals), OTel positional
  pairing unlocked, arg-level change attribution, module-bearing per-node refusal for the walk.
  Rows W8, W12, W13, W16c. (Ownership split, explicit: Chunk 1 parses ONLY the implicit-input
  NAMES from dag.call — enough for category 1; Chunk 4 parses the full structure.)
- **Chunk 5 — CLI surface + report** polish; Cloud-destination appendix finalized for handoff.

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

The Cloud faithfulness bar (Q6); and whether category 2's answer text should eventually name
the lifetime mechanism (would need retention facts — refused for now per §4). RESOLVED since
this list was written: E2 vs dag.call-parsing as the category-1 authority — E2 was ADOPTED on
W10's evidence at Chunk 3 (ruling and reasoning in §13), with dag.call parsing remaining the
OTel path.

## 13. As-built validation notes (implementation record)

**W10 (real-workload §7.4 pairs, run 2026-07-06 at Chunk 2):** on the real cold/warm capture
pairs (`/tmp/whatif-cal3-{module,withexec}/`), warm as the query side against cold as reference:
(a) the known prior-run population answers category 2 exactly — e.g. `Container.withoutEnvVariable
xxh3:f536b280c02268fe` (executed in both captures): *"computed in a previous run … which one
applied here is not recorded. History searched: the one paired reference capture"*; (b) the known
scoped chains (`Query.moduleSource` cold `xxh3:8107b9e7f62d4dac` → warm `xxh3:410bda4bc8c95210`)
pair via the unique same-class root partner and answer category 3 naming *"scope input values"*
among the self-change candidates with the *"scope structure not recorded (native)"* label — the
category-1 half of W10 was E2-gated on native captures per §6.1's own data-availability
statement, and **COMPLETED after Chunk 3's E2 adoption**: fresh native cold/warm captures from
an E2-emitting engine build (2026-07-06, module workload `hello@v0.3.0`, captures in
`/tmp/invtrace-w10/`) answer the warm scoped chain (`Query.moduleSource
xxh3:308c3f7996cacd4a`) with category 1 — *"scoped per client … (dagql.PerClientInput)"*, scope
evidence `cachePerClient` — natively; the same captures carry live E1 facts (`request
no_matching_term`, `request input_unknown 1`) and zero DNC-ident suppressions; (c) the from-tag
pair answers with its paired
counterpart and paired price (warm `Container.from xxh3:b677e73b409dfa9b` ← cold
`xxh3:e442fcb8babb1931`, save@pull=0 = 670.6ms) — NOTE the cold capture holds TWO from digests
(the digest-pinned form `xxh3:bd838c8a8919c204`, stable and hit warm; the tag-scoped form,
re-minted warm), and the walk's lineage counterpart (tag-scoped) is consistent with the
calibration's independent result-id pairing (both forms share rid 6801).

**Gate-rendering fix (manager directive at Chunk 5, coordinator-approved; the pre-existing
whatif behavior flagged during Chunk-1 review):** the what-if-cached RANKING, DETAIL, and
CALIBRATION sections now REFUSE rendering when the OTel structural gate failed (previously they
rendered after the gate verdict printed) — a counterfactual over data a gate already declared
unfaithful is decoration. Same rule the why-uncached walk has had since Chunk 1; the general
report still renders with warnings. `ReportOptions.RefuseCachedSections` carries the CLI-level
verdict; pinned by `TestReportRefusesCachedRankingOnGateFailure`.

**W10 evidence regeneration recipe (durability; the /tmp captures are not the row's only
home):** the §7.4 pairs (`/tmp/whatif-cal3-{module,withexec}/`) and the W10-completion pair
(`/tmp/invtrace-w10/`) regenerate as follows. (1) Private OUTER engine (two agents' CLIs GC each
other's auto-provisioned engines): `docker run -d --name dagger-outer.<tag> --privileged -v
<vol>:/var/lib/dagger registry.dagger.io/engine:v0.21.7`. (2) Build engine+CLI from the branch:
`PATH=$HOME/bin:$PATH _EXPERIMENTAL_DAGGER_RUNNER_HOST=docker-container://dagger-outer.<tag>
_EXPERIMENTAL_DAGGER_DEV_CONTAINER=dagger-engine.<tag>
_EXPERIMENTAL_DAGGER_DEV_IMAGE=localhost/dagger-engine.<tag> ./hack/build` (the final start step
fails on port 6060 — expected; host 6060 is taken). (3) Cold start manually:
`docker rm -f dagger-engine.<tag>; docker volume rm dagger-engine.<tag>; docker run -d --name
dagger-engine.<tag> -p <port>:6060 --privileged -v dagger-engine.<tag>:/var/lib/dagger
localhost/dagger-engine.<tag> --extra-debug --debugaddr=0.0.0.0:6060`. (4) Enable profiling:
`curl -X POST -d on http://172.17.0.1:<port>/debug/wcprof/enabled` (from inside the tailcall
container use gateway 172.17.0.1, never localhost). (5) Run the workload twice from an empty
dir with `_EXPERIMENTAL_DAGGER_RUNNER_HOST=docker-container://dagger-engine.<tag>
<branch>/bin/dagger …` — module workload: `-m github.com/shykes/daggerverse/hello@v0.3.0
functions` (any module exercises the scoped chains); the §7.4 originals used a dagger-repo
module build and a withExec-heavy pipeline (hack/wcprof-cached-calibrate documents that flow).
(6) Capture `curl http://172.17.0.1:<port>/debug/wcprof/dump?flush=true` after EACH run
(cold.wcprof, warm.wcprof). Analyze: `go run ./cmd/wcprof-analyze -why-uncached-class
'Query.moduleSource' -why-uncached-vs cold.wcprof warm.wcprof`.

**§5 refusal-family → pinned-test mapping (Chunk 2/4 as-built):** duplicates at anchors, d1
never paired to d2 → `TestWhyMissW16bDigestAnchorsBeatClassPairing`; crossing anchor candidates
(reordering never pairs across an anchor) → `TestWhyMissW4InputChanged` (reordering variant);
multiple maximal anchor sets (occurrence-uniqueness refusal) →
`TestLCSAnchorsOccurrenceUniqueness` ([X,X]×[X], two-distinct-max-strings) +
`TestWhyMissPairAmbiguityRefusal` (end-to-end); unequal leftovers, one side EMPTY
(deletion/addition reports, per this section's amended reading and W16b's own row text) →
`TestWhyMissW16bDigestAnchorsBeatClassPairing` + `TestWhyMissW4InputChanged`; unequal leftovers,
BOTH non-empty (the refusal line) → `TestWhyMissPairUnequalNonEmptyLeftoversRefuse` (added when
this mapping exposed it unpinned); pairwise class mismatch → `TestWhyMissW4InputChanged`
(class-mismatch variant); duplicate classes with equal counts (the complete in-order matching is
positional and unique — any other complete matching crosses; argued in `pairInputVectors`'s
contract comment) with the same-digest-duplicate consequence voided →
`TestWhyMissPairConflictVoids` + `TestWhyMissPairConflictAcrossParentsRestarts`; the
now-vacuous byte-identical case (full anchoring forces uniqueness) →
`TestLCSAnchorsOccurrenceUniqueness` ([X,X]×[X,X] anchors fully, unique).

**E2 ratification record:** E2's adoption was ratified by the MANAGER on the W10 evidence and
independently ratified by the COORDINATOR ("the delegated standard was met in its strongest
form"); it remains flagged to Erik (§12), who may re-litigate. The E3a
profiling-source-activation note above is likewise recorded for Erik: one line flips it to
always-on (dag.inputs precedent) if he wants unprofiled Cloud traces to carry ordered vectors.

**ResultIDsCaptureLocal blast radius (coordinator requirement, verified from the docs' own
provenance statements, not memory):** every committed calibration and validation number — the
§7.4/V-row results in `whatif-cached-calibration.md` (its own provenance: "isolated
container/port; native wcprof dumps", debug port 6062) and this doc's W10 results (native dumps
in `/tmp/whatif-cal3-*` and `/tmp/invtrace-w10`, provenance stated above) — derives from LOCAL
NATIVE captures. No committed evidence was ever derived from Cloud-trace pairs: **no committed
evidence affected; the exposure was latent for Cloud pairs only** (and is closed by the Chunk-4
fix that marks wccloud-loaded graphs ResultIDsCaptureLocal).

**Pair-walk refinement (review round, Chunk 2):** in pair mode a DIGEST-STABLE missed input does

not make its changed parent Merkle collateral — a stable digest is an unchanged input ref, so it
cannot have changed the parent's key; both nodes are independent origins (the stable one answers
category 2/8, the changed parent reports its own divergence). The single-capture collateral rule
is unchanged. Deepest-changed-node answers name the concrete divergence the pairing found
(removed/added/changed inputs vs a true self change), never a blanket "the call changed".

**E1 seam-check against the take-3 as-built tip (Chunk 3, coordinator-required; read via the vm
worktree-holder from `remote-cache-take3-fork-0bdd5926` at HEAD `0a42c6b8fd`):** §2's anchors
re-verified. [Dual citation, per the coordinator: take-3's integration tip subsequently moved
`0a42c6b8fd` → `df3e1d6b39` (chunk A: service bundle import + origin identity, schema 19); the
coordinator reviewed that diff first-hand and attests it TERMINAL-NEUTRAL (import/persistence
work, not the serve path) — so this drift table remains verified against `0a42c6b8fd` and holds
at `df3e1d6b39` on the coordinator's first-hand review.] No drift in the lookup core: `lookupCacheForRequestLocked` and its helpers are
byte-stable at the same lines (`cache_egraph.go:805-861`, `:554-559`, `:646-656`, `:683-734`).
Line-only drift: the DoNotCache pre-lookup return moved `:3765→:3912`; `lookupCacheForDigests`
moved to `:4113-4186`; the no-publish error terminal for waiters sits at `cache.go:4275-4284`.
SEMANTIC drift, and the reconciliation E1 adopts: (1) take-3 classifies serve outcomes at exactly
three sites — the hit return (`hit_live`/`hit_restored`, cache.go:3998), the EXECUTOR-path miss
after the join check (`miss_first`, :4036 — joiners classify nothing), and `releaseFailedHit`
(`demoted_to_miss`, :4212); E1's classification points are positioned to coincide with that
family (one seam): the request-entry reason is derived inside the same locked lookup whose
terminals take-3 classifies, and the hit-unusable arm fires exactly where `releaseFailedHit`
consumes source exhaustion. (2) The design's `persisted_load_failed` terminal is SPLIT as-built:
`errSourcesExhausted` → `demoted_to_miss` (consumed; the call executes live), any other load
failure → a propagated request error (`ifNotDemoted`). On this branch (which predates take-3) the
load failure propagates as an error and E1 records `persisted_load_failed` before the return; at
merge time that emit belongs inside the demote seam and `persisted_load_failed` becomes the "why"
companion of `demoted_to_miss` — same family, no fork. (3) The digest-only entry classifies no
serve outcome in take-3; E1 extends the family there (link-borne facts) rather than forking it.

**E3a activation model (Chunk 4):** the ordered-input attr is gated on the wcprof OTel source
being active (the same activation as the wait-link and forced-fact emits), so ordinary
UNPROFILED Cloud traces do not carry it — positional pairing on Cloud traces activates for
profiled runs. dag.call-derived evidence (category 1, E3b structures, the module-bearing
detection) is on ALL traces, profiled or not. If Erik wants E3a always-on (the dag.inputs
precedent), it is a one-line gate change in `stampOTelOrderedInputs`.

**E2 ruling (Chunk 3, decided on evidence per the coordinator's default):** E2 is ADOPTED. The
deciding evidence is W10's native run: the flagship scoped chains — the feature's binding
first-class answer — could only reply "scope structure not recorded" on native captures, and the
offline surface is native-FIRST (§11.3). The emit is one interned JSON per profiled call at the
existing SetIdent seam (names + empty-value flags, never values), recorded for EVERY profiled
call so the absence ("[]") is authoritative — the refinement over E2's minimal form that makes
the undetermined form's "carries no scope inputs" an engine statement rather than a guess. The
dag.call-parsing alternative remains the OTel path (Chunk 1); both encode the identical JSON.
Companion counters: do-not-cache ident derivation failures are a separate counted caveat
(`SuppressedDoNotCacheIdents`), never a capture refusal — they degrade one call's addressability,
not demand evidence.

## 14. Cloud-destination appendix (Chunk 5 — the handoff)

The offline analyzer is the reference implementation; the Cloud port carries the ALGORITHM, not
a re-derivation (§6.2's binding rule: the walk must not be re-derived in SQL). What Cloud needs,
in dependency order:

**14.1 The per-op tuple to materialize** (ClickHouse MV over `otel_traces`, one row per call
span): `(trace_id, span_id, dag.digest, outcome, wcprof.call.outcome, wcprof.lookup.outcome,
dag.inputs, wcprof.inputs.ordered, dag.call-derived: {implicit-input names+emptiness,
module-bearing bit, canonical self structure}, dagger.io/dag.cached, start, end, op id order)`.
Everything is a recorded attribute today except the dag.call derivations, which are a PARSE of a
recorded attribute (the loader's `decodeDagCall` is the reference: one base64-proto decode
yielding scope inputs, the CallSelf rendering, and the module bit). The digest-only lookup facts
ride as span LINKS (`link.purpose=lookup_outcome` with `wcprof.lookup.digest` +
`wcprof.lookup.outcome`) and materialize into a side table keyed by digest.

**14.2 The algorithm surface to port** (all in `engine/wcprof/wcanalyze/whymiss*.go`, each with
its validation rows): (a) digest-node construction with first-demand status (StartNS, op-id
tie-break) and the within-run annotations (context-dependent, re-executed, failed-before-
re-demand — the EXACT per-digest summary predicates, W11/W15); (b) the frontier walk with the
single-capture collateral rule and the pair-mode refinement (stable inputs never collateralize a
changed parent, §13); (c) the §5 pairing contract: occurrence-unique LCS (embedding-count DP,
saturating at 2 — ambiguity ALWAYS refuses; the byte-identical exception is provably vacuous,
argued in-code), one-to-one in-order class matching between anchors, empty-side gaps as
deletions/additions, the per-pairing E3a ordered-vector soundness gate, and the poison-set
restart-to-fixpoint for pairing conflicts (the final report derives nothing from a voided
pairing); (d) classification precedence — the EXACT switch order in `classifyOrigin`: 7 from THIS
capture's outcomes (any-dnc per-digest rule) > 8 within-capture (failed-before-re-demand) >
E1-exact (5/6/9 from recorded terminal facts) > stable-reference (inside which: 7 when the
reference records dnc for the digest > 8 failed-only > 2, else the tally-labeled undetermined) >
1-scoped > 3-paired > 4-absent > undetermined — with every answer carrying its deciding datum
and the searched-history statement; (e) E3b arg-level attribution (`diffCallSelf`) with the
identical-rendering label.

**14.3 Pricing:** the offline analyzer prices origins with the what-if-cached replay (W7:
equality with the detail run). Cloud has no replay; the honest Cloud v1 renders the walk,
categories, and answer texts WITHOUT priced impact (or with the origin's recorded producing
wall-clock labeled as such — a recorded quantity, not a counterfactual). Porting the replay is a
separate, later decision; a schedule-free "savings" number would violate the §3.3 contract.

**14.4 History generalization:** pair mode's reference capture generalizes to "recent runs of
this pipeline" WITHOUT algorithm changes (§5): stable/absent checks become an indexed digest
lookup over the history set; the reference side of positional pairing uses the most recent run
containing the class-matched counterpart; every absence statement names the history actually
searched (run ids), per row W16.

**14.5 Faithfulness bar (Q6, flagged for Erik in §12):** the recommendation stands — the same
refuse-if-unverifiable gates: the completeness checksum (declared vs received span counts), the
dropped-events/suppressed-idents admission rule, per-node refusals (module-blind, unordered
pairing, corrupt dag.call as CORRUPT not absent), and an explicit "incomplete trace" render
state instead of a silently partial answer.

**14.6 Activation model:** on ALL existing Cloud traces: the walk (module-blind caveats and
per-node refusals), categories 1/7 via dag.call + outcomes, pair-mode digest-stable answers
(2/8), and the E3b STRUCTURES (parsed and rendered on every dag.call-bearing span). E3b
ATTRIBUTION, however, only fires from the category-3 paired branch — it needs a positionally
paired origin, which needs ordered vectors on both input-bearing sides — so on unprofiled
traces it reaches only zero-input root pairs; arg-level attribution in general activates with
E3a (profiled runs). On profiled runs additionally: E1 exact causes (5/6/9, input_unknown next
hops), E3a positional pairing, the digest-only lookup facts. Native dumps additionally carry E2
scope facts and the DNC ident micro-emit. The take-3 merge folds E1's `persisted_load_failed`
into the `demoted_to_miss` seam (§13's drift table).
