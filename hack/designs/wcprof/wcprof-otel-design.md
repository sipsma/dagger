# wcprof × OTel — design + implementation plan

**Status:** design for review (no code written). **Author:** fresh-eyes pass off `main`.
**Audience:** wcprof lead + project owner.

> One-line thesis: the native wcprof recorder already instruments exactly the
> right choke points with exactly the right model (`call` / `call_exec` / `lazy`
> / `exec` / `service_start` + explicit **wait edges**). The OTel source is not a
> new model — it is **the same model emitted a second way**. The whole job is to
> make the engine's OTel spans carry the *wait edges, shared-execution identity,
> and — where the UI deliberately re-points a span — an explicit causal-parent
> override* that native records inline, because OTel's span tree (built by context
> propagation) silently drops or mis-attributes them at four specific places. Fix
> those four at the emit side; the loader is then a mechanical
> spans→ops→waits translation (causal parent = `wcprof.parent ?? parentId`) that
> reuses `wcanalyze` unchanged — and the UI is untouched throughout.

---

## 1. What we reuse, and the exact IR contract

The analyzer and its counterfactual replay are the foundation and are treated as
correct. Concretely, the OTel source must produce the same input that the native
dump path produces:

- `wcanalyze.Build(header *wcprof.DumpHeader, events []wcprof.DumpEvent) (*Graph, error)`
  (`engine/wcprof/wcanalyze/graph.go:147`) is the single entry point we target.
  It consumes a `DumpHeader` (interned string table + open-ops)
  (`engine/wcprof/dump.go:16`) and a flat list of `DumpEvent`
  (`engine/wcprof/dump.go:41`) whose `Type` is `"op" | "wait" | "link"`.
- From those, `Build` wires structural parents (`ParentID`→`Parent`/`Children`,
  `graph.go:283-301`), re-parents nested-client roots under their hosting exec
  via `link` events (`graph.go:270-294`), attaches wait edges
  (`graph.go:247-268`), and computes self-time as *interval − children − waits*
  (`graph.go:381-398`).
- The replay (`engine/wcprof/wcanalyze/replay.go`) then runs the counterfactual.

**Therefore the loader's output target is `[]wcprof.DumpEvent` + a
`wcprof.DumpHeader`.** We do not need (and should not add) any new analyzer
type. The OTel loader is "compile spans → the existing dump IR, then call
`Build`". This is the cheapest possible reuse and it keeps native and OTel
sharing one code path from `Build` onward (including the validation surfaces in
`report.go`).

### 1.1 The single assumption that everything hinges on

The replay has exactly one piece of *implicit* causal inference — the
**implicit join** (`replay.go:23-27`, implemented in `joinUpTo`,
`replay.go:353-369`): when an op reaches an action at original time `t`, it first
joins (waits for) every *child* that had originally ended by `t`. This is what
lets native get away with **not** emitting an explicit wait edge for a plain
synchronous resolver call — the parent op is literally blocked inside the child
on the call stack, so the nesting itself *is* the wait.

This is sound for native because native builds parentage from the live call
stack: `BeginOp` reads the current op out of context (`wcprof/record.go:46-61,
93-122`) and a child's parent is whoever was executing when it began. A parent op
exists on the stack only while it is genuinely blocked in its child.

Everything in this document follows from one fact: **OTel parentage is NOT built
from the live call stack — it is built from context propagation**, and context
propagation is faithful to "blocked inside" only for synchronous calls. Where the
engine detaches work to a goroutine, re-points a span for UI reasons, or
suppresses a span entirely, OTel's parent edge stops meaning "synchronously
waited for," and feeding it to the implicit join *invents* dependencies
(over-serialization) or even cycles. So the design must guarantee:

> **Invariant E (emit faithfulness):** the analyzer never reads `parentId`
> directly as causality. Each op has a **causal parent** = an explicit
> `wcprof.parent` override if the engine emitted one, else the span's `parentId`
> (§3.0, §5). Invariant E is: *every causal-parent edge is a genuine synchronous
> nesting*, and *every other blocking dependency arrives as an explicit wait edge
> attributed to the op that actually blocked*.

The override exists for exactly one reason: the engine sometimes deliberately
sets a span's `parentId` to something that is **not** its causal parent — to keep
the UI rendering a certain way (the lazy-evaluation re-point, §2.5). In those
cases `parentId` is the *UI* parent and `wcprof.parent` is the *causal* parent;
they legitimately differ, and the engine — which knows both — emits the causal one
explicitly. This is **emit, not inference**: the loader only ever *reads*
`wcprof.parent`; it never derives one (§3.0, §5). For every other choke point
`parentId` already *is* the causal parent, so no override is emitted and none is
read.

The rest is: find every place the engine violates Invariant E in its OTel output,
and fix it at emit.

---

## 2. The four faithfulness breaks (grounded in the code)

I traced each choke point the brief flags. Here is what the engine actually does
today, and precisely how OTel diverges from native.

### 2.1 The dagql call span merges three things into one interval

A field call goes through `ObjectResult.call` (`dagql/objects.go:635`). The
telemetry span wraps the **entire** `GetOrInitCall`:

```
dagql/objects.go:655   if s.telemetry != nil && !field.Spec.NoTelemetry {
dagql/objects.go:656       telemetryCtx, done := s.telemetry(ctx, req)   // AroundFunc → span Start
dagql/objects.go:657       defer func() { ... done(res, res.HitCache(), &err) }()  // span End
dagql/objects.go:664       ctx = telemetryCtx
dagql/objects.go:678   res, err = cache.GetOrInitCall(ctx, ..., fn)      // lookup + singleflight + run
```

So a single OTel call span spans `[cache lookup] + [singleflight wait] +
[resolver execution]`. The span is created in `core.AroundFunc` (`core/telemetry.go:136`)
with `dag.digest` (the cache key) and `dag.call` attributes
(`core/telemetry.go:83-86`), and the done-callback records `dag.cached` /
`dag.pending` (`core/telemetry.go:258-297`).

For the **executor** caller this is actually fine: it blocks in `c.wait` on a
channel (`dagql/cache.go:3717`, `:3875`) while the resolver `fn` runs in a
**detached goroutine** (`dagql/cache.go:3700-3713`) whose context descends from
the executor's, so the resolver's sub-call spans nest under the executor's call
span — a genuine synchronous nesting. Self-time = call span − children ≈ cache
overhead + the resolver's own non-sub-call work. ✔

The problem is everyone else.

### 2.2 Break #1 — singleflight joiners: the wait is lost or mis-typed

When a second caller hits an in-flight identical call, it joins:

```
dagql/cache.go:3647   if oc := c.ongoingCalls[callConcKeys]; oc != nil {
dagql/cache.go:3653       oc.waiters++
dagql/cache.go:3655       profOp.SetOutcomeHint(wcprof.OutcomeJoined)
dagql/cache.go:3656       return c.wait(ctx, ..., oc, req, true)   // blocks until executor's fn done
```

Native records this honestly: a `call` op for the joiner
(`dagql/cache.go:3515`) **plus** an explicit wait edge to the shared execution
op `oc.profOpID` with reason `singleflight` (`dagql/cache.go:3867-3874`). The
joiner's self-time is therefore ~0 (its whole interval is a wait).

OTel records **nothing causal**. The joiner's call span (if emitted) covers
`[join … executor's fn end]` but has **no children and no wait edge** — so the
loader would see a span whose entire duration is self-time (it looks like real
work) and which is *not connected to the execution it actually waited for*.
Scaling the execution's cost in the counterfactual would not propagate to the
joiner's critical path, and the joiner's class would be over-credited with
self-time it never spent working. (This is the `actWaitNoop`/`actSelf` confusion
the replay model cannot fix from the consumer side — see `replay.go:162-174`.)

### 2.3 Break #2 — telemetry suppression deletes the joiner's span entirely

Worse, the joiner's span often does not exist at all. `core.AroundFunc` consults
`ShouldEmitTelemetry` (`core/telemetry.go:62`), which suppresses any **repeated
cacheable** call:

```
dagql/telemetry.go:57   if seen && !doNotCache { return false }
core/telemetry.go:63    return ctx, dagql.NoopDone   // ← ctx returned UNCHANGED
```

"Seen" is keyed on the call digest in a per-session store
(`dagql/telemetry.go:48-64`), so suppression fires for **every repeat of a
digest within the session**, not just concurrent ones. Two consequences:

1. **Sequential repeats** (e.g. `ctr.sync()` then later `ctr.stdout()` on the
   same recipe) → the repeat is suppressed and is almost always a *cache hit*.
   Hits are negligible for makespan (the result already exists), so dropping them
   is acceptable — see §4.1.
2. **Concurrent duplicates** → exactly one caller emits the span; the others are
   suppressed. Because `ctx` is returned **unchanged** (`core/telemetry.go:63`),
   any spans created *under* a suppressed call attach to the suppressed call's
   **parent**, not to the call. For a singleflight joiner that's harmless (the
   joiner runs no resolver), but it means the joiner's wait — already missing per
   Break #1 — folds into its parent's self-time instead of even being a visible
   gap.

### 2.4 Break #3 — emitter ≠ executor: the resolver's children mis-parent

The telemetry-suppression order (the `LoadOrStore` in `ShouldEmitTelemetry`,
`dagql/telemetry.go:53-55`) and the singleflight-winner order (the `callsMu`
section, `dagql/cache.go:3642-3717`) are **independent locks**. So the caller
that *emits the span* for a digest need not be the caller that *executes the
resolver*. When they diverge:

- The emitter joins (its span covers a wait, no children).
- The executor is suppressed → its `ctx` is unchanged → the resolver's
  `fn` runs in the detached goroutine under the **executor's parent span**, so
  the resolver's sub-calls nest under an unrelated ancestor.

Net effect in the loaded graph: a `dag.digest=D` span that did no work but shows
full self-time, *and* D's real sub-work scattered under some other op's subtree.
This is precisely the "deduplicated/suppressed calls that emit no span of their
own … its causal edge attaches to its parent" failure the brief calls out, and
it is the kind of mis-attribution that produces **impossible structure** once you
add the wait edges back naively (an op appears to wait on work that the trace
parents elsewhere).

Native is immune because it begins the shared-execution op `execOp` on the
**call's own** detached context (`dagql/cache.go:3669-3677`) with wcprof's own
context key — independent of OTel's suppression — so the resolver's children nest
under `call_exec` no matter which caller won either race.

### 2.5 Break #4 — lazy evaluation re-points work to a span that already ended

This is the subtlest and the most cycle-prone. A resolver can return a *pending*
result and defer materialization (`internal-docs/lazy_evaluation.md`). Later,
some consumer forces it via `Cache.Evaluate` → `evaluateOne`
(`dagql/cache.go:2885`). Native models the deferred run as its own `lazy` op
begun on the **consumer's** eval context (`dagql/cache.go:2953-2961`), with the
callback's children nesting under it and other consumers waiting on it via
explicit edges (`dagql/cache.go:2940`). Consumer → `lazy` op is a true
synchronous nesting (the consumer blocks in `waitForLazyEvaluation`). ✔

OTel does something different on purpose, for UI reasons
(`internal-docs/lazy_evaluation.md:156-177`, code at `dagql/cache.go:2968-2995`):

```
dagql/cache.go:2984   resumeCtx, resumeSpan = Tracer(evalCtx).Start(evalCtx, "resume <field>", WithLinks(originalSpanCtx, installs...), Passthrough())
dagql/cache.go:2990   callbackCtx = trace.ContextWithSpan(resumeCtx, resumedCallbackSpan{ Span: resumeSpan, sc: originalSpanCtx, ... })
```

- The **resume span** is a passthrough child of the *triggering consumer* and
  merely **links** back to the original producer's span context (captured at
  produce time in `captureSessionLazySpanContext`, `dagql/cache.go:436-459`).
- `resumedCallbackSpan.SpanContext()` returns the **original producer's** span
  context (`dagql/cache.go:2803-2805`), so the callback's child spans (the real
  deferred work `W`) nest under the **producer** `O`, which **already ended** when
  it returned the pending result.

Loaded naively, this yields: `W` parented to `O` (a span that ended before `W`
started — the implicit join can't even apply, `replay.go:389`), while the
*actual* waiter (the consumer) has only a passthrough marker that the analyzer
would treat as a tiny self-time leaf. The real critical edge consumer → `W` is
absent, and the producer→work parent edge is a lie about synchrony. If a loader
tried to "fix" this by also treating the resume span's link-to-`O` as a causal
edge, it would create `O → W` (parent) and `… → O` (link) relationships that can
close a cycle — exactly the self-wait the brief warns never happens in a real
run.

The `parentId = O` re-point is **load-bearing for the UI and must not change**
(it is an intentional dagui design choice — deferred work renders under the call
that produced it). So this is the one place where the span's `parentId` is
deliberately *not* its causal parent. The fix (§3.2) keeps the re-point exactly as
today and adds an explicit **`wcprof.parent` causal-parent override** (§3.0) on
the re-pointed spans pointing at the consumer-side `lazy` op — UI reads `parentId`,
the analyzer reads `wcprof.parent`. No span moves; no cycle; no double-count.

### 2.6 Where OTel is already faithful (don't over-engineer these)

Two choke points the brief flags are, on inspection, mostly fine in OTel — and in
one case *better* than native:

- **Container exec / nested-client *parentage* (brief §5.3).** The executor
  propagates the caller's span context as the container's traceparent
  (`engine/engineutil/executor_spec.go:751-752, 836-837`), and `causeCtx` is the
  `withExec` call's span context (`core/container_exec.go:1304`). So the nested
  module-runtime client's API spans nest under the `withExec` span **via real
  trace propagation** — a genuine synchronous nesting (the exec is blocked while
  the container runs the module code). Native needs an explicit
  `LinkKindNestedClient` to achieve the same stitching
  (`engine/engineutil/executor.go:126-130`) because wcprof context can't cross
  the container boundary; OTel gets it for free and the loader needs no
  nested-client link logic at all. **Caveat:** this covers only *parentage*. The
  engine-vs-user *breakdown inside* the exec is **not** free — OTel emits no
  phase spans today, and the engine overhead is not sub-ms, so the design must add
  the `containerStart`/`processRun` split (§3.3). Don't conflate "parentage is
  free" with "the exec is fully modeled."
- **Services (brief §5.4).** The blocking part — `service.start` incl. health
  check — runs on a detached `svcCtx` (`core/services.go:971-977`) and callers
  block on it (`core/services.go:955-963`); the daemon then idles in a background
  goroutine until torn down (`core/services.go:1028-1034`). OTel keeps a single
  `serviceSpan` and adds an **origin link per installer**
  (`core/services.go:113, 230-237, 250-276`). The trap is only that the service
  span may cover the *idle availability* lifetime; see §3.4 for the small fix.

---

## 3. The design: faithful emit, choke point by choke point

Design principle throughout: **mirror native's already-correct model in OTel
form, emitting the minimum extra structure needed for Invariant E, and express
every blocking dependency as a wait edge on the *waiter* (not as a fan-in of
links on the target).**

### 3.0 The wait-edge wire format (one convention, used everywhere)

OTel has no wait-edge concept, so we add one. The decision that matters most
(and the one the brief's practical note about link caps points at): **attach the
causal edge to the waiter, never fan all waiters in as links on the target.**

A shared execution can have hundreds of joiners across a whole run; fanning one
link per joiner onto the *execution* span would blow the OTel SDK's default
128-link cap (`go.opentelemetry.io/otel/sdk/trace` `DefaultLinkCountLimit`) and
silently drop edges. Attaching to the *waiter* avoids that global fan-in — but it
does **not** make per-span link counts unconditionally tiny: a *suppressed*
caller has no span of its own, so its link lands on the current ancestor span
(§3.1), and one ancestor can fan out to many concurrent suppressed siblings. So
the cap must be engineered, not assumed away (see "Link-cap safety" below). The
rule is still: **attach the causal edge to the waiter, never to the target.**

> **Wait edges are emitted on the waiter's span, as span links with**
> `dagger.io/link.purpose = "wait"` **(a new purpose value alongside the existing
> `cause`/`error_origin`, `otel-go attrs.go:93-100`), carrying the target's
> span/trace id plus the blocked interval and reason as link attributes:**
> `wcprof.wait.start_unix_ns`, `wcprof.wait.end_unix_ns`, `wcprof.wait.reason`
> (`singleflight|call_exec|lazy|service|exec|lock|io`), and for resource waits
> (`lock`) `wcprof.wait.ident` instead of a target id.

**Timestamp encoding (must survive Cloud's JSON round-trip, and be knowable at
emit time).** The blocked interval is encoded as **absolute Unix nanoseconds, as
decimal strings**. Two independent requirements force this exact choice:

- *Decimal strings* dodge a precision bug: the Cloud trace API decodes link
  attributes into `map[string]any`, so every JSON *number* arrives as `float64`
  (`internal/cloud/trace.go:116`, link `Attributes map[string]any`;
  `:368-379`). Absolute Unix nanos (~1.8e18) exceed float64's exact-integer range
  (2^53 ≈ 9e15) and would be silently rounded (~256 ns) — immaterial for ms-scale
  bottlenecks but a gratuitous violation of the "exact blocked interval" contract.
  Strings round-trip exactly (`internal/cloud/trace.go:370-371`). (Span
  *start/end* are unaffected — Cloud returns them as typed `time.Time`,
  `internal/cloud/trace.go:88-89` — only attribute *values* hit the float64 path.)
- *Absolute* (not "trace-relative") because the engine emits the wait link at
  runtime from `c.wait`, lazy eval, or service-start code (`dagql/cache.go:3867`,
  `:2935-2943`, `core/services.go:955`) — where the only timing available is local
  wall-clock. The loader's op epoch is *trace-min-start*, which is knowable only
  *after* all spans are ingested. So "trace-relative-to-min-start" is not an
  emittable wire format. The loader **rebases** the absolute wait nanos to
  trace-min-start exactly as it already rebases span start/end (§5 step 2),
  keeping waits and op intervals on one timeline.

Why span links and not child "wait spans" or events:

- Links already carry a `SpanContext` (target id) and an attribute set, which is
  exactly a typed edge; they don't perturb the timing tree the way an extra child
  span would.
- **Link-cap safety (engineered, not assumed).** Per-waiter is far better than
  per-target, but suppressed-caller fan-in onto one ancestor (§3.1) means a single
  span *can* accrue many wait links. We therefore set an explicit high
  `LinkCountLimit = 16384` on the engine tracer provider (the default 128 evicts
  *oldest* links on overflow — i.e. silently drops the *earliest* waits, causing
  under-serialization, the exact failure this effort exists to prevent). 16384 is
  chosen to exceed any realistic count of *concurrent suppressed siblings under a
  single span* (dagql fans selections out one goroutine each, `dagql/server.go:1144-1163`;
  realistic parallel-identical fan-out is dozens–low-thousands), while bounding
  worst-case per-span link memory (a link is a `SpanContext`+attrs ≈ tens of
  bytes; 16384 links ≈ low-single-digit MB, and only the rare high-fan-in span
  ever approaches it — most spans hold 0–2 links). **Documented limit:** a
  pathological fan-out beyond 16384 concurrent suppressed siblings on *one* span
  would still evict; §6.5 stress-tests near that bound and §6.6 round-trips it
  through Cloud. (An emit-side mitigation — coalescing same-waiter→same-target waits —
  is recorded as a §9 seam should the limit ever bind; it is replay-exact only under
  the interval preconditions noted there, which the common concurrent fan-in
  satisfies.)
- Events are an alternative but are lossy under the per-span event cap and are
  awkward to give a target id.

The loader maps each `purpose=wait` link to a `wcprof.DumpEvent{Type:"wait"}`
with `ParentID`=waiter op, `TargetID`=target op (resolved by span id), `Reason`,
`StartNS`/`EndNS` (rebased absolute nanos; `engine/wcprof/dump.go:41-60`). This is
the *only* new vocabulary the OTel path introduces; everything else is existing
spans/attrs.

#### 3.0.1 Target-publication ordering (the rule that makes wait links reliable)

A wait link needs a concrete target `SpanContext` *at the moment the waiter emits
it*. Every shared-work choke point in the engine has the same shape — a target is
created, then a synchronization primitive (a `waitCh`, a `starting` map entry)
is published, then waiters observe that primitive and block. Native already gets
this right: it creates the target wcprof op and stores its id **under the same
lock, before** the primitive becomes visible — `oc.profOpID` for `call_exec`
(`dagql/cache.go:3673`,`:3693`, before the `ongoingCalls` publish at `:3697` and
unlock at `:3715`), `shared.lazyEvalProfOpID` for lazy (`dagql/cache.go:2960`,
before `lazyEvalWaitCh` is set at `:2962` and unlock at `:2966`), and
`start.profOpID` for services (`core/services.go:983`, before `ss.starting[key]`
at `:985`). So a native joiner that observes the primitive can always read a
valid target id.

> **Invariant T (target before primitive):** the OTel augmentation must create
> the target span (or at least mint and stash its `SpanContext`) **under the same
> lock and before** the waiter-observable primitive is published, mirroring
> native's op-id ordering. The target context is stashed on the shared state
> (`oc`, `shared` lazy state, `startingService`) and the goroutine that runs the
> work adopts that pre-minted span rather than starting its own.

This is not optional polish. The lazy choke point is the trap: the resume span is
created **inside the eval goroutine, after** `lazyEvalWaitCh` is published
(`dagql/cache.go:2968`,`:2984`), so a joiner at `dagql/cache.go:2935-2943` can run
before that span exists. Without Invariant T the joiner would have no target —
forcing the loader to either drop the edge (under-serialization) or synthesize one
later (anti-inference violation). Both are unacceptable. The fix (applied per choke
point below) is to mint the target span context up-front under the lock.

**When the target is still invalid, make it *observable*, never silently dropped.**
Invariant T guarantees a valid target only under *uniform* recording — the executor
and every caller record together, so a recording joiner's `oc.execSpanCtx` is
always valid. The one way it can be invalid is a **non-uniform / cross-session**
trace: a recording caller joining a singleflight execution started by an *untraced*
session (`ongoingCalls` is keyed by call+concurrency, **not** session, so a
recorded caller can join an unrecorded in-flight execution). In that case the emit
must **not** drop the wait edge — a never-emitted wait is exactly the
under-serialization §6.1 exists to catch. Instead the emitter attaches the wait
link with a **zero target** but full `wcprof.wait.*` attributes (the SDK retains an
attributed link), so the loader resolves no target, counts an `UnresolvedWaitTarget`,
and the §6.1 gate **fails loud** — precisely mirroring native, whose targetless
`wcprof.BeginWait(profOpID=0)` the gate also reports as unresolved. Such a trace
mixes recorded and unrecorded in-flight work and cannot be faithfully analyzed
anyway, so failing loud is the correct outcome, not a regression. (Implemented in
Chunk 2, `dagql/otelprof_hooks.go` `emitOTelWait`; gated only on the *waiter*
being recording — a non-recording waiter has no op to attach to and is a no-op.)

**Per-attempt reset keeps the target honest across retries (lazy).** A failed lazy
evaluation is retryable (`lazyEvalComplete` is set only on success), and joiners
read the wait-target fields whenever `lazyEvalWaitCh` is set. So the target fields
are **reset to invalid per attempt** — under `lazyMu`, before `lazyEvalWaitCh` is
(re)published, and overwritten only when *that* attempt actually mints — and
cleared again when `lazyEvalWaitCh` is cleared on completion. Without this, a retry
leader whose telemetry is *off* would leave a prior recording attempt's stale
target in place, and a joiner would **silently mis-link** to a dead op from the old
attempt instead of falling back to the gate-observable targetless wait (the
mixed-recording rule above). This applies to **both** the OTel field
(`shared.lazyEvalSpanCtx`) **and** native's `shared.lazyEvalProfOpID` — the native
field had the same stale-retry hazard and is reset alongside it, both to honor this
"targetless ⇒ gate-observable" model and to keep the two oracle sources aligned
under non-uniform recording. (Convergence pass `107ebe5c0c`; touches the native
recorder, owner-approved — code honoring existing design intent, not a change of
it.)

#### 3.0.2 Causal-parent override (`wcprof.parent`) — lazy-only, explicit, never inferred

One choke point (lazy eval, §2.5/§3.2) deliberately sets a span's `parentId` to a
span that is **not** its causal parent, because the UI must keep rendering
deferred work under the call that produced it. We cannot change that rendering
(it is an intentional dagui design decision). So for exactly those spans the
engine emits a second, explicit edge:

> **`wcprof.parent` convention:** a re-pointed span carries
> `wcprof.parent = <causal-parent span id>` (the consumer-side `lazy` op), encoded
> as the **lower-hex OTel span-id string** (the same 16-char form spans/links use
> on the wire) so the stamping processor and loader cannot diverge on encoding.
> `parentId` stays the UI parent (the producer); `wcprof.parent` is the causal
> parent. The loader's causal parent is `wcprof.parent ?? parentId` (§5). The
> engine knows both parents and emits the causal one — **this is emit, not
> inference**: the loader only reads the attribute, never derives it.

Three guardrails keep this from becoming a back-door to heuristic reparenting:

1. **Lazy-only.** `resumedCallbackSpan` (`dagql/cache.go:2990`) is the *sole* place
   in the engine that points a span's `parentId` at a different, already-existing
   span (verified: it is the only `SpanContext()`-overriding parent wrapper; the
   other `ContextWithSpanContext` calls re-anchor to *self* or to the legitimate
   exec cause). Every other choke point's `parentId` already *is* causal, so no
   override is emitted there and the loader will never see one.
2. **Stamp only the *direct* re-pointed spans.** Their descendants must keep
   nesting via normal `parentId` (so the deferred work's internal structure
   survives). The mechanism is a span processor whose `OnStart(ctx, span)` stamps
   `wcprof.parent` **iff** the ctx carries the lazy override **and**
   `span.Parent().SpanID() == <producer span id>`. That parent-id test is the
   crucial discriminator: only the callback's *direct* children have the producer
   as parent; descendants have their real parent and fall through unstamped. (A
   naive "stamp every span created under the lazy context" would over-stamp
   descendants — the override ctx value is inherited — and flatten the work subtree
   under the `lazy` op, losing structure. The parent-id check is what prevents
   that.) `OnStart` can read the ctx and `SetAttributes` on a `ReadWriteSpan`
   (`otel-go live.go:25`). **Concretely:** register this processor in the
   per-client `tracerOpts` where the engine builds each client's provider
   (`engine/server/session.go:684-715`), **before** the `LiveSpanProcessor`
   (`:686`) so the stamp is present even in the live-start snapshot
   (`LiveSpanProcessor.OnStart` exports immediately, `otel-go live.go:25-30`);
   correctness does not depend on the ordering — the *ended* span always carries
   the attribute and the loader uses the ended copy (§5 step 1) — but ordering
   gives live consumers the attribute too. See §3.2 for the emit-side wiring.
3. **Bounded to one attribute the loader trusts blindly.** Because emission is
   guarded by (1)+(2), `wcprof.parent` only ever appears on genuine lazy-work
   direct children, so the loader can apply `wcprof.parent ?? parentId`
   unconditionally without any matching logic of its own.

> Note: this `wait` link purpose is **distinct** from the existing `cause`
> linkage (`otel-go attrs.go:94-98`) and from `dag.inputs`
> (`core/telemetry.go:119-127`). Cause links are failure causality; `dag.inputs`
> are cache-key edges for the future cache-diff sibling. The loader **ignores
> both** for the runtime wait graph (brief §3, "two graphs, one IR"). We only add
> a `wcprof.inputs.*` seam if/when that sibling is built.

### 3.1 dagql call + cache singleflight (the central fix)

This single fix resolves Breaks #1–#3. We give the **shared execution** a
first-class OTel span that is independent of AroundFunc suppression, and we make
every caller emit a wait edge to it.

**Emit a `call_exec` span for the resolver run**, mirroring native's `execOp`.
Start it where native starts `execOp` — on the call's detached context just
before the goroutine spawns (`dagql/cache.go:3669-3677`) — and run the resolver
`fn` under it. Concretely the augmentation lives at the same spot as the existing
wcprof hook (`dagql/cache.go:3700-3713`):

- The `call_exec` span's parent is the call's context; the resolver's sub-call
  spans nest under it. Because this span is created in the cache layer regardless
  of whether AroundFunc emitted a caller span, **the resolver's children always
  nest under `call_exec`** — fixing Break #3 (emitter≠executor) structurally.
- **Per Invariant T (§3.0.1)**, the `call_exec` span (or its `SpanContext`) is
  minted and stashed on `oc` **under `callsMu`, before** the `ongoingCalls`
  publish (`dagql/cache.go:3697`) and unlock (`:3715`) — exactly where native
  stores `oc.profOpID` (`:3693`). The goroutine that runs `fn`
  (`:3700-3713`) adopts that pre-minted span. This guarantees every joiner that
  observes `oc` has a valid wait target.
- Attributes: reuse `dag.digest` (= `callKey`, `dagql/cache.go:3622`) as the
  stable identity, and mark it `dagger.io/ui.passthrough` so the UI continues to
  show the caller span, not this internal one. Add `wcprof.op.kind = "call_exec"`
  so the loader classifies it without guessing. **Do *not* emit `dag.call`** on the
  `call_exec` span: it is passthrough (no UI consumer for it; the visible caller
  span already carries `dag.call`), the loader/oracle read only the span name +
  `dag.digest`, and re-deriving it would cost a second `req.ResultCall.CallPB(ctx)`
  + `Encode()` per cache miss on the hot path for no consumer. `dag.digest` alone is
  the right payload (verified building Chunk 2).
- Outcome: carry `dagger.io/dag.cached` only on the *caller* span as today; the
  `call_exec` span by definition executed.

**Emit a `dagql.publishResult` span as a child of `call_exec` — for native
oracle-parity, not for counterfactual attribution.** After `fn` completes, the
cache runs `initCompletedResult` (publication/indexing/dependency-attachment)
inside `oc.initCompletedResultOnce` (`dagql/cache.go:3922-3947`); native records a
separate `OpKindInternal` `dagql.publishResult` op parented under the shared
execution via `wcprof.ContextWithOpID(ctx, oc.profOpID)` (`:3926-3934`). We mirror
that exactly: bracket `initCompletedResult` (`:3936`) with an OTel
`dagql.publishResult` span whose parent is the (already-ended) `call_exec`
`SpanContext` stashed on `oc`.

Be precise about what this does and does **not** buy, because it is easy to
overclaim. Publication runs *after* `call_exec` ends (`:3700-3706`) and *after*
the caller wait edge closes (`oc.waitCh`, `:3875-3881`). So:

- In the replay, `call_exec` never *joins* `publishResult`: the implicit join only
  absorbs children whose end is `≤` the parent's own end (`replay.go:353-369,389`),
  and this child ends later. Nothing else waits on it either. So scaling the
  `publishResult` class saves ~0 makespan — the counterfactual does **not** credit
  it. The publication interval is instead absorbed into the **caller/ancestor**
  self-time (the caller's interval runs through publication, and `publishResult` is
  a *grand*child not subtracted by `SelfSegments`, `graph.go:379-398`). So
  publication is counterfactually charged to the caller class, in **both** native
  and OTel.
- The reason to emit it anyway is **oracle parity**: native produces this exact
  shape (a `dagql.publishResult` self-time row *plus* the caller-self absorption),
  so for the cross-source oracle's per-class table to match, OTel must produce it
  too. Emitting it keeps native and OTel in agreement; omitting it would make the
  tables diverge.
- If publication ever proves *hot* on real traces, the right fix is to model it as
  a genuine wait target of the still-blocked caller **in both native and OTel**
  (so the oracle stays meaningful) — recorded as a §9 seam, explicitly parallel to
  the §4.1 persisted-decode seam. We do **not** claim the current late-child shape
  fixes class attribution.

The per-caller result normalization that follows (`ensurePersistedHitValueLoaded`,
`:4009`) gets **no** native op, so folding it into caller self-time already
matches native — no span needed there.

**Each caller emits a `wait` link to the `call_exec` span**, with the blocked
interval from `c.wait` (`dagql/cache.go:3853-3880`) and reason `singleflight`
(joiner) or `call_exec` (executor). This is the OTel analog of native's
`wcprof.BeginWait(ctx, oc.profOpID, …)` (`dagql/cache.go:3867-3874`). The waiter
is the caller's current span; if the caller was telemetry-suppressed, the
"waiter" is whatever span is current in its context (its parent) — which is the
*correct* place for the time to land, because that ancestor is the op that
actually blocked.

The **load-bearing** edges are the *joiners'*: the `call_exec` span is created in
the executor's context, so it nests under the executor's span (or, if the
executor was suppressed, under the executor's parent — also correct, since that
ancestor is synchronously blocked in the resolver). That structural nesting
already makes the implicit join wait for `call_exec` on the executor side, so the
executor's explicit edge is redundant-but-harmless (both reduce to
`clock = max(clock, finish(call_exec))`, idempotent — `replay.go:379-381`). The
*joiners* are in a different subtree (the execution is not nested under them), so
their wait edge is the **only** thing connecting them to the execution — without
it, Breaks #1–#2 stand. Emitting from every caller uniformly (matching native)
is simplest; the joiner edge is the one that must never be dropped.

A subtle but important placement consequence: because suppressed callers never
enter `core.AroundFunc`, the wait edge **must** be emitted from the cache layer
(`c.wait`), which always runs — not from AroundFunc. The waiter is resolved from
the current span in `ctx` via `span.AddLink` on the live caller/ancestor span (an
established pattern in this codebase — see the service origin links at
`core/services.go:230-237`).

This placement is also the source of the link-cap concentration risk: when a
caller is suppressed, its wait link lands on the ancestor span, and one ancestor
can fan out to many concurrent suppressed siblings (`dagql/server.go:1144-1163`),
so a single span can accrue many wait links. That is why the cap is engineered
(`LinkCountLimit = 16384`) rather than assumed tiny — see §3.0 "Link-cap safety"
and the §6.5 cap-stress / §6.6 round-trip fixtures.

Result, expressed in the IR:

- `call_exec` → one `op` event (kind `call_exec`), children = resolver sub-calls
  + the `dagql.publishResult` op, self-time = real resolver self-work. ✔
- `dagql.publishResult` → one `op` event (kind `internal`) under `call_exec`, a
  **native-parity diagnostic** (its self-time matches native's row); publication
  itself is counterfactually charged to the caller class in both native and OTel,
  not to this op (see above). ✔ (oracle parity, not an attribution fix.)
- each caller → a `wait` event to `call_exec` over the true blocked interval, so
  self-time on the caller/ancestor is correctly reduced and the counterfactual
  propagates execution cost to every caller's critical path. ✔ (Fixes #1, #2.)

**We do *not* need to force a caller `call` span for every caller** (that would
re-introduce the volume `ShouldEmitTelemetry` exists to avoid). The executor's
caller span already exists unless suppressed, and the wait edge — emitted from
`c.wait` against the current span — lands on the right op whether or not that
caller emitted its own span. This mirrors how native reads the waiter from
context in `wcprof.BeginWait` (`wcprof/record.go:222-235`) and keeps the OTel
source's extra volume to "executions + the links of whoever blocked."

Volume: two extra spans per *executed* call (the `call_exec` and its
`publishResult` child — cache misses, the expensive ones we want to see anyway)
plus one tiny link per caller that blocked. We do **not** emit anything new for
cache hits (§4.1). This respects the volume constraint while restoring
faithfulness.

**Always-on posture (a deliberate, signed-off decision).** This emit is gated on
telemetry being active (a recording span in `ctx`), **not** on `wcprof.Enabled` or
a profiling flag — because the north star is analyzing *any* Cloud trace after the
fact, and you cannot retroactively enable these spans during the slow run that
already happened. So the cost lands on every already-traced run: **+2 passthrough
spans per cache MISS + one wait link per blocked caller; cache HITS emit nothing;
untraced runs emit nothing.** The owner has explicitly signed off on this, and
**declined a kill-switch** (keep-it-simple) — recorded here so it is not
re-litigated as an accidental posture.

### 3.2 Lazy / deferred evaluation

Goal, restated for the hard constraint: **align the analyzer with native's model
(deferred work is its own op under the consumer, others wait on it) while keeping
the UI-visible structure unchanged** — the deferred work renders under the same
visible parent (the producer), with the same nesting/name/passthrough as today.
(Not *byte-for-byte*: minting the `lazy` op earlier shifts its start timestamp,
and the no-producer-context case gains one *hidden* passthrough span — see step 1.
What is guaranteed is that dagui's visible tree is unchanged.) The producer-side
rendering of lazy work is an intentional dagui design choice and is out of scope
to change. We achieve this by separating *UI parentage* (`parentId`, untouched)
from *causal parentage* (`wcprof.parent`, §3.0.2).

There are two problems to fix at this choke point — a timing problem (the wait
target doesn't exist when joiners need it) and a parentage problem (the work is
rendered under the producer, which the analyzer must not read as causality). The
fix handles both **without re-parenting any existing span** (no span's `parentId`
changes; the work stays rendered exactly where it is today):

**1. Mint the `lazy` op under `lazyMu`, before `lazyEvalWaitCh` is published**
(Invariant T, §3.0.1) — at `dagql/cache.go:2957`, where native sets
`shared.lazyEvalProfOpID` — **whenever telemetry is on**, so a joiner always has a
wait target. This is the consumer-side `lazy` op: parent = the triggering consumer
(`evalCtx` ⊂ `stackCtx`, `:2947`), a genuine synchronous nesting (the consumer
blocks in `waitForLazyEvaluation`); `wcprof.op.kind = "lazy"`; marked
`ui.passthrough`. Stash its `SpanContext` on the shared lazy state next to
`lazyEvalProfOpID`; the goroutine adopts it to run + end the callback. Two
existence cases, made explicit (this is where "byte-for-byte" breaks and why it
doesn't matter visually) — and they differ in the op's **class**:

  - **Producer-context captured (common).** The resume span already exists today
    (`:2984`) as a passthrough child of the consumer linking back to the producer.
    The `lazy` op **is that resume span**, just minted earlier (under the lock) —
    same parent/name/links/passthrough; only its start timestamp shifts marginally
    earlier (it now also covers the lock acquisition). The deferred work still
    renders under the producer via `parentId` (step 2). No new visible node.
    *Class divergence:* the op keeps the UI-load-bearing span name `"resume <field>"`
    as its class, vs native's `profCallClass`. This is **benign because the lazy
    op's self-time is eval-overhead-small — the head/tail gaps around the deferred
    sub-work — so it never reaches the top-N the product ranks** (not literally
    "self-time ~0": it is small-but-nonzero, so a class-label miss *would* surface
    in a full-ranking, non-top-N comparison; the oracle's `minSelf` filter makes it
    invisible there too). Fixing it would need the forbidden UI-name change; the
    clean future fix if exact full-ranking parity is ever wanted is to emit
    `profCallClass` as a separate `wcprof.class` attribute the loader prefers over
    the span name for classification.
  - **No producer context (suppressed/untraced producer).** Today no resume span
    is created (`:2971-2995` guards on a captured producer ctx) and the work nests
    under the consumer. We now mint a `lazy` op here too — a **new, hidden
    (`ui.passthrough`) span** under the consumer, named by `profCallClass` (no
    UI-facing span to preserve, so its class **matches native**) — so joiners have a
    target and the work nests under it. Because it is passthrough, dagui still shows
    the work under the consumer (the added node is elided): the visible tree is
    unchanged, though the trace contains one extra hidden span. There is no producer
    to re-point to, so `resumedCallbackSpan` and the `wcprof.parent` override are not
    used in this case — the work nests under the `lazy` op by ordinary `parentId`.

**2. Keep `resumedCallbackSpan` exactly as today** (`dagql/cache.go:2990`) so the
deferred work's spans keep `parentId = producer` and **dagui renders unchanged**.

**3. Stamp the causal override.** On the callback context, carry the override
`{lazyOpSpanID, producerSpanID}` (producerSpanID = `originalSpanCtx.SpanID()`).
A span processor stamps `wcprof.parent = lazyOpSpanID` on the **direct** re-pointed
work spans — those whose `Parent().SpanID() == producerSpanID` (§3.0.2). Their
descendants (including a lazy-triggered `withExec` and its in-container /
nested-client work, which nest via normal `parentId` and the exec traceparent,
§2.6) follow their stamped ancestor without needing their own override.

**4. Joining consumers** (`dagql/cache.go:2935-2943`) emit a `wait` link (reason
`lazy`) to the stashed `lazy` op `SpanContext` — now always available, satisfying
Invariant T (native: `:2940`). The **leader** (the triggering consumer that mints
the op) also emits a `wait` link to its own `lazy` op, mirroring native's leader
wait and Chunk 2's executor wait (§3.1): it is redundant-but-harmless — the lazy op
already nests under the leader, so the implicit join serializes it, and the wait/op
intervals overlap so union-subtraction credits no extra self-time (idempotent
`max`). Emitted uniformly for native oracle-parity.

What the analyzer then sees (via `wcprof.parent ?? parentId`, §5): the work nests
under the `lazy` op (under the consumer) — **not** under the producer. So:

- **No double-count.** The work is the `lazy` op's analyzer-children, so its
  self-time counts once under its real classes (exec, call, …); the `lazy` op's
  own self-time is ~0 (just eval overhead). The producer's analyzer-children do
  **not** include the work, so the producer is not also charged for it. (This is
  exactly the failure the naive Option B had — work staying under the producer
  while an empty `lazy` op inflates to the full eval duration — and the override
  is what avoids it.)
- **Correct per-class attribution.** Because the work sits under the `lazy` op,
  scaling the work's *real* class (e.g. the exec it ran) shortens the `lazy` op's
  finish, which shortens every consumer's wait. A wait edge to an *empty* `lazy`
  op would propagate makespan but mis-credit the generic "lazy" class instead of
  the exec — so the override is necessary, not just the wait edge.
- **No cycle.** The work is never causally parented to the already-ended producer;
  the only producer association is the existing non-causal UI link.
- **UI-visible structure unchanged.** `parentId` is untouched and the `lazy` op is
  passthrough, so dagui renders the work exactly where it does today — under the
  producer (producer-context case) or under the consumer (no-producer case, where
  the added `lazy` node is elided). The only non-visible deltas are the marginal
  start-timestamp shift and, in the no-producer case, one extra hidden span (step
  1). A span created on one goroutine and ended on another is fine.

### 3.3 Container exec / nested clients

Parentage is free (§2.6 — nested-client spans nest under the `withExec` span via
traceparent). But the **engine-vs-user split is required, not optional**, and is
the single highest-value addition for the user-facing goal ("a slow `go build` is
a valid headline answer").

**Emit the `exec.containerStart` (engine) vs `exec.processRun` (user) split.**
The whole `withExec` span is **not** "approximately user process time," and
marking it `work_type=user` would mislabel engine overhead as the user's slow
command. Native deliberately splits these because the engine overhead is *not*
sub-millisecond — the original wcprof headline was exactly a serial container-
setup tax (`engine/wcprof/README.md:1-7` "a 300ms serial tax before every
container start"). Concretely, native records:

- `exec.containerStart` over `[run start, process started]` (engine) and
  `exec.processRun` over `[process started, run end]` with `WorkTypeUser` **only**
  on the process interval, split at the started-callback
  (`engine/engineutil/executor_spec.go:1407-1412`);
- per-setup-phase ops `exec.<phase>` (setupNetwork, setupRootfs, …,
  `engine/engineutil/executor.go:188-203`);
- `withExec.prepareMounts` (`core/container_exec.go:1741-1742`) and
  `withExec.applyOutputs` (`core/container_exec.go:2175-2182`) for mount-prep and
  output-commit.

The OTel executor path today is pure propagation/forwarding with **no**
`Tracer.Start` phase spans (`engine/engineutil/executor_spec.go:745-840`). So
this is genuinely new emission. **v1 requirement:** emit the `containerStart` vs
`processRun` split at the started-callback boundary, with `wcprof.work_type =
"user"` on `processRun` only, as genuine synchronous children (the exec is blocked
through both, so Invariant E holds).

**Parentage, specified.** Emit an `exec.run` span (`wcprof.op.kind = "exec"`,
class `exec.run`) mirroring native's `OpKindExec` op (`engine/engineutil/executor.go:122`),
as a child of the **`call_exec` span** — the executor runs inside the `withExec`
resolver `fn`, which (per §3.1) runs under `call_exec`. `containerStart` and
`processRun` are children of `exec.run`. This matches native's op shape
(`exec.run` → phases) so the cross-source oracle's class table lines up. Note this
makes nested-client work (which nests under the `withExec` execution span via the
existing `causeCtx` propagation, `core/container_exec.go:1304`, captured *before*
`exec.run` exists) a **sibling** of `exec.run` under `call_exec` rather than a
child of `exec.run` — which is fine for the counterfactual (`call_exec`'s implicit
join waits for both). The finer phases (setupNetwork, prepareMounts, applyOutputs)
are an **additive follow-up**, emitted as children of `exec.run` exactly as native
nests them (`engine/engineutil/executor.go:188-203`), added when the dead-air
report shows they matter, not because they are guessed to.

External-I/O tagging (`work_type = "external"` on git/pull/filesync) is part of
the leaf-I/O seam (§3.5), out of scope for v1.

### 3.4 Services

The start is already faithful via the existing `serviceSpan` + origin links, but
we must ensure the **idle availability** lifetime is not mistaken for blocking
work:

- Treat `service.start` as the op: scope the *self-time-bearing* portion of the
  service span to the start + health-check window (native's `service.start` op,
  `core/services.go:974`), and mark the idle remainder so it contributes no
  self-time. The simplest honest emit: emit a dedicated `service.start` span
  bracketing `svc.Start` (`core/services.go:990`) with `wcprof.op.kind =
  "service_start"`, and leave the long-lived service span as a
  non-self-time-bearing availability marker (`ui.passthrough` already keeps it
  out of the way).
- **Per Invariant T (§3.0.1):** mint the `service.start` span (or its
  `SpanContext`) and stash it on the `startingService` **before** publishing
  `ss.starting[key]` and releasing `ss.l` (`core/services.go:978-986`) — exactly
  where native stores `start.profOpID` (`:983`). Then an installer that arrives in
  the `isStarting` branch (`:951-963`) always has a valid wait target.
- Each installer that blocks on start emits a `wait` link (reason `service`) to
  the `service.start` span — the analog of `core/services.go:955`. Reuse the
  existing origin-link plumbing (`core/services.go:230-237`) but with
  `purpose=wait` and the blocked interval, so it lands in the runtime wait graph
  rather than only the UI cause graph.
- A service that is bound but never blocked on (already running) yields no wait
  edge — correct: it wasn't on anyone's critical path.

### 3.5 Session root and leaf I/O (scope boundaries)

- **Session root.** The per-query span `POST /query`
  (`engine/server/session.go:1420`, `ui.passthrough`) is the natural **root op**
  per query; the dagql call tree nests under it. The loader treats it as a root
  (or as the chain anchor for sequential queries, matching `replay.go:265-297`).
  The wcprof session *phases* (`session.workspaceLoad` etc.,
  `engine/server/session.go:1478-1513`) have no OTel spans; they would show as
  root self-time / dead-air. Optional: emit phase spans later if that dead-air
  proves interesting (it's the same "where to add hooks next" guidance the README
  gives for native).
- **Leaf I/O (git fetch, image pull, filesync).** Not instrumented in *native*
  either (README "Status / caveats"); they surface as self-time of the calling
  op. OTel already emits `pulling …` spans and progress records
  (`engine/telemetryattrs/attrs.go:13-35`, telemetry-capture skill) and has an
  `effect.ids`/`effects.completed` convention for cached buildkit effects
  (`otel-go attrs.go:107-123`). This is a rich **seam** for a later pass: map
  pull/git/filesync spans to `OpKindIO` with `WorkType=external`. Out of scope
  now; leave the seam, don't infer.

---

## 4. What we deliberately do NOT emit

Keeping volume sane is a first-class constraint (brief §3, §5). The faithfulness
argument for each omission:

### 4.1 Cache hits

Native records a `call` op even for hits "to deliberately record what OTel
suppresses" (README) — but that is for the *cache-diff* sibling, not the
counterfactual. For wall-clock bottleneck ranking an *in-memory* hit contributes
**negligible self-time** (the result already exists) and **no wait edge**, so
omitting it (which OTel already does via `ShouldEmitTelemetry`,
`dagql/telemetry.go:57`) is *correct* for our model. Two clarifications matter,
because "hit" is **not** always "zero runtime work":

1. **First-occurrence persisted/imported hits ARE captured.** A cold hit served
   from persisted/imported cache can block on payload decode and dependency
   attachment (`dagql/cache.go:3632-3639` → `ensurePersistedHitValueLoaded` →
   `dagql/cache_persistence_import.go:563-700`) — exactly the post-restart CI
   scenario. But the *first* occurrence of a digest is **not** suppressed
   (`ShouldEmitTelemetry` returns true for unseen keys), so it gets a call span,
   and that span wraps the whole `GetOrInitCall` including the decode — so the
   decode time lands in the span's self-time. The loader maps **all** emitted
   spans; it does **not** categorically drop cached spans. So the expensive cold
   decodes are already captured.

2. **The residual is a bounded native/OTel attribution gap, not "harmless."** Only
   *repeated* digests are suppressed, and a repeat is almost always a cheap
   in-memory hit (the first decode populated the in-memory result). The one case
   where a suppressed repeat still does real work is a concurrent second caller
   that blocks on an *in-flight* persisted decode or dependency attachment
   (`persistDecodeWaitCh`, `cache_persistence_import.go:598-620`; `attachDepsWaitCh`,
   `:563-578`). Neither native nor OTel models this as a wait edge (there is no
   `wcprof.BeginWait` in `ensurePersistedHitValueLoaded`) — but they **attribute it
   to different ops**: native records a `call` op even for the repeated hit
   (`dagql/cache.go:3515`), so the time lands in *that op's* self-time/class; OTel
   suppresses the repeat, so the time folds into the *visible ancestor's* self-time.
   Because the cross-source oracle *is* native top-N `RunWhatIfs` (§6.2), this is a
   real ranking divergence if the path is hot, **not** mere cosmetic drift. The
   §6.5 persisted-cache fixture is the gate: if it shows non-trivial top-N drift,
   the fix is a real decode target + wait edges added to **both** native and OTel
   (so the oracle stays meaningful), tracked as the §9 seam — a native gap to close
   first, not an OTel-only one.

The only *always-relevant* non-hit case is a **pending hit** — a cache hit whose
result still needs lazy evaluation; that is handled by the lazy path (§3.2), and
`recordStatus` already distinguishes it (`core/telemetry.go:259`, only sets
`dag.cached` when `!HasPendingLazyEvaluation`).

### 4.2 Executor phases, introspection, meta, internal

- Executor setup phases: §3.3.
- Introspection / `node` / `id` / `sync` / `Error` spans are already skipped
  (`core/telemetry.go:32-43, 454-480`) and should stay skipped — they are not
  user-meaningful work.
- `ui.internal` spans (`core/telemetry.go:129-131`) are kept (they can be on the
  critical path) but carry the attribute so analysis can group/segregate them.

---

## 5. The loader (mechanical, zero causal inference)

Input: a set of OTel spans for one trace (from the Dagger Cloud trace API — the
canonical ingest per brief §3; `otlpdump` JSONL is the dev-loop equivalent,
telemetry-capture skill). Output: `wcprof.DumpHeader` + `[]wcprof.DumpEvent`,
passed straight to `wcanalyze.Build`.

The loader does **only** mechanical translation. Each step below is a direct
field map with no heuristic causality, no node synthesis, no timestamp-containment
re-parenting:

1. **Dedup live spans.** With `OTEL_..._TRACES_LIVE=1` a span is exported on
   start and on end (telemetry-capture skill); key by span id and keep the ended
   copy. (Pure dedup, not inference.)
2. **Span → op.** One `DumpEvent{Type:"op"}` per span.
   - `OpID` = a dense id assigned from the span id (stable map).
   - `ParentID` = **causal parent** = `wcprof.parent` attribute (resolved to op)
     **if present, else** the span's `parentId` (the synchronous nesting; see
     Invariant E — by construction these are now all genuine). The `??` is the only
     parentage rule, and it is mechanical: the loader reads the emitted override or
     falls back to `parentId`; it never *derives* an override. `wcprof.parent` only
     appears on lazy-work direct children (§3.0.2/§3.2), so this fallback is a
     no-op everywhere else.
   - `Kind` = `wcprof.op.kind` attribute if present (`call_exec`, `lazy`,
     `service_start`, `io`), else inferred *structurally not causally* from
     span/attrs: a span with `dag.digest` and child `call_exec` ⇒ `call`; a
     `withExec` ⇒ `exec`. (Classification, not causal inference.)
   - `Class` = span name (`Type.Field`, `core/telemetry.go:51`).
   - `Ident` = `dag.digest`.
   - `ResultID` = `dag.output` if present (for the result-link seam).
   - `WorkType` = `wcprof.work_type` attribute (else engine).
   - `Outcome` = from `dag.cached` / `dag.canceled` / span status / `dag.pending`.
   - `StartNS`/`EndNS` = span start/end **rebased** to the trace epoch (trace-min
     start), matching the dump's relative-ns convention (`dump.go:160`). The epoch
     is computed once after all spans are read (the min span start).
3. **Wait links → wait events.** Each link with `link.purpose="wait"` →
   `DumpEvent{Type:"wait"}` (§3.0): `ParentID`=the span carrying the link (the
   waiter), `TargetID`=link target span id → op (or `Ident` for `lock`),
   `Reason`, `StartNS`/`EndNS` parsed from the `wcprof.wait.*_unix_ns` decimal
   strings (absolute Unix nanos — exact, no float64 path) and **rebased to the same
   trace epoch as op intervals** (step 2). Emitting absolute nanos is what makes
   this implementable: the engine cannot know the future trace-min-start at emit
   time, so it emits absolute and the loader rebases (§3.0).
4. **Build the string table** and emit the header (`dump.go:144-152`); call
   `wcanalyze.Build`.

What the loader explicitly does **not** do (brief §6.2, §3): it never invents an
op the trace didn't emit; never re-parents by timestamp containment; never turns
a `cause`/`error_origin` link, a `dag.inputs` entry, or span-time overlap into a
wait edge; never breaks cycles or massages over-serialization. The one
"reparent," `wcprof.parent ?? parentId`, is **not** loader inference — it reads an
edge the *engine* emitted (§3.0.2); the loader never decides for itself that a
span belongs elsewhere. If the loaded graph has a cycle or an op with self-time >
makespan, that is a **bug in the emit side** to be fixed there (brief §6.3), and
the validation in §6 makes it loud.

Nested-client stitching needs **no** loader logic (unlike native's
`LinkKindNestedClient` reparent, `graph.go:270-294`) because OTel already nests
nested-client spans under the exec via traceparent (§2.6). The
`nested_client`/`result` link handling in `Build` simply goes unused by this
source — fine, it's a no-op when no such links exist.

---

## 6. Validation plan (first-class, only on corrected traces)

Validation runs **only** on traces from the augmented engine (brief §1, §7) — a
trace from the un-augmented engine is garbage and must never be used to "tune"
anything. Four layers, loudest-first:

### 6.1 Structural invariants on the loaded graph (cheap, always-on gate)

Run after every `Build` in the OTel path, fail loudly:

- **No cycles / self-waits.** Reuse the replay's own signal: `Simulation` already
  counts `CycleWarnings` (`replay.go:341-345`, surfaced in `report.go:163-165`).
  The gate asserts `CycleWarnings == 0`. A cycle ⇒ unfaithful emit (§2.5), not a
  replay flaw.
- **No op self-time > makespan**, and **no single op interval > trace span**
  (catches a joiner mis-typed as self-time, §2.2, or a service-availability span
  leaking self-time, §3.4).
- **Bounded `FallbackAnchors`** (`replay.go:330-338`): fallback anchoring means
  an op's parent never reached its spawn — a sign of detached/re-pointed work that
  slipped Invariant E. Track the count as a regression metric.

The invariants above catch *over*-serialization and impossible structure.
Building the Chunk 1 gate revealed the enumerated set missed the opposite failure —
wait-edge **loss** (under-serialization): a lost wait silently degrades a join into
a fixed delay and drops counterfactual propagation to the target's class, exactly
the under-attribution this whole effort exists to prevent. Three more hard
invariants (implemented in Chunk 1, `engine/wcprof/wcotel/gate.go`, commit
`71b69f1f16`):

- **No unresolved non-lock wait target.** A `purpose=wait` link with reason ≠
  `lock` whose target span id doesn't resolve to an op fails the gate. This is the
  cross-session non-uniform-recording case (§3.0.1): the emitter deliberately emits
  such a wait *targetless* (rather than dropping it) so it is observable here — were
  it ungated, the replay would degrade the join to a fixed delay
  (`replay.go:170-173`) and silently lose the dependency. A trace that hits this
  mixes recorded and unrecorded in-flight work and can't be faithfully analyzed, so
  failing loud is correct.
- **No malformed wait timing.** A wait link with missing/unparseable
  `wcprof.wait.*_unix_ns` (§3.0) fails — the loader records it as a conservative
  zero-duration no-op, but a faithful augmented emit never produces one.
- **No dropped links on a wait-carrying trace.** *Keyed on whether the trace
  carries waits:* on an **augmented** trace (`WaitEdges > 0`) any dropped link or
  dropped link-attribute fails (at `LinkCountLimit = 16384` a drop can only be wait
  loss — an evicted edge or a stripped `link.purpose`/timing); on an
  **un-augmented** trace (`WaitEdges == 0`) drops are report-only, because a
  baseline captured from a stock 128-cap engine can legitimately have benign
  >128-link *non-wait* spans and a blanket fail-on-any-drop would false-positive
  them. The `WaitEdges > 0` key subsumes the narrower "drop on a span that kept a
  wait" predicate *and* catches a span that lost *all* its waits or a dropped
  `link.purpose` attribute (both invisible to that predicate). It is cleaner than a
  caller-set validation-vs-production mode flag: the Cloud path **cannot report
  dropped counts at all** (its trace query has no dropped-count field —
  `internal/cloud/trace.go` fragment `SpanProps`, links `:52-57`; `SpanLink`
  `:112-117`), so a "production-lenient" branch would be vacuous. On Cloud we
  instead engineer drops out — `LinkCountLimit = 16384` exceeds any realistic
  concurrent suppressed-sibling fan-out (§3.0/§3.1) — and verify survival with the
  §6.6 round-trip rather than a count we can't see.

**Documented residual (the gate's blind spot).** If *every* wait on *every* span
were dropped, `WaitEdges == 0` makes the trace indistinguishable from un-augmented,
so the structural gate cannot catch that pathological total-loss case. The §6.5
cap-stress and §6.6 Cloud round-trip fixtures — which assert that *specific*
injected waits survive — are the backstop for total loss.

### 6.2 Cross-source oracle (the strongest check)

Run a workload on a dev engine with **both** native wcprof (`--profile` /
`_DAGGER_WCPROF`, README) and the OTel augmentation active; compile both to the
IR; compare. Native is ground truth.

- **Identity-level:** for each `dag.digest`, native's `call_exec` self-time vs
  OTel's `call_exec` self-time should match within tolerance; wait edges should
  correspond (same waiter→target, overlapping intervals).
- **Ranking-level (the one that matters):** run `RunWhatIfs`
  (`replay.go:485`) on both and assert the top-N bottleneck classes and their
  `SavedNS` agree within tolerance. This is the real contract — the OTel source
  must produce the *same bottleneck ranking* as native.
- Drift between the two localizes the *next* faithfulness bug to a specific
  digest/choke point (use `DriftOrigins`, `replay.go:668`).

**Scope-match the two sources, or the comparison is apples-to-oranges.** The two
sources do **not** cover the same scope of work, and ignoring that produces a low
top-N jaccard that looks like unfaithfulness but is not (observed empirically at
Chunk 3: jaccard ≈ 0.23 while the matched-scope deterministic oracle is
jaccard=1.00/drift=0.00 and the empirical *overlap* drift is 0.00). Native
`_DAGGER_WCPROF` is engine-**global** (all sessions + core-schema construction);
the OTel source compiles **one Cloud trace** = the **client-session** scope, which
additionally contains client-infra spans native never sees (session start,
`connect`, trace export) and omits engine work that produced no client-trace span.
So a raw class comparison is disjoint *by construction*. To use the oracle (and the
§6.4 gate) as a clean validator:
  - run native with **`--profile <session>`** (per-session, README) scoped to the
    *same* session whose Cloud trace is compared — not engine-global; **and**
  - **class-filter** to the engine-resolver work both sources instrument
    (`call` / `call_exec` / `lazy` / `exec` / `service` / `session_phase`),
    excluding irreducibly OTel-only client-infra and native-only no-span work. The
    harness exposes `NativeOnly` / `OTelOnly` (`oracle.go`) so the excluded residual
    stays auditable, not hand-waved.
  - *Caveat:* cross-session lazy can make perfect `--profile` scoping impossible
    (native `--profile` records nested clients whose work spans multiple OTel
    traces), so **class-filtering is the ultimate equalizer**, not session scoping
    alone.
This is a *validation*-methodology requirement, **not** product drift: the Cloud
trace *is* the client-session scope, exactly what "why was my CI run slow?"
operates on — the oracle just needs like-for-like to validate it.

This oracle is the centerpiece. It is the only way to prove the augmentation is
faithful without hand-auditing traces.

### 6.3 Known-answer injection

On a controlled workload, inject a known delay (e.g. `sleep N` in one `withExec`)
and assert: (a) that class rises to the top of `RunWhatIfs` with `SavedNS ≈ N`;
(b) a *parallel*, off-critical-path `sleep` does **not** rank (proves the
counterfactual still distinguishes total-time from bottleneck through the OTel
edges). Add a singleflight fan-in case (N concurrent callers of one slow digest)
and assert all N callers' critical paths credit the shared execution — the direct
regression test for Breaks #1–#3.

### 6.4 Standing drift gate on a representative complex workload

Toy graphs hide over-serialization (brief §7). Pick a real, complex workload
(e.g. `engine-dev` build, or a module pipeline with services + lazy dirs +
nested clients) and stand up a CI check that asserts `simulated baseline drift vs
actual` stays within a band (`report.go:170-174` already computes it) and that
§6.1 invariants hold. This catches regressions where a future engine change
re-introduces an unfaithful nesting. **When this gate compares the OTel source
against native, it must scope-match per §6.2** (per-session `--profile` native +
class-filtering to the shared resolver classes) — otherwise the global-vs-trace
scope mismatch swamps the signal. The scope-matched empirical oracle is also the
closing proof for the Chunk 3 jaccard observation (§6.2).

### 6.5 Targeted adversarial fixtures (the regression suite)

Each fixture targets one break or one ordering hazard, and asserts the oracle
(§6.2) agrees with native and the §6.1 invariants hold:

- **Lazy re-point fidelity (the load-bearing fixture for §3.2).** Force a pending
  result produced by call `O`, then have several consumers trigger `Evaluate`
  concurrently. Assert, on the corrected trace:
  1. **UI parentage unchanged** — every deferred-work span still has
     `parentId == O` (the override must not move spans); a golden-trace diff
     against the pre-change span tree is the strongest form.
  2. **Causal re-home** — those same spans carry `wcprof.parent =` the `lazy` op,
     and *only* the direct children do (a descendant exec span carries none and
     nests via `parentId`), so the analyzer subtree roots at the `lazy` op with its
     internal structure intact.
  3. **No double-count** — total self-time over classes ≤ makespan; the producer
     `O`'s self-time excludes the deferred work; the `lazy` op's self-time is ~0.
  4. **Consumer critical path includes the eval** — `RunWhatIfs` scaling the
     work's *real* class (e.g. the exec it ran) reduces the triggering consumer's
     finish; scaling a generic "lazy" class does not (it has ~0 self-time).
  5. **Invariant T** — a joiner arriving before the goroutine would have created
     the span still gets a valid `lazy` target; no wait edge is dropped.
- **Many suppressed siblings under one parent — semantics *and* cap-stress.** One
  parent resolves many repeated/suppressed selections of the same digest
  concurrently (DagQL resolves siblings in parallel, `server.go:1144-1163`). Two
  jobs: (a) *semantics* — assert the parent-attached waits model fan-in, not
  serialization: replay takes `max`, not a sum (`replay.go:379-381`), and self-time
  subtracts the *union* of wait intervals (`graph.go:379-398`); catches bad
  timestamp placement or accidental `actWaitFixed` classification. (b) *cap-stress*
  — push the concurrent suppressed-sibling count high (into the thousands, beyond
  realistic max but under the 16384 cap) and assert, on the otlpdump path where
  `DroppedLinksCount` is visible, that **no wait links were dropped** and every
  caller's wait survives. This is the regression guard for the §3.0 cap choice.
- **Persisted-cache import/decode — with a ranking-drift gate.** Run after an
  engine restart with imported cache so first-occurrence hits decode persisted
  payloads (`cache_persistence_import.go:563-700`). Assert (a) the cold
  first-occurrence decode shows up as call-span self-time (§4.1); and (b) on a
  *repeated/concurrent* suppressed-decode workload, the native↔OTel top-N
  `RunWhatIfs` rankings agree within tolerance. If (b) drifts non-trivially, that
  is the documented native/OTel attribution gap (§4.1) crossing into materiality —
  the trigger to add a real decode wait target in **both** sources (§9 seam), not
  to ship around it.
- **`withExec` with delayed setup vs runtime.** A `withExec` whose container
  *setup* is slow independently of a fast user process (and vice-versa). Asserts
  the §3.3 split charges engine overhead to `containerStart` and user time to
  `processRun` (work_type=user), and that the headline correctly fingers whichever
  is slow.
- **Singleflight fan-in** (from §6.3) and **emitter≠executor race** (drive
  concurrent duplicate digests under load): assert all callers credit the shared
  `call_exec` and no resolver children mis-parent.

### 6.6 Cloud round-trip test (production ingest)

Because the Cloud path cannot self-report dropped links (§6.1), prove durability
end-to-end: emit a known augmented trace, send it through the real Dagger Cloud
ingest, fetch it back via the trace API (`internal/cloud/trace.go`), compile, and
assert (a) every `purpose=wait` link survived with its target id and
`wcprof.wait.*_unix_ns` string attributes intact, parseable, and **bit-exact**
(this is the gate proving strings dodged the float64 coercion); (b) the compiled
graph is byte-identical (modulo ordering) to the one compiled from the local
`otlpdump` capture of the same run; and (c) §6.1 invariants hold.

**Crucially, size the fan-in to exceed realistic maximum, not a toy.** A small
known trace can pass while production traces with large parallel
repeated/suppressed selections silently lose links. So include a span carrying a
suppressed-sibling wait fan-in in the **thousands** (the §6.5 cap-stress shape) and
assert every wait link survives the Cloud round-trip — this is what actually
validates the `LinkCountLimit = 16384` choice against whatever cap/truncation
Cloud applies. This is the only check that proves the production path carries the
full causal model at scale; it must pass before the Cloud front-end (step 7) is
trusted.

---

## 7. Implementation sequence

> **Execution roadmap:** these 8 steps are grouped into 5 reviewable chunks — with
> per-chunk scope, gating validation, dependency DAG, and cumulative-state
> checkpoints — in [`wcprof-otel-impl-plan.md`](./wcprof-otel-impl-plan.md). That
> roadmap is the build plan; this section remains the canonical step list it maps
> from. (Mapping: step 1 → Chunk 1; step 2 → Chunk 1 vocabulary + Chunks 2–4
> emission; step 3 → Chunk 2; step 4 → Chunk 3; steps 5–6 → Chunk 4; steps 7–8 →
> Chunk 5.)

Ordered so each step is independently verifiable and the oracle (§6.2) comes
online early:

1. **IR + loader skeleton.** OTel spans (from `otlpdump` JSONL first) →
   `DumpEvent`/`DumpHeader` → `Build` → `WriteReport`. No augmentation yet; the
   output will be *wrong* — that's expected and is the baseline that motivates
   each fix. Add the §6.1 structural gate now so breakage is visible.
2. **Wait-edge + causal-parent conventions** (`link.purpose="wait"` + absolute-
   unix-ns string attrs, §3.0; `wcprof.parent` + the stamping span processor,
   §3.0.2) on the engine tracer, with `LinkCountLimit = 16384` (§3.0). Pure
   plumbing; nothing emits them yet.
3. **Cache singleflight fix** (§3.1) — `call_exec` span (minted under `callsMu`,
   Invariant T) + `publishResult` child + per-caller wait links. Stand up the
   cross-source oracle (§6.2) here: it should immediately show singleflight
   rankings converging toward native. Add the §6.5 singleflight/emitter≠executor
   fixtures.
4. **Lazy fix** (§3.2) — mint the `lazy` op under `lazyMu` before `lazyEvalWaitCh`
   (Invariant T); **keep `resumedCallbackSpan` so the UI is unchanged**; stamp
   `wcprof.parent` on the re-pointed work; joiner wait links. Add the §6.5 lazy
   re-point fidelity fixture (UI parentage unchanged + no double-count + consumer
   path includes eval + Invariant T). Oracle should converge on lazy-heavy
   workloads (Directory/File/Container pipelines).
5. **Exec engine/user split** (§3.3) — `containerStart` vs `processRun` spans with
   `work_type=user` on the process interval. This is the headline-enabling fix, not
   mere labeling; add the §6.5 delayed-setup fixture.
6. **Services** (§3.4) — `service.start` span (target stashed before publishing
   `starting`, Invariant T) + installer wait links.
7. **Cloud trace API ingest** — swap the loader's front-end from `otlpdump`
   JSONL to the Dagger Cloud trace API (the production path); the compile stage is
   unchanged. **Gate on the §6.6 round-trip test** before trusting this path.
8. **Standing drift gate** (§6.4) in CI; finer exec phases (§3.3) and the leaf-I/O
   seam (§3.5) as follow-ups.

Steps 1–3 prove the thesis; 4–6 complete faithfulness; 7–8 productionize.

---

## 8. Pushback on the brief (per §9 authority)

1. **"Trivial loader" undersells one necessary mechanical step, but the spirit is
   right.** The loader is inference-free, but it is not literally
   "spans→ops→waits" — it must dedup live-export duplicates, classify op kinds
   from attributes, and choose an epoch. These are mechanical, not causal. I'd
   restate the invariant as *"the loader performs zero causal inference"* rather
   than *"trivial"*, so nobody is tempted to push a legitimate mechanical step
   (live-span dedup) back into the emit side where it doesn't belong.

2. **The lazy fix is a control-flow change, not just a richer span — but it does
   NOT change the UI** (decided, §10). The brief frames every fix as "make the
   spans honest at the emit side." For lazy eval that turned out to require two
   real engine changes beyond adding attributes: (a) *minting the lazy op span
   under `lazyMu` before `lazyEvalWaitCh` is published* (Invariant T, §3.0.1)
   rather than inside the eval goroutine where it lives today — otherwise a
   concurrent joiner has no wait target; and (b) *separating UI parentage from
   causal parentage* via the `wcprof.parent` override + a stamping span processor
   (§3.0.2/§3.2) — because the producer-side rendering is a fixed UI constraint, so
   we keep `parentId` and emit the causal parent alongside it. Flagging that
   "faithful emit" undersells this: the choke point needs restructuring (a new
   convention + a processor). No span *moves* and the UI-visible structure is
   unchanged, though it is not literally byte-for-byte — the `lazy` op's start
   timestamp shifts marginally and the no-producer-context case gains one hidden
   passthrough span (§3.2 step 1).

3. **Nested-client stitching is *easier* in OTel than the brief implies.** §5.3
   lists nested-client nesting as a place "faithfulness tends to break." In fact
   OTel propagates the exec's traceparent into the container
   (`executor_spec.go:836-837`, `container_exec.go:1304`), so nested work nests
   correctly *for free* — this is the one spot OTel beats native (which needs an
   explicit link). Worth removing from the "danger" list so effort isn't spent
   re-deriving a non-problem.

4. **Cache *hits* are a non-goal for the counterfactual, despite native recording
   them.** The brief says emit "what OTel suppresses." That guidance is right for
   the *cache-diff sibling* but would be a volume mistake for the bottleneck
   model: in-memory hits carry ~0 self-time and no wait edge (§4.1). I read the
   brief's "deliberately record what OTel suppresses" as scoped to the sibling and
   have designed the runtime-wait source to keep suppressing repeated hits. The
   one subtlety worth stating plainly: persisted/imported cold hits *do* block on
   decode, but they are **first-occurrence** (un-suppressed) so their decode lands
   in the call span's self-time and is captured. The residual is a *repeated,
   suppressed* digest re-decoding behind an in-flight import: native and OTel
   attribute that to *different ops* (native's per-hit `call` op vs OTel's visible
   ancestor, §4.1), so it can move native↔OTel top-N rankings if the path is hot —
   a bounded native/OTel attribution gap, gated by the §6.5 persisted-cache
   fixture, not "harmless drift." Still a clarification + validation gate, not a
   redesign — and the fix, if needed, is a native gap to close first.

5. **The emitter≠executor race (Break #3) is real but I could not measure its
   frequency from code alone.** It is a genuine correctness hole (independent
   locks, §2.4) and the §3.1 fix closes it regardless of frequency by making the
   `call_exec` span suppression-independent. But whether it bites in practice
   (vs. being rare) is unknown without the corrected trace; I deliberately did
   not measure (brief §6.1). The design is robust to it either way.

---

## 9. Open questions / seams left intentionally

- **Cache-diff sibling.** `dag.inputs` (`core/telemetry.go:119-127`) is the
  cache-key edge set; left untouched. When the sibling is built, add a
  `wcprof.inputs.*` seam in the loader; do not conflate with wait edges.
- **Leaf I/O instrumentation** (§3.5): pull/git/filesync → `OpKindIO` +
  `external`, using existing `pulling`/progress/`effect.ids` telemetry.
- **Persisted-import decode singleflight** (§4.1): `persistDecodeWaitCh` /
  `attachDepsWaitCh` (`cache_persistence_import.go:563-620`) are uninstrumented in
  native too. If the §6.5 persisted-cache fixture shows non-trivial native↔OTel
  ranking drift, give them the same Invariant-T target + wait-edge treatment as
  `call_exec` — but fix native first so the oracle has ground truth to match.
- **`dagql.publishResult` as a real wait target** (§3.1): today publication is a
  late child counterfactually charged to the caller class in both native and OTel
  (parity, not attribution). If publication proves hot, model it as a synchronous
  wait target of the still-blocked caller in **both** sources so the oracle stays
  meaningful — directly parallel to the persisted-decode seam above.
- **Wait-link fan-in merge (cap reinforcement)** (§3.0): if the
  `LinkCountLimit = 16384` bound is ever approached on real traces, an emit-side
  coalescing of multiple waits from the same waiter to the same target can collapse
  the common identical-digest fan-in (N joiners of one execution → 1 link). The
  *clock* effect is idempotent (joins to one target are `max`, `replay.go:379-381`),
  **but the merge is replay-exact only under interval preconditions, not in
  general.** Wait classification is per-interval (`actWaitJoin` iff
  `wait.End ≥ target.End − ε`, `replay.go:162-173`) and self-time subtracts the wait
  interval (`graph.go:379-398`), so naively collapsing to one `[min(start),
  max(end)]` link — all the v1 single-interval wire shape (§3.0) can carry — can both
  mis-classify and delete self-time: e.g. an early *abandoned* (`actWaitNoop`) wait
  plus a later real join, merged, classifies as a join at the *early* start
  (serializing too early) and erases the self-work gap between the two waits. Safe
  forms: (a) coalesce only overlapping/adjacent waits whose individual classification
  is identical — the common concurrent fan-in (all joiners block until the one
  execution finishes) satisfies this; (b) extend the wire shape to a true
  multi-interval union and expand it back to per-interval wait events before replay;
  or (c) keep one link per disjoint interval and coalesce only duplicates/overlaps.
  It also is *not* free — it needs per-(waiter,target) wait aggregation across the
  concurrent sibling goroutines — so it is a reinforcement to hold in reserve, not
  part of v1, and must **not** be implemented from a bare "collapse to one link".
- **Multi-engine / Cloud scale-out.** `EngineIDAttr`
  (`engine/server/session.go:1417-1419`) tags spans by engine; a cross-engine CI
  run is multiple roots. The replay already chains roots
  (`replay.go:265-297`); verify behavior on a scale-out trace.
- **Epoch / clock skew across clients.** OTel spans from nested clients carry
  their own clocks; the loader anchors to the trace's min start. Cross-host skew
  (engine vs CI runner) could distort intervals — worth a validation probe on a
  real CI trace before trusting absolute durations across the client/engine
  boundary.
- **`LinkCountLimit` headroom.** Pinned at 16384 (§3.0), sized above realistic
  concurrent suppressed-sibling fan-out; the documented residual is a pathological
  >16384 fan-out on one span, guarded by the §6.5 cap-stress and §6.6 Cloud
  round-trip. Revisit the value only if those fixtures show real traces approaching
  it (then prefer the qualified fan-in merge above, under its interval
  preconditions, over an ever-higher cap).
- **Stamping-processor coverage (resolved in Chunk 3).** Earlier drafts framed
  this as covering separate "parent-client export *providers*." The reality
  (verified building Chunk 3): there is **one tracer provider per client** with
  multiple export *processors* on its chain — the client's own DB plus each
  parent export, all `LiveSpanProcessor`s appended in the same loop
  (`engine/server/session.go:684-715`). So a single stamping processor
  **prepended** to that one provider's processor chain covers **all** its exports;
  registering it first means its `OnStart` sets `wcprof.parent` before any
  `LiveSpanProcessor` snapshots the span, so even live-start exports carry it. If a
  lazy-work span missed the override on any export path, the loader would
  (correctly, per anti-inference) leave it under the producer — silently
  mis-attributing the work — so this is behaviorally guarded by
  `dagql.TestWcprofLazyParentProcessorStampsAllExports`, which asserts the stamp
  reaches both the own and parent exporters (and descendants stay unstamped in
  both).

---

## 10. Scope decisions (resolved by the project owner)

Both choices that were open in earlier drafts are now settled:

1. **Lazy UI re-pointing — RESOLVED: the UI does not change.** The producer-side
   rendering of lazily-evaluated work is an intentional dagui design choice and is
   out of scope to change. The design therefore keeps `parentId = producer`
   untouched and reconciles faithfulness via the explicit `wcprof.parent` causal
   override (§3.0.2/§3.2): UI reads `parentId`, the analyzer reads the override. No
   span moves, no change to dagui's visible tree, no sign-off gate — the earlier
   "move the work to the consumer" option (Option A) is **off the table**. (The
   only non-visible deltas are a marginal `lazy`-op start-timestamp shift and, when
   no producer span context was captured, one hidden passthrough op — §3.2 step 1;
   neither changes what dagui renders.)
2. **Scope of "why was my CI run slow?" — RESOLVED: one trace.** The unit of
   analysis is **one Cloud trace = one engine's view of one session**. Multi-trace
   / whole-CI-job aggregation is explicitly **out of scope** and must **not** be
   built into this loader — it would require cross-trace inference (forbidden by
   §3). Nothing downstream may assume more than one trace; the per-trace result is
   the deliverable. (If whole-job analysis is wanted later it is a separate
   product+ingest effort layered *above* this loader, not inside it; related: the
   scale-out seam, §9.)
