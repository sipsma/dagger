# Cache-invalidation tracing: design

Status: draft for adversarial review · 2026-07-07 · verified against the engine at `4bc3d9404`
(every mechanism claim carries its deciding code path; nothing below is asserted unread).

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
5. On miss: in-flight **join** if a same-client execution is running (`cache.go:3888`), else
   **execute**; errored executions publish no result (`cache.go:4193-4210`).

The engine already narrates these terminals to a debug-gated tracer
(`cache_debug.go:644-660`: `traceLookupAttempt` / `traceLookupMissNoMatch` / `traceLookupHit`) —
the right seam exists; it is neither capture-borne nor terminal-complete today (it does not
distinguish expired or session-filtered candidates from plain no-match).

## 3. The answer model

### 3.1 The frontier walk

Given uncached op X in a capture, walk X's recorded cache-input digests (`Op.CacheInputs` — both
sources since `bb7625e2c`; ordered exactly as the recipe hash consumes them) downward: an input
that was a hit is a boundary; an input that missed is walked recursively. The
**miss frontier** = the deepest uncached ops whose own inputs are all hits or leaves. Everything
between X and the frontier missed as *Merkle collateral* (its digest changed because an input's
digest changed) and is shown as the path, not the cause. The output is the **ranked frontier**
(there can be several independent origins), never a single guessed origin.

### 3.2 The root-cause taxonomy

Each frontier origin gets exactly one category. Categories 1–2 are first-class correct-behavior
answers. Every category is a pure function of recorded data; where the data cannot decide, the
answer is the stated *undetermined* form, never a guess.

| # | Category | Answer text (shape) | Decided by | Code anchor |
|---|---|---|---|---|
| 1 | **Deliberately scoped** | "not cached across clients/sessions/calls/schemas, by design, because …" (per-scope why-text; `from` tag vs pinned nuance) | scope implicit-input names in the call structure (OTel `dag.call` today; scope-kind fact once emitted) | `dagql/cache_inputs.go:14-92`; `core/schema/container.go:1015-1046`; `core/schema/modulesource.go:64-65` |
| 2 | **Session-lifetime result** | "computed in a previous session; results of this call live and die with their session, by design" | same digest recorded executed in a prior capture of the pair/history; no scope inputs | retention: session ownership release (`cache.go:712+`, `internal-docs/cache_pruning.md`) |
| 3 | **Input changed** | "input #k changed: <digest A> → <digest B>; walk continues into it" | cross-run pairing (§5): paired parent, input k differs | `missingInputIndex` mirrors this in-engine (`cache_egraph.go:709-717`) |
| 4 | **New work** | "first appearance of this call in the available history" | digest absent from every prior capture in scope | — (absence over the available history, stated as such) |
| 5 | **Expired (TTL)** | "a result existed but its TTL had expired" | **terminal fact only** — underivable offline today | `cache_egraph.go:554-559,:587,:602` |
| 6 | **Session-resource filtered** | "an equivalent result exists but requires session resources this session lacks" | **terminal fact only** — underivable offline today | `cache_egraph.go:632-656` |
| 7 | **Engine refuses** | "this call is never cached (do-not-cache)" | recorded outcome | `cache.go:3765`; outcome `do_not_cache` |
| 8 | **Prior attempt failed** | "the previous execution errored; failures are not cached" | prior capture: same digest, failed outcomes only | `cache.go:4193-4210` |
| — | *Nuances, not categories* | `hit_pending` (recipe cached, first materialization owed) and `joined` (in-flight dedupe) annotate nodes on the path | recorded outcomes | whatif design §3.2 |

**Undetermined form:** with only a single capture and no terminal fact, categories 2/3/4/5/6
collapse to *"no cached result existed under this key; cause not recorded in this capture"* plus
whatever category-1 evidence the call structure itself carries. The design makes that sentence
rarer in three steps (§4), never by inference.

### 3.3 Priced impact

Each frontier origin is priced by the existing what-if simulator: hypothesize the origin's digest
cached, re-simulate, report "this root cause explains N downstream misses costing T wall-clock"
(`wcanalyze` `ResolveCachedHypothesis`/`NewCachedSimulation`, reused as-is). This is the
which/why × how-much join of the two tracks, and it is what ranks the frontier.

## 4. Data plan: have / add-justified / refused

**Have today (both sources unless noted):** `Op.CacheInputs` (ordered input digests);
recipe-digest idents on calls and production; outcomes incl. `hit_pending` + `do_not_cache`;
producer-labeled deferred work; use-markers; result ids (native; per-capture on OTel);
`dag.call` on OTel — the full call AST **including implicit-input names**, i.e. category-1
classification needs no new emit on the OTel path (verified: names survive redaction;
`callpbv1.Call.implicitInputs`).

**Add, justified (each closes a category the code shows is otherwise unanswerable):**

- **E1 — terminal miss-reason fact** on the call op/span, classified where the engine already
  knows it (the `cache_debug.go` seam, made capture-borne and terminal-complete):
  `never_indexed | input_unknown(k) | expired | session_filtered`. Justification: categories 5–6
  are *underivable offline* — candidate collection silently drops expired and session-filtered
  results, so no offline walk can ever produce them; and `input_unknown(k)` gives the walk an
  authoritative next hop even in single-capture mode. Volume: one small enum (+ one int) on miss
  calls only. This is old Track-A Option B, now argued from the lookup code itself.
- **E2 — scope-kind fact** on calls whose recipe includes scope inputs (kind: client / session /
  call / schema / from-tag / requested). Justification: makes category 1 authoritative and
  native-complete; today it is derivable only via OTel `dag.call` parsing (named alternative if E2
  is declined — the analyzer then classifies on OTel captures and labels native captures
  "scope not recorded").

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
  when classes match (`CacheInputs` vectors are ordered exactly as the recipe hash consumes the
  structural refs — `cache.go:3841-3850`, `result_call_frame.go` hash body). Pairing is pure
  recorded data; a node that pairs with nothing is category 4 (new work) or a structural change,
  reported as such. On OTel captures, `dag.call` self-identity (field/args/view/module)
  additionally confirms pairs.
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
  known session-lifetime population answers category 2, with the from-tag pair's paired prices.

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
   (§4); scope-kind E2 proposed with a real recorded-data alternative. 3. **Native parity:**
   yes for the walk (CacheInputs is native now); category 1 needs E2 or stays OTel-first —
   stated per-capture, never guessed. 4. **Cross-run trigger:** explicit pair first; Cloud
   history generalizes later (§5). 5. **Origin scope:** ranked frontier, priced by the simulator
   (§3.1, §3.3). 6. **Cloud faithfulness bar:** recommend the same refuse-if-unverifiable gate
   with an explicit "incomplete trace" render state — flagged for Erik, does not block chunks 1–3.

## 12. Flagged for Erik (not blocking)

The Cloud faithfulness bar (Q6); E2 vs dag.call-parsing as the category-1 authority; and whether
category 2's answer text should eventually name the lifetime mechanism (would need retention
facts — refused for now per §4).
