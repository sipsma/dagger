# Recipe-digest instability across cold/warm runs — investigation report

Cache Performance Analysis · Track B (what-if-cached) · investigator report to
whatif-cached-impl · 2026-07-05

**Scope.** Why do the same logical calls carry different recipe digests across
two back-to-back runs against one engine, why did one case unify its result
object and another not, and — the primary question — how will we *know* which
explanation is right. Basis: the fresh V34 captures in `/tmp/whatif-cal2/` and
the older pre-Chunk-4 captures in `/tmp/whatif-cal/`, plus the engine source at
HEAD (`7b1fb1f35`).

**Rule of this report (Erik's, after the equivalence-machinery incident).**
Every causal claim below is either **VERIFIED** — with the exact
command / probe / `file:line` shown — or labelled **HYPOTHESIS** with its
discriminating test named in the same sentence. Nothing confident-but-unchecked.

**The one-line answer, up front (VERIFIED).** The digest instability is not an
accident, a timestamp, or a leaked registry pin. It is the engine deliberately
mixing a per-session / per-client value into the cache key of exactly these
calls, so that a mutable resolution (a tag → digest, a local path → content) is
reused *within* a session but never *across* sessions. Two CLI invocations are
two sessions, so these recipes differ by construction and re-execute every run.
No recorded fact — equivalence or otherwise — can turn that into a cross-run
hit, because the engine is choosing not to hit. The old "warm hit through the
equivalence machinery" story is refuted: zero cross-run equivalence hits exist
in either the new *or* the old captures.

---

## Part 1 — The discrimination plan (PRIMARY)

Ordered by decisiveness per unit cost. Checks 1–8 are **read-only over the
existing captures or source** and I have already run them (results in Part 3);
they are listed here so the plan shows what each one *decides*. Checks 9–12
need a fresh engine run or a rebuild and are proposed, not taken. "Cost" is
analyst effort + machine risk, not wall-clock.

| # | Check | What result decides which hypothesis | Cost | Status |
|---|---|---|---|---|
| 1 | **Recursive CacheInputs differ.** For an unstable call, diff its two `Op.CacheInputs` vectors; follow the first differing input to its producing call in each trace; recurse to the leaf. | Leaf **at a propagated input** ⇒ instability flows up from below (H-A1 input-provenance). Leaf **at the call's own self-digest** (inputs equal / no inputs, idents differ) ⇒ instability is in *this call's* own args/implicit-inputs (H-A2 self-digest). | Cheap (data on disk) | **DONE → self-digest, both cases** (Part 3, §C) |
| 2 | **Read the resolver + its cache-key scoping** for each unstable class (`from`, `moduleSource`). | Presence of a `dagql.ImplicitInput` returning a session/client/random value ⇒ H-A2a *deliberate scoping*. Absence ⇒ look for H-A2b (a run-varying scalar arg) or H-A2c (a resolved-pin implicit input). | Cheap (source) | **DONE → deliberate scoping** (Part 3, §D) |
| 3 | **Client-ID disjointness from data.** Enumerate `Op.ClientID` in cold vs warm. | Disjoint client IDs ⇒ any `PerClientInput`-scoped recipe *must* differ across runs (closes moduleSource end-to-end). Overlapping ⇒ scoping is not the cause. | Cheap (data) | **DONE → disjoint** (Part 3, §E) |
| 4 | **Equivalence-hit join.** Every warm hit whose digest is absent from the cold trace, joined on `ResultID` against cold results. | Any warm hit resolving to a **cold** result (rid ≤ cold-max and present in cold) ⇒ a real cross-run equivalence hit exists (old story true). **Zero** such ⇒ the chains re-execute; no equivalence hit to transfer (old story false). | Cheap (data) | **DONE → zero, new & old captures** (Part 3, §F) |
| 5 | **ResultID-join soundness audit.** Is `nextSharedResultID` monotonic and never recycled between cold dump and warm run? | A reset/recycle path that can fire between the two ⇒ the join (checks 3–4, Finding A/B) is unsound. No such path under these conditions ⇒ join sound. | Cheap (source) | **DONE → sound** (Part 3, §G) |
| 6 | **Outcome-stamp trust audit.** Can a cache hit ever be stamped `executed`, or vice-versa? | If `executed` can be mis-stamped ⇒ the "re-execution" story (H-C) is suspect. If `hitCache` is set only on the lookup-hit path and `executed` only when the caller spawns work ⇒ trustworthy. | Cheap (source) | **DONE → trustworthy, TUI-corroborated** (Part 3, §H) |
| 7 | **Unification decomposition (1b).** Read `WithContentDigest → TeachContentDigest → teachResultIdentityLocked`; check whether each unstable class attaches a *session-independent* content digest. | Session-independent content digest that matches a prior result ⇒ the executed call adopts the pre-existing id (H-B1 unify). No cross-session-stable content match ⇒ fresh id (H-B2 no-unify). | Cheap (source) | **DONE for `from` (unifies); asModule residual open** (Part 2, 1b) |
| 8 | **Warm-cheapness decomposition (1c).** For the unstable call, sum subtree self-time by op-kind and count child hit/executed outcomes, cold vs warm. | Warm cost dominated by the *same* call_exec self-time ⇒ external re-run price variance (not inner hits). Warm cost collapses because an expensive `exec_phase` child is *absent* ⇒ explained by inner content hits. | Cheap (data) | **DONE → `from` = price variance; `asModule` = inner hits** (Part 3, §I) |
| 9 | **Client-side ID structural diff.** Capture the two runs' full dagql `call.ID` protobuf for the unstable call and diff field-by-field (receiver / field / args / implicitInputs / module / nth / view). | Names the *exact* self-digest component that differs — distinguishes H-A2a (an implicit-input field like `fromSessionScope`/`cachePerClient`) from H-A2b (a user-visible scalar arg). Confirms check 2's code reading against the wire. | Medium (client instrumentation; no engine change) | Proposed |
| 10 | **E-graph debug trace on a fresh cold+warm pair.** Flip `debugEGraphTrace` (`dagql/cache_debug.go:22`), rebuild the isolated dev engine, run the workload twice, diff the `lookup_attempt` / `teach_content_digest` / `eqclass_merged` lines. | Confirms from the engine's own logs: (a) the unstable call's `request_self` differs while `request_inputs` match (backs H-A2 self-digest); (b) whether `from`'s warm result is unified into the cold eq-class and `asModule`'s is not (settles H-B). | High (rebuild + fresh run; isolated container/port only — never touch peer engines) | Proposed |
| 11 | **Live `/debug/dagql/egraph` snapshot** after cold, then after warm. | The `EqClasses` / `Digests` mappings show whether the from content digest lands in one class spanning both runs' result ids (unify) vs asModule in two classes (no unify) — no rebuild needed, unlike check 10. | Medium (isolated engine already running; one HTTP GET each phase) | Proposed |
| 12 | **Stabilization probe (the only check that can move the headline drift).** In a scratch engine, force a canonical/digest-pinned ref for `from` (or a stable client id for `moduleSource`) and re-run the calibration. | If the cold region then elides as a real warm hit and drift collapses toward the session-phase floor ⇒ the instability is the whole gap and is fixable by canonicalization (confirms the §3a lever in `equivalence-facts-scoping.md`). If drift persists ⇒ another residual dominates. | High (fresh runs, config surgery) | Proposed — highest value, do last |

**How to read this table.** Checks 1–3 already answer question 1a (the *what*
and the *why*) to the resolution the data and source permit. Checks 4–6 secure
the ground the answer stands on. Check 7 answers 1b for `from` and isolates the
one open residual (asModule's identity). Check 8 answers 1c. Checks 9–11 are
independent confirmations of the same conclusion by three different instruments
(the wire ID, the engine's trace, the live e-graph) — run one if a second
witness is wanted before acting; check 10 is the most complete but the most
expensive. Check 12 is the only one that changes the calibration number and so
is the natural next escalation to Erik, not something to run unilaterally.

---

## Part 2 — Hypothesis enumeration, with evidence for and against

### 1a. Why these recipe digests differ across runs while siblings are stable

The recorded `Ident` of a `call` op is its recipe digest =
`deriveRecipeDigest` over the call frame: `{receiver-digest, type, field,
scalar-arg bytes, implicit-input bytes, module-digest, nth, view}`
(`dagql/result_call_frame.go:633`). `Op.CacheInputs` records only the
*structural input refs* — `{receiver, ID-typed args, module}` digests
(`selfDigestAndInputRefs`, `result_call_frame.go:839`; emit at
`dagql/cache.go:3823-3849`). So the self-digest — type/field/**scalar
args**/**implicit inputs**/nth/view — is *not* in `CacheInputs`. This is the
lever the recursive differ pulls.

- **H-A1 — instability propagates up from a produced input** (a resolved
  registry pin, a filesync/host-upload identity, a nested value). *Evidence
  against (VERIFIED):* the recursive differ bottoms out at the call's own
  self-digest, not at any propagated input — for `from` the single input (the
  receiver) is byte-identical across runs; for `Query.moduleSource` there are
  no inputs at all yet the idents differ (Part 3, §C). **Refuted** for these
  calls.

- **H-A2 — instability is in the call's own self-digest.** Confirmed by the
  differ (H-A1's refutation *is* H-A2's confirmation). Sub-hypotheses for
  *which* self component:
  - **H-A2a — a deliberate per-session/per-client cache-key scope
    (implicit input).** *Evidence for (VERIFIED):* `Container.from` attaches
    `fromSessionScopeInput` (`core/schema/container.go:1015`), a
    `dagql.ImplicitInput` whose resolver returns `clientMD.SessionID` for a
    **tag-only** ref and `""` for a **digest-pinned** ref
    (`container.go:1030-1046`); `Query.moduleSource` is registered
    `.WithInput(dagql.PerClientInput)` (`core/schema/modulesource.go:65`),
    and `PerClientInput` mixes in `clientMD.ClientID`
    (`dagql/cache_inputs.go:14`). Implicit inputs are hashed into the
    self-digest (`recipeDigestWithVisiting`, `result_call_frame.go:688-694`),
    exactly where the differ localised the divergence. Client IDs are disjoint
    across the two runs (Part 3, §E), so a `PerClient`-scoped recipe *must*
    differ. The design intent is in the code comments: *"Tag-only refs are
    mutable; resolve once per session"* and *"scope to the session so that
    resolution of a tag→digest is cached within the session but not across."*
    **This is the confirmed cause.**
  - **H-A2b — a user-visible scalar arg varies** (e.g. the address string).
    *Evidence against:* the pipeline passes the literal `alpine:3.20` both runs
    (`cold-run.log`/`warm-run.log`), and the stable sibling proves an identical
    scalar path is stable. Not needed to explain the data; the scoping input
    (H-A2a) accounts for it. **Not operative here** (would be separable by
    check 9 if ever in doubt).
  - **H-A2c — a resolved-pin / timestamp implicit input.** *Evidence against:*
    the resolver's rebinding writes a *content* digest
    (`container.go:1165`), which is session-independent (below), not a
    recipe input; no timestamp enters `deriveRecipeDigest`. **Refuted.**

  **Why the sibling is stable (VERIFIED):** the stable `from`
  (`xxh3:bd838c8a8919c204`) is a digest-pinned/canonical ref, for which
  `fromSessionScopeInput` returns `""` (`container.go:1041-1043`) — no session
  in its key — so it is identical across runs and warm-hits. The unstable
  `from` (`9382…`/`076f…`) is the tag `alpine:3.20`, which gets the session id.

  **General mechanism (VERIFIED, for the report's reuse):** the scoping family
  is `dagql/cache_inputs.go` — `PerClientInput` (client id), `PerSessionInput`
  (session id), `PerCallInput` (a fresh random id *every call* —
  `identity.NewID()`), `PerSchemaInput` (schema digest), and
  `RequestedCacheInput` (client-or-per-call by a bool arg). Any call carrying
  `PerClient`/`PerSession`/`PerCall` is unstable across the cold/warm boundary
  by design.

### 1b. Why `from`'s warm result unified (rid 6801) and `asModule`'s did not

The recorded id is read at op *end* (`profOp.EndWithResult(outcome,
profResultID(res))`, `dagql/cache.go:3741`; `profResultID` =
`shared.id`, `dagql/wcprof_hooks.go:66`), i.e. **post** any unification — the
warm result's *original* id is not in the dump. So the dump shows the outcome
of unification, not its mechanism; the mechanism comes from the code.

- **H-B1 — `from` unifies because it teaches a session-independent content
  digest that matches the cold result.** *Evidence for (VERIFIED):* after the
  executed `from` resolves the image, it calls `WithContentDigest(ctx,
  HashStrings("container.from", refName.Digest().String(),
  ctr.Platform.Format()))` (`container.go:1165`) — no session/client input, so
  the content digest is identical across runs. `WithContentDigest` on an
  attached result routes to `cache.TeachContentDigest` (`dagql/cache.go:2450`),
  which re-derives the identity with that content digest and calls
  `teachResultIdentityLocked` (`dagql/cache_egraph.go:988,1060`) — the
  eq-class merge point. The cold run taught the *same* content digest for the
  same image, so the warm result lands in the cold result's eq-class and the
  call resolves to the canonical (cold) id 6801. Consistent with the data:
  warm `from` executed (658ms) yet recorded rid 6801 (Part 3, §A). **Confirmed
  for `from`.**
- **H-B2 — `asModule` gets a fresh id because it has no cross-session-stable
  content match.** *Evidence for (partial, VERIFIED):* the data shows no
  unification (cold rid 5305, warm rid 8167). The module's `_implementationScoped`
  content digests *are* source-derived and session-independent
  (`HashStrings("...._implementationScoped", sourceImplementationDigest)`,
  `core/schema/module.go:3053`, `modulesource.go:3140`), so the *inner* content
  transfers (that is why the codegen hits — Part 3, §I). **HYPOTHESIS for the
  outer object:** the top-level `asModule` result either attaches no
  cross-session-matching content digest to the returned `Module` object, or its
  content-preferred identity chains through the per-client `moduleSource` so the
  taught digest differs across sessions. *Discriminating test:* check 10 or 11 —
  observe whether warm `asModule` teaches a content digest already present from
  the cold run (predict: no) while `from` does (predict: yes); or read
  `moduleSourceAsModule`'s return path (`modulesource.go:3153+`) to the content
  digest actually set on the `Module` result. This residual does **not** affect
  cost (warm `asModule` is 24ms regardless) — it is an identity curiosity, not
  a drift driver.

### 1c. Whether the warm cheapness is explained by inner hits / content locality

- **`from`: NOT explained by inner hits (VERIFIED).** The warm from's 658ms is
  `call_exec` **self-time**, the same kind that carried the cold 2184ms; its
  subtree adds essentially no child work (Part 3, §I). External I/O has no
  wcprof emit site (`OpKindIO` unused — `engine/wcprof/wcprof.go:72`), so the
  registry-resolve time folds into `call_exec` self and is not further
  decomposable from the recording. The 2184→658ms difference (~1.53s) is the
  cold price minus the warm price of the *same unavoidable re-run* — registry /
  content locality below dagql on the second resolve. **This is the dominant
  withExec drift residual and it is not a data gap any equivalence emit can
  close: both runs genuinely execute this call.**
- **`asModule`: fully explained by inner hits (VERIFIED).** The cold subtree
  spends 12.6s in one `exec_phase` (codegen generate-typedefs) with 7924 inner
  hits; the warm subtree has **no `exec_phase` at all** and 24ms of
  orchestration (Part 3, §I). The expensive production did not re-run because
  the stable inner-call digests hit — exactly the general-rule transfer layer.

---

## Part 3 — What I verified by cheap read-only checks (method shown)

All probes are throwaway Go under `probe_scratch/` in this worktree (uncommitted),
using `wcanalyze.Load` + `Build`. Commands shown are what I ran.

**§A — The three implementer findings reproduce exactly.** `go run
./probe_scratch/` over `/tmp/whatif-cal2/`:
- `Container.from` cold `xxh3:9382969610723944` (2184ms, rid 6801) vs warm
  `xxh3:076fc01455d1bae4` (658ms, rid 6801); sibling `xxh3:bd838c8a8919c204`
  stable (cold executed 0ms rid 6801, warm hit rid 6801).
- `Query.moduleSource` cold `f1e4c77869350ecb` (rid 4113) vs warm
  `a0aa6492a09da14d` (rid 8008); `ModuleSource.asModule` cold `338d592281b016ac`
  (14357ms, rid 5305) vs warm `f83e04ad7a3680be` (24ms, rid 8167).
Matches the brief's numbers to the digit. **VERIFIED.**

**§B — `CacheInputs` (the Chunk-4 emit) is populated in the new captures, empty
in the old.** New: 11499/23106 (exec cold) and 18302/32032 (module cold) call
ops carry inputs; old `/tmp/whatif-cal`: 0/23106, 0/32032. Both eras carry
`ResultID`. `dropped=0`, `suppressedIdent=0`, `suppressedForcer=0`,
`openOps=0` on all four new captures — so the what-if-cached refusal gates
(design §6.5 note 13) are clean. **VERIFIED** (`probe_scratch/differ`).

**§C — The recursive CacheInputs differ bottoms out at the self-digest.**
`go run ./probe_scratch/differ/`:
- `Container.from`: 1 input, **identical** cold vs warm ⇒ "divergence is in the
  call's SELF digest, not an input. LEAF."
- `Query.moduleSource`: 0 inputs, idents differ ⇒ "divergence in the call's
  SELF digest. LEAF."
- `ModuleSource.asModule`: 1 input that differs ⇒ recurse → `Query.moduleSource`
  (its input) → self-digest leaf.
So the run-varying element is each call's *own* args/implicit-inputs, and it does
not flow up from a produced input. **VERIFIED.**

**§D — Both instabilities are deliberate session/client cache-key scoping.**
By reading source:
- `Container.from` (tag-only): `fromSessionScopeInput`, a `dagql.ImplicitInput`
  returning `clientMD.SessionID` for tags and `""` for digest refs
  (`core/schema/container.go:1015-1046`).
- `Query.moduleSource` and the module-source mutators (`withSourceSubpath`,
  `withIncludes`, `withUpdateDependencies`): `.WithInput(dagql.PerClientInput)`
  (`core/schema/modulesource.go:65,114,127…`); `PerClientInput` mixes in
  `clientMD.ClientID` (`dagql/cache_inputs.go:14`).
- Implicit inputs are hashed into the self-digest (`recipeDigestWithVisiting`,
  `result_call_frame.go:688-694`), matching §C's localisation. **VERIFIED.**

**§E — Client IDs are disjoint across cold/warm.** `go run
./probe_scratch/cid/`: exec cold `ovw8oerhb82rha07mot9b3biw` vs warm
`ow87spez634zxistz0ct4cox0`; module cold `mu0l3lqsjmwkewmfu8b3ppyyo` (+ nested
`1cuz…`) vs warm `uixy611hovw25h8gql97kzza3`. Disjoint ⇒ any
`PerClientInput`-scoped recipe necessarily differs across the two runs.
**VERIFIED.**

**§F — Zero cross-run equivalence hits, in the new AND the old captures.**
`go run ./probe_scratch/oldcaps/ <base>`: warm hits whose digest is absent from
the cold trace, joined on `ResultID`:
- new `/tmp/whatif-cal2`: exec 4 absent-digest hits, **0** join to a cold rid,
  all 4 rid > cold-max (6805); module 22, **0** join, all > cold-max (8003).
- old `/tmp/whatif-cal`: exec 4, **0** join, all > cold-max (6805); module 22,
  **0** join, all > cold-max (8002).
Every such warm hit is on a result the warm run itself produced. **The old
"warm hit through the equivalence machinery" explanation was unsupported by the
very captures it was written about — wrong from the start, not just on the new
data.** The unstable chains re-execute warm; there is no cross-run hit to
transfer. **VERIFIED.**

**§G — The ResultID join is sound.** `nextSharedResultID` is monotone
(`c.nextSharedResultID++` at `dagql/cache_egraph.go:1481`; persistence import
sets it to `max+1`, `cache_persistence_import.go:384`); the only reset,
`maybeResetEgraphLocked`, fires **only when `len(c.egraphTerms)==0`**
(`cache_egraph.go:1703-1706`). Between cold and warm the cold results are
retained (that is *why* warm hits them), so the egraph is non-empty and the
reset cannot have fired — the observed warm hits are themselves the proof. IDs
are therefore never recycled across the boundary, so `ResultID` equality means
same shared result. **VERIFIED.**

**§H — The `executed` outcome is trustworthy.** `hitCache: true` is set only in
the lookup-hit return path (`dagql/cache.go:4066`); the `executed` hint is set
only when the caller spawns the execution (`cache.go:3980`), `joined` at
`:3890`. The final stamp is `hit`/`hit_pending` iff `res.HitCache()`, else the
hint (`cache.go:3722-3741`). A content-teach unification at insert does **not**
set `hitCache`, so a call that executes-then-unifies is correctly `executed`
with a post-unification `ResultID` — precisely the `from` case. Independently
corroborated by the warm TUI: `from(alpine:3.20)` shows a green check at 0.7s
(≈ the 658ms), *not* `CACHED`, while `container`, all `withExec`s and `stdout`
show `CACHED` (`/tmp/whatif-cal2/wl-exec/warm-run.log`). **VERIFIED.**

**§I — Warm-cheapness decomposition.** `go run ./probe_scratch/decomp/`:
- `from` cold: 2184ms, all `call_exec` self (2183ms), subtree = 2 executed
  calls. Warm: 658ms, all `call_exec` self, subtree = 1 executed + 1 hit. Same
  self-time kind, no expensive child appearing/disappearing ⇒ external re-run
  price variance, not inner hits.
- `asModule` cold: 14357ms, `exec_phase` self 12587ms (+ call_exec 1678ms),
  subtree = 1191 executed / 7924 hit. Warm: 24ms, `call_exec` 17ms + `call`
  10ms, **no `exec_phase`**, subtree = 159 executed / 33 hit. The 12.6s codegen
  is absent warm ⇒ cheapness is inner content hits. **VERIFIED.**

---

## What this means for Track B (plain words)

1. **The calibration drift on these two calls is not a data gap the analyzer or
   any equivalence emit can close.** The engine deliberately refuses to reuse a
   tag resolution or a local-module load across sessions (session/client
   scoping). The cold region can never be a warm hit *by design*, so the
   simulator honestly replays it at cold cost, and the residual is the cold
   price minus the warm price of work both runs actually do. The doctrine holds:
   this is reality, not a model defect.

2. **The equivalence-fact recording proposal (equivalence-facts-scoping.md §3c)
   would not move these numbers** — confirmed, since zero cross-run equivalence
   hits exist in *either* capture era. Its value is explanatory (itemising the
   residual via the already-recorded ResultID join, §3b), not a smaller drift.

3. **The only lever that moves the headline drift is recipe stabilization
   (§3a): canonicalizing the tag to a digest pin, or a stable client identity.**
   That is a change to how the *engine keys* these calls, with correctness
   implications (stale-tag reuse across sessions is what the scoping prevents) —
   an Erik decision, and check 12 is how we'd measure whether it collapses the
   drift before committing to it.

4. **The `from` result's cross-run unification (rid 6801) is real and benign**:
   the tag `from` re-executes (session-scoped recipe) but its *output* is
   content-addressed session-independently, so it merges into the cold result
   and everything downstream of it (the withExecs) hits. That is why the warm
   pipeline is fast despite the from-call re-running — the one place the
   recording's "executed with a pre-existing rid" shape is not a bug but the
   expected trace of content unification after a forced re-resolve.
