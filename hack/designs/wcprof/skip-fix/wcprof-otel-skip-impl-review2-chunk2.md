# Skip-fix implementation plan v2 — round-2 review (Chunk 2 implementer)

Read v2 in full; re-verified every changed claim against the skip-implementer worktree
(`wcprof-otel-skip-implementer-a7daa7c9`, HEAD `4585bf413d`). File:line are that worktree.

## Verdict

**v2 faithfully incorporated round 1 and the architecture is approved.** The predicate flip, N1, N3,
and the three pushbacks are all correct and verified. Two substantive findings remain from the
holistic pass, **neither a merge-blocker** (the doc's own §9 empirical net would catch them), but both
worth fixing before merge: **(1) N2's provenance enumeration is incomplete and the §5.7 memo design is
circular**, and **(2) a few "make it robust/explicit" gaps** (store the flag on the frame or
recompute-at-import; state the recipe-preserving-update invariant; the `FunctionCall` trap). The
zero-inference principle and the goal are preserved. Implement, with the items below folded in.

## 1. Changes — did v2 incorporate round 1?

**Predicate flip → CORRECT, well-executed.** The separate, debug-independent, receiver-type
classifier is right. Verified: the 11 reflection types are real schema object types
(`core/schema/module.go:418-648`); the cut is a pure recipe function (receiver type + field, both in
`callKey`, `cache.go:3669`), so §4 holds with **no** non-recipe input — the v1 §10 debug caveat is
genuinely gone. The §3.1 retraction is sound: I re-confirmed the loader builds `opIDBySpan` for every
span with no kind filter (`loader.go:251-254`) and orphans only on an *absent* parent
(`:299-310`), and a normal `dag.call` span classifies as a present `"call"` op (`:444-445`) — so the
debug-orphan argument was indeed false. The "debug-gating is actively harmful (re-arms the amplifier
in the mode you capture to)" point (§3.1) is a sharp, correct addition.

**N1 (outer native `OpKindCall`) → VERIFIED correct.** `getOrInitCall` mints
`wcprof.BeginOp(OpKindCall)` at `cache.go:3562`, gated at `:3559` (`if !wcprof.Enabled || req==nil ||
req.ResultCall==nil`), passing a `nil` profOp to the inner on early-return; profOp methods are nil-safe
(`record.go:124-130`). Adding `|| req.SkipProfile` is clean and is the right oracle-symmetry fix.

**N3 (lazy target-flag gating) → CORRECT, and the reframing is right.** The forcer (`cache.go:2964/
:2973`) is a different recipe than the producer (`shared.loadResultCall()`), so §4.2's "waiter shares
the recipe" genuinely does not cover lazy; closing it by gating every lazy wait on the producer's
stored `shared.profSkip` is **load-bearing**, not robustness. The recommendation to comment this so no
one "simplifies" the lazy gate to the waiter's own bit is good — that simplification would reopen a
cross-recipe dangle.

**The three pushbacks → all correct.**
1. **`!IsRecording` useless in production — agreed** (I made the weaker version in round 1; v2's
   sharper "useless, not weak" is right: production records, so the guard is false on the hot path).
2. **chunk3 parentless-`publishResult` deferred — correct and genuinely orthogonal.** The kept
   survivors are parented (or not) independently of the skip; and parentless survivors have an *empty*
   `cpSpan`, which does **not** trip `OrphanedParents` (the orphan test requires `cpSpan != ""`,
   `loader.go:303`). So the skip fix's own gate (`OrphanedParents`/`UnresolvedWaitTargets`) is clean
   without chunk3's fix. The §5.6 caveat ("don't over-claim gate-clean under the *proposed*
   internal-kind-root signal") is the right scoping.
3. **profiler-skip ⊋ UI-suppress — correct and desirable** (this is exactly the decoupling I argued in
   round 1 point B; keeping `introspectionInfo`/UI untouched is right).

**Open items:**
- **(a) import provenance — recommend RECOMPUTE at import, not accept-default-false.** Import already
  reconstructs the `resultCall` frame (`cache_persistence_import.go:164-175`), so `profSkip =
  profileSkip(frame)` there is a pure, off-hot-path computation — cleaner than carrying a default-false
  volume edge, and it also removes a real flag *disagreement*: for an imported result later
  singleflighted, `oc.profSkip` is computed fresh from `req` (correct) while `shared.profSkip` would be
  the imported default-false — the two paths stay individually self-consistent (no dangle) but the lazy
  side needlessly profiles introspection. Recompute makes both agree. (Accept-default-false remains a
  safe fallback if a lock-safe recompute point proves awkward — but it is awkward to *prove* negligible
  per §9; recompute is simpler to reason about.)
- **(b) over-cut audit — PASSES my spot-check.** No reflection-type field does container/exec/module
  work: there is no `Function.call`/exec field; the only `AsContainer` in the schema
  (`module.go:2088`) is in `moduleRuntime` whose receiver is `*core.Module` (not a reflection type);
  the slow loaders (`Query.moduleSource`, `ModuleSource.asModule`) have non-reflection receivers and
  stay profiled. Keep it a §9.4 hard check, but the assumption holds.

## 2. Holistic — new findings (now that the design is concrete)

### [HIGH] N2 enumeration is incomplete; the invariant is right but the site list isn't
The N2 *invariant* ("`profSkip` travels with the stored `resultCall`") is correct, and the **primary**
path (req-frame in `initCompletedResult`, `cache.go:4160`) is handled. But the actual set of sites that
set a `sharedResult.resultCall` (`storeResultCall`, `cache.go:1547`) is larger than the doc enumerates,
and its cited "audit" lines (1799/2431/2531) are *construction literals*, not the `storeResultCall`
calls. Grounded list:
- **Frame-ESTABLISHING sites the doc misses** (a result gets its first/derived frame here → `profSkip`
  must be set, or it defaults false):
  - `cache_egraph.go:1485` — `index_wait_result_request_frame`: backfills `requestFrame.clone()` when
    `res.loadResultCall()==nil`. A singleflight-wait result indexed here gets no `profSkip`.
  - `cache.go:2356` — nth-element load: `storeResultCall(req.ResultCall.clone())` for a *derived*
    (forked, `Type=Elem`, `Nth`) recipe — different recipe than the parent, so even a copied flag could
    be wrong.
- **Frame-UPDATING sites the doc doesn't analyze** (a frame is swapped for a *same-recipe* refinement →
  `profSkip` is unchanged, so these are safe — but the doc never says *why*):
  - `cache.go:1064` (`teach_content_digest`), `:1975` (`attach_result_normalized`), `:2568` (fork copy).

**Consequence is bounded, not a gate hole** — I verified both directions: a wrong `profSkip` on a
`sharedResult` yields either volume (false-when-should-be-true → introspection lazy op profiled) or
real-work coarsening (true-when-should-be-false → forcer's wait dropped), but **never a dangle**,
because every wait gates on the *same* stored flag (so target-absent ⇒ wait-absent on both sides).
That matches the doc's own "bounded volume edge" framing for import. So: not a merge-blocker, but the
§9 completeness assertions must explicitly exercise **index-wait-backfilled** and **nth-element/derived**
results (not just singleflight/lazy/adopt/import), and the citations corrected.

**Robustness recommendation (dissolves most of the audit):** store the flag **on the `ResultCall`
frame**, stamped in `AroundFunc` (where `profileSkip` is already computed lock-safely), instead of on
the `sharedResult`. Because `profSkip` is a pure function of the frame's recipe, it then travels
*automatically* through `clone()`/store/adopt/index-wait — i.e. `cache.go:4151/4160`,
`cache_egraph.go:1485`, the adopt path, all "just work." Only **recipe-DERIVING** forks (`:2356`, and
any `fork()` that changes `Type`/`Nth`/`Receiver`) still need a recompute — a much smaller, clearer
audit surface than "every `storeResultCall` site." (On-demand computation at the lazy gate is *not*
viable: `profileSkip`'s `ReceiverCall`→`egraphMu.RLock` under `lazyMu` is the exact lock nest the
`CallRequest` seam exists to avoid.)

### [MED] The §5.7 memoization key is circular — it can't deliver the saving it claims
§5.7 proposes memoizing `profileSkip` by `(receiverTypeName, field)` to cut the `egraphMu.RLock` cost.
But obtaining `receiverTypeName` *is* the `ReceiverCall`→`resultCallByResultID`→`egraphMu.RLock` lookup
(`introspectionInfo` reads `receiver.Type.NamedType` only after `ReceiverCall`, `telemetry.go:393-403`;
`result_call_frame.go:1377-1387`). So a `(receiverTypeName, field)` memo caches only the *cheap*
set-membership check, after paying the *expensive* lookup. To actually avoid the lock, key the memo on
something available **without** the lookup — `frame.Receiver.ResultID` (`result_call_frame.go:64`, on
the frame, lock-free): cache `receiverResultID → isReflectionType` (or `→ profSkip`). Module load makes
many field accesses on the *same* reflection-object instances, so a resultID-keyed memo actually hits.
(First check whether the receiver's type is reachable lock-free via `frame.Receiver.shared` — if so,
no memo is needed at all.) Either way, fix the key before landing the memo.

### [LOW] State the recipe-preserving-update invariant
The reason the update sites (`teach_content_digest`, `attach_result_normalized`, fork-copy) are safe is
that they swap a frame for a *same-recipe* refinement, so `profSkip` is stable and there is **no
mid-flight flag change** (which would otherwise risk a transient mint/wait mismatch). The doc relies on
this implicitly; say it, so a future change that makes one of those sites recipe-*altering* is flagged.

### [LOW] `FunctionCall` vs `Function` name trap
The reflection set must contain `Function` (metadata) but **not** `FunctionCall` — the latter is the
active-call context with real-work, `DoNotCache` fields (`returnValue`/`returnError`,
`module.go:251-261`). v2's set correctly lists `Function` only; add a classifier test asserting
`FunctionCall.returnValue` is **not** skipped, so a future "add the obvious sibling" edit can't silently
coarsen real work.

### [affirm] Goal-safety is real and is the crux of why this cut is acceptable
The skip removes only the **fast in-memory metadata walk** (reflection accessors); the **slow** part of
module load — `Query.moduleSource`/`ModuleSource.asModule` and the SDK exec underneath — has
non-reflection receivers and **stays profiled**. So "find what's slow / user-work first-class" is
preserved by construction, not by hope. This is the load-bearing goal argument and it checks out.

### [flag] Native skip changes merged PR #13393 behavior
Gating native on the same flag (§5.4) removes introspection ops from native `--profile` dumps too — a
behavior change to a shipped feature. Correct for the oracle and native is dev-only, but it deserves
the explicit "fine losing introspection in native" sign-off (same call Erik already made for OTel),
exactly as the doc flags. Not a blocker; just don't let it land silently.

## Bottom line

v2 is a strong, faithful response to round 1 — predicate flip, N1, N3, and all three pushbacks verified
correct, architecture approved, zero-inference/goal intact. Land it with: **N2 made
robust-by-construction (flag on the frame + recompute-at-import) and its §9 assertions actually
exercising index-wait/derived/imported results**; the **§5.7 memo key corrected to the lock-free
ResultID**; the recipe-preserving-update invariant and the `FunctionCall` test added; and the native
PR #13393 change explicitly signed off. None of these blocks the approach; all are completeness/
robustness, and the doc's elevation of §9 to the correctness centerpiece is the right instinct — these
just make sure the net is cast over every frame-provenance path, not most of them.
