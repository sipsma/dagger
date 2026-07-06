# wcprof × OTel — design + implementation plan

**Status:** design for human review (no implementation). Designed fresh off `main` by reading
the engine source; PR #13393 (`engine/wcprof/**`, merge `6499f42650`) is the foundation and is
treated as correct. All citations are `file:line` against the tree this doc lives in, except the
`engine/wcprof/**` analyzer, which is cited against the #13393 merge commit (it is not yet on the
commit this worktree branches from — see §0).

---

## 0. TL;DR (the whole design in one screen)

wcprof's analyzer is a discrete-event **replay** whose *only* causal inference is the
**implicit join**: a parent op's clock advances to the simulated finish of every child whose
recorded interval ended before the parent's next action (`wcanalyze/replay.go:11-39`,
`351-369`). That rule is sound **only when a structural parent genuinely contains and
synchronously waits for its children**, and when every *other* blocking relationship arrives as
an **explicit wait edge attributed to the op that actually blocked**. Native wcprof guarantees
both by recording at choke points.

OTel's span tree is built by context propagation. For the dagql engine it is *mostly* the same
synchronous call tree — but it diverges in exactly the places that matter, and always for the
same two reasons:

1. **Dedup hides the blocker.** `ShouldEmitTelemetry` suppresses the 2nd+ occurrence of a call
   digest in a session (`dagql/telemetry.go:48-64`, `core/telemetry.go:62-64`). A singleflight
   **joiner** and a **lazy-eval joiner** therefore block with **no span and no edge** — the
   dependency is invisible, so the replay under-serializes (speeding up the shared work shows no
   saving on the joiner's branch).
2. **Spans get re-pointed / outlive their waiter.** Lazy "resume" spans and long-running
   **service** spans are parented for UI reasons and carry `cause`-purpose links that are *not*
   runtime waits (`dagql/cache.go:639-659`, `core/service.go:741-746`). A loader that reads span
   nesting or `cause` links as waits invents serialization and can manufacture cycles.

So the work is **at the emit side**, and it is small and local:

- **Make the shared execution a stable node and make every blocker point at it.** Emit a
  dedicated `call_exec` span at singleflight election, host the resolver under it, and emit an
  explicit **wait edge** (a span link with a dedicated purpose + a start/end interval) from every
  joiner to it. Do the same for lazy joiners (→ the existing resume span) and service-start
  joiners (→ a new `service_start` span).
- **Encode wait edges so they cannot be confused with anything else and cannot be silently
  dropped.** A dedicated link purpose (`dagger.io/wcprof.wait`) distinct from `cause`
  (otel-go `attrs.go:93-100`), carrying the interval as attributes; and raise the OTel span link
  cap, which is the default 128 today (`engine/server/session.go:666-708` sets no `SpanLimits`).
- **Loader does nothing clever.** spans → ops (kind/class/ident from attributes), span-parent →
  structural parent, `wcprof.wait` links → wait edges. Ignore `cause` links, `dag.inputs`, and
  every other link. Emit the *same* `wcprof.DumpEvent` stream the native recorder emits and call
  the **existing** `wcanalyze.Build` + replay unchanged.
- **Validation is a gate, not a report.** Cross-source oracle (native vs OTel on a dev engine),
  structural invariants (zero broken cycles, no op self-time > makespan), known-answer injection,
  and a drift gate on a *representative* workload — all run **only** on traces from the corrected
  engine.

One genuine **OTel advantage** falls out for free: nested-client (module-runtime) work already
stitches into the parent trace via traceparent propagation (`engine/server/session.go:1330-1349`),
so the loader needs **no** equivalent of native's `nested_client` link.

> **§0 note on the worktree.** This branch (`wcprof-otel-fresh-design-…`) is based on a commit
> *before* #13393 merged, so `engine/wcprof/**` is not checked out here. The merge commit
> `6499f42650` is in the object DB and is the canonical "on main" version; analyzer citations are
> against it. This is almost certainly a worktree-setup oversight, not a signal that the analyzer
> is absent on `main`. Flagging so the human can rebase the eventual implementation branch onto a
> commit that actually contains `engine/wcprof/**`.

---

## 1. The foundation: what the replay actually requires

The loader's job is to feed `wcanalyze.Build` (`graph.go:147`) an op/wait/link event stream and
let the existing replay run. So the binding contract is the replay's semantics. Reading
`replay.go` precisely:

- **Op timeline.** Each op replays as `self` segments (advance clock by `dur×factor`), `spawn`
  (anchor a child's start at the current clock), and `wait` actions, sorted by time
  (`replay.go:154-185`).
- **Wait classification at compile time** (`replay.go:162-175`) — this is the crux of what an
  edge must look like:
  - `actWaitJoin` ⟺ `Target != nil && Target != self && wait.EndNS ≥ Target.EndNS − 1ms`. A real
    blocking dependency: `clock = max(clock, finish(Target))`.
  - `actWaitFixed` ⟺ `Target == nil` (named-resource/lock wait): `clock += dur`.
  - `actWaitNoop` ⟺ a wait that **ended before its target's recorded end** — i.e.
    abandoned/canceled/mis-resolved. Contributes no time; it only marks an action point. **This is
    the model's built-in defense against junk edges: an edge whose interval doesn't actually reach
    its target's completion is ignored.**
- **Implicit join** (`replay.go:351-369`): before each action at original time `t`, the op joins
  every child whose `EndNS ≤ t` (children pre-sorted by `EndNS`). This is the synchronous-call
  model and "is conservatively safe for async children" — *if and only if* the child genuinely sits
  inside the parent. A child that ends **after** the parent is never joined (so a long-running
  service child can't serialize its starter — see §3.4).
- **Self-time** (`graph.go:381-398`): `duration − Σ child intervals − Σ wait intervals`. So a wait
  edge **must carry a real start/end interval** or the waiter's idle time is mis-counted as its own
  self-time (and would rank as a false bottleneck). A bare link with no interval is not enough.
- **Cycle break** (`replay.go:341-345`) and **fallback anchor** (`replay.go:317-338`) are
  *diagnostics of bad data*, surfaced in the report (`report.go:163-164`). Per the brief: a cycle
  is always mis-emitted data, never a replay flaw. We treat any nonzero `CycleWarnings` as a build
  failure (§5), not something to paper over.

**Three hard requirements for emit fall directly out of this:**

| # | Requirement | Why (replay mechanic) |
|---|---|---|
| R1 | Every span-parent relationship the loader keeps must be a **synchronous container** (parent blocked until child ended, or child ends after parent so the join skips it). | Implicit join treats it as a `max(clock, finish(child))`. |
| R2 | Every cross-op block that is *not* a structural parent/child must arrive as an explicit edge **on the op that blocked**, **targeting the op it waited on**, **with the true block interval**. | `actWaitJoin` needs Target + an interval reaching Target's end; self-time needs the interval. |
| R3 | Anything that is *not* a runtime wait (UI `cause` links, `dag.inputs` cache-key edges) must be **distinguishable** so the loader drops it. | A spurious edge → false serialization or a cycle. |

Everything below is in service of R1–R3.

---

## 2. The one principle

> **Faithful structural parent + explicit, correctly-attributed wait edges + a vocabulary that
> separates waits from everything else. The loader compiles; it never infers.**

There is no second mechanism. We do not add simulators, cycle-breakers, node synthesis, or
timestamp-containment reparenting. If the replay produces a cycle or drift on corrected data, the
emit is wrong and we fix the emit (§5 makes that loud).

---

## 3. Engine-side OTel augmentation, choke point by choke point

For each: **what's emitted today** (cited), **where faithfulness breaks**, **what to emit**.

### 3.1 dagql call resolution + singleflight — *the crux*

**Today.** `ObjectResult.call` wraps every field selection: `s.telemetry(ctx, req)` starts one
span via `core.AroundFunc` and the `defer`'d `done` ends it, wrapping the **entire**
`cache.GetOrInitCall` including the singleflight wait (`dagql/objects.go:634-643`, `657`). The span
carries `dag.digest` (op identity — free, already computed), `dag.call`, `dag.inputs`, and
`ui.internal` (`core/telemetry.go:83-131`). Crucially, `AroundFunc` returns `NoopDone` with **no
span** when `ShouldEmitTelemetry` is false (`core/telemetry.go:62-64`), and that returns false for
the **2nd+ occurrence of a call digest in the session** unless `DoNotCache`
(`dagql/telemetry.go:48-64`).

Inside `getOrInitCall` (`dagql/cache.go:3480`):

- **Cache hit** → returns immediately (`3579-3583`). Instant; no blocking.
- **DoNotCache** → runs `fn` inline in the caller (`3498-3501`). Synchronous, always emits a span.
- **Miss** → singleflight: under `callsMu`, the first caller for a `(callKey, concurrencyKey)`
  creates an `ongoingCall` and runs `fn` in a **detached goroutine** under
  `oc.sharedWorkCtx = context.WithoutCancel(callCtx)` (`3611`, `3631-3643`); later callers with the
  **same `ConcurrencyKey`** increment `waiters` and join (`3590-3599`). Both executor and joiners
  then block in `c.wait` on `oc.waitCh` (`3646`, `3782-3801`). Joining requires
  `req.ConcurrencyKey != ""` (`3590`, `3627`); without it, concurrent identical misses
  **double-execute** (the PR's "duplicate-execution detection").

**Where it breaks (three distinct faults):**

1. **The joiner is invisible.** A concurrent joiner is the 2nd occurrence of the digest →
   `ShouldEmitTelemetry` false → **no span**. It then blocks in `c.wait` until the shared `fn`
   completes. Nothing in OTel records that this caller's current span was blocked, nor on what.
   The replay sees dead air in the joiner's branch and **under-serializes**: scaling the shared
   work shows no saving where a joiner depended on it. *(This is the #1 gap.)*

2. **Emitter ≠ executor (a race).** "First to pass `ShouldEmitTelemetry`" (a session seen-key
   `LoadOrStore`, `telemetry.go:53-55`) and "first to create the `ongoingCall`" (`callsMu`,
   `cache.go:3585-3628`) are **independent** races run at different points of `call`
   (telemetry at `objects.go:635`, election at `objects.go:657`). When caller A emits the span but
   caller B wins election, `fn`'s sub-work nests under **B's** context — and B was suppressed, so
   it nests under **B's parent span**, not under A's call span. A's span becomes an empty waiter
   whose self-time is its whole wait (a false bottleneck), and the real work is mis-attributed to
   B's subtree. With ≥2 concurrent callers this is ~50/50, not rare.

3. **Duplicate executions** (no `ConcurrencyKey`) each run under a different suppressed caller's
   parent — the work is real but scattered with no clean per-execution node.

All three have the same root cause: **OTel has no stable node for "the shared execution," and no
edge from the callers that blocked on it.** Native models this exactly with a `call`-op-per-caller
+ one shared `call_exec` op + singleflight wait edges (PR §Design).

**What to emit.**

- **(a) A dedicated `call_exec` span**, created **synchronously at executor election** (where `oc`
  is built, `cache.go:3616`), parented under `oc.sharedWorkCtx`, identity = `dag.digest`,
  marked `ui.internal` **and** `ui.passthrough` so it's transparent in dagui but present in the
  trace. Store its `trace.SpanContext` on `ongoingCall` (new field) **before** unlocking `callsMu`
  so every joiner can read it without a race. Run `fn` under it (so the resolver's sub-work nests
  under `call_exec`, not under whichever caller happened to emit). This single change neutralizes
  faults 2 and 3: the execution now has one correctly-parented node regardless of who emitted, and
  duplicates each get their own node. End it when `fn` returns (the goroutine at `3631-3643`).

- **(b) A wait edge from every blocking caller to its `call_exec`.** At `c.wait`
  (`cache.go:3782`), where the executor/joiner distinction and the block interval `[enter, waitCh
  close]` are both known, emit a `wcprof.wait` link (§3.6) from the **caller's current span** to
  `oc`'s `call_exec` span, reason `singleflight` (joiner) or `call_exec` (executor), interval =
  the actual wait. The executor's edge is redundant with the implicit join (its `call_exec` is a
  context-child) but keeping it uniform is harmless and race-proof. The joiner's edge is the part
  that was missing. **The waiter is the caller's existing span — no new per-joiner span**, so
  suppression's volume win is preserved (we add links, not spans).

- **(c) Cache hits and most repeats stay suppressed and need nothing.** A hit is instant
  (`3579-3583`); it never blocks, so it contributes no wait. This is the key reason OTel's
  aggressive suppression is *fine* for wcprof: the only suppressed calls that matter to the
  waits-graph are the ones that actually block (singleflight + lazy joiners), and we add edges for
  exactly those. *(One caveat: a hit can return a still-`pending` result — that deferred cost
  surfaces at the lazy choke point, §3.2, not here.)*

- **(d) Class/identity.** `call_exec` carries `dag.digest`; the loader's class is the field name
  (`Type.field`, already the span name in `core/telemetry.go:51`) and ident is the digest. No new
  attribute needed beyond what `AroundFunc` already emits, plus a `kind=call_exec` marker (§3.6).

**Cost of (a):** +1 internal span per *executed* (cache-miss) call. Executions are the actual
work and are bounded; suppression already keeps callers/hits span-free; joiner edges are links.
So the marginal volume is "one transparent span per real execution," which is the right
correctness/volume trade (and exactly what native records as `call_exec`). A lighter variant —
reuse the emitter's `AroundFunc` span as the host and only synthesize `call_exec` when the
executor was suppressed — halves the new spans but adds conditional complexity; I recommend
shipping the uniform version first and measuring before optimizing.

**Why no cycle here.** A joiner→`call_exec` edge can only cycle if the execution's subtree waits
back on the joiner's ancestor. The cache forbids recursive evaluation of the same key
(`ErrCacheRecursiveCall`, `cache.go:3567-3568`), and a finished run has no deadlock, so faithfully
attributed edges can't close a loop. A cycle in the loaded graph therefore means a mis-attributed
edge — caught by §5.

### 3.2 Lazy / deferred evaluation

**Today (already sophisticated).** A pending result is evaluated by `evaluateOne`
(`dagql/cache.go:2876`). The first caller to force it sets `lazyEvalWaitCh` and runs the callback
in a detached goroutine; **later forcers join** via `waitForLazyEvaluation` (`2926-2931`), and
the first forcer *also* blocks on it (`3032`). In the goroutine, the engine looks up the span
context where the lazy value was **created** (`captureSessionLazySpanContext`, `435-458`,
populated at install/hit, `3580`/`3894`) and starts a **"resume `<field>`" span** parented under
the **triggering** caller's `evalCtx`, with `trace.WithLinks(...)` back to the creation span and
all **install** spans (`2946-2972`). Those links are tagged `dagger.io/link.purpose = cause`
(`lazyResumeLinks`, `639-659`; otel-go `attrs.go:93-100`) for dagui failure attribution. The
deferred work runs **under the resume span**.

**Where it breaks — and where it doesn't.**

- ✅ **Trigger → resume is faithful.** The triggering caller blocks (`3032`) while the resume span
  (its context-child) runs the deferred work. Implicit join models it correctly; the deferred work
  genuinely nests under the resume span. Nothing to add for the trigger.
- ❌ **Lazy joiners are invisible** (`2926-2931`) — same shape as §3.1's singleflight joiner. They
  block with no span and no edge.
- ⚠️ **The `cause` links are a trap for the loader.** They point resume → *creator/installer*,
  which have already **ended** (the creator returned a pending result long ago). If the loader read
  links as waits, `resume`'s "wait" on the creator would satisfy `wait.EndNS ≥ Target.EndNS`
  (creator ended in the past) → classified `actWaitJoin` → a backwards dependency, and a strong
  cycle risk if the creator is anywhere above the resume in the tree. **These must be ignored.**

**What to emit.**

- **(a) Lazy-joiner wait edges.** In `waitForLazyEvaluation` (joiner path, `2930`/`3032`), emit a
  `wcprof.wait` link from the joiner's current span to the **resume span**, reason `lazy`,
  interval = the block. Requires the resume span's `SpanContext` to be reachable by joiners: stash
  it on the `sharedResult`'s lazy state next to `lazyEvalWaitCh` (`2940`) so every joiner can link
  to it. (The resume span is created inside the goroutine at `2962`; set the stash there, and have
  joiners that arrive before it's set fall back to no edge — they'll show as bounded dead air, which
  the drift gate tolerates and reports rather than mis-attributes.)
- **(b) Class the resume span by the creating call.** Add `dag.digest` (and reuse the field name)
  to the resume span so the loader attributes its self-time to the call that *created* the lazy
  value — matching native's "`lazy` ops classed by the call that created the lazy value" (PR
  §Design). Today the resume span only has a name (`2951-2953`).
- **(c) Loader ignores `cause`.** Hard rule (§4): only `purpose = wcprof.wait` links become edges.
  This is also why wait edges get their **own** purpose rather than reusing `cause` — and it's the
  same lesson as the prior-attempt commit "keep profiler causal links out of dagui's cycle-prone
  graph": link-consumers (dagui) and wcprof must not share an edge vocabulary.

### 3.3 Container exec + the nested-client boundary

**Today.** A `Container.withExec`/`sync` selection is a normal dagql call, so it gets an
`AroundFunc` span (§3.1). The exec captures `causeCtx := trace.SpanContextFromContext(ctx)` (that
call's span, `core/container_exec.go:1297`) and passes it to `engineClient.Run(..., causeCtx, ...)`
(`2093-2100`). The executor runs setup phases sequentially with **no per-phase OTel span**
(`engine/engineutil/executor.go:114-131`, `134-169`), and **no dedicated exec/process span** —
in-container telemetry and the process runtime are attributed to the `withExec` call span. A
nested client (module runtime) that calls back into the API gets its query span **remote-parented**
under `causeCtx` via traceparent propagation: each served query starts a span only if the incoming
context is traced (`engine/server/session.go:1330-1349`), and spans fan out to parent client DBs
(`691-697`).

**Where it breaks — and a free win.**

- ✅ **Nested-client stitching is free.** Because the whole run shares one trace and the nested
  client's query span is a remote child of the exec's `causeCtx`, module-function work already
  nests under the exec in the OTel tree. Native needs an explicit `nested_client` link to achieve
  this (PR §Design; `graph.go:270-294`); **the OTel loader needs none.** This is a real point in
  OTel's favor and simplifies the loader.
- ⚠️ **User work is not first-class.** The process runtime is self-time of the `withExec` call
  class. That's correct *attribution by class* (a slow `go build` ranks under `Container.withExec`),
  so the brief's "user work is a valid headline answer" already holds at v1. What's missing is the
  native split of `exec.containerStart` (engine overhead) vs `exec.processRun`
  (`WorkType=user`), and per-phase costs (`setupNetwork`, …).

**What to emit.**

- **v1 (sufficient):** nothing new — rely on the `withExec` call span + free nested stitching. User
  work attributes by class.
- **v1.1 (refinement, recommended soon):** a `processRun` child span (`WorkType=user`) around the
  actual process, and optional phase spans mirroring native's phases
  (`executor.go:114-131`). These are *additive* synchronous children of the exec (R1 holds
  trivially — the exec blocks on them), so they carry no new faithfulness risk; they only sharpen
  attribution. The PR's headline finding (`exec.setupNetwork` p95 tail) lives here, so it's worth
  doing, but it is not on the correctness-critical path.

### 3.4 Services — availability vs. work

**Today.** `Services.Start` dedups concurrent starts via `ss.starting[key]`: a caller that finds a
start in flight joins it (`core/services.go:323`, `561-579`); `StartBindings` starts a binding set
in parallel (`522-559`). `Service.Start` (`core/service.go:509`) blocks until the service is
**healthy** and returns, leaving it running. The span it creates is named `exec <args>` and — key
detail — **`span.End` fires when the service EXITS / is torn down**, not when it's healthy: the
deferred `End` is conditional on a start error only (`core/service.go:748-771`). It also gets
`cause` links to install spans (`741-746`).

**Where it breaks — and where it self-corrects.**

- ✅ **The long availability span does *not* over-serialize its starter** — and this is worth
  stating precisely because it looks dangerous. The service span is a context-child of the starting
  call but **ends after** the starter (at teardown). The implicit join only joins children with
  `EndNS ≤ t` (`replay.go:357-360`), so the starter, finishing first, **never joins** the service
  span. It dangles. And because nothing waits on its *finish*, scaling its self-time can't move any
  root's finish → it **does not rank** as a bottleneck. The makespan is over roots
  (`replay.go:463-474`), and the service span is not a root. So the naive fear ("a service that runs
  the whole workload becomes the bottleneck") does **not** materialize, *provided* the span stays a
  non-root child. **This is the one place I'd add a guard rather than trust the argument** (below).
- ❌ **The start cost has no clean node, and joiners are invisible.** The bottleneck-relevant part
  is start→healthy (the body of `Service.Start`), but the only span spans start→exit. A caller that
  binds a service and waits for health has no edge to the thing it waited on; start-dedup joiners
  (`services.go:323`) are invisible like every other joiner.
- ⚠️ **`cause` links again** (`service.go:741-746`) — ignored by the loader (§3.2c).

**What to emit.**

- **(a) A `service_start` span** bounding exactly `Service.Start`'s execution (entry → healthy
  return; the health wait is at `service.go:862-877`), `kind=service_start`. This is the
  start-cost node native records. Parent it under the starter; it ends *before* the starter
  continues, so it's a normal synchronous child.
- **(b) Wait edges from start-dedup joiners** (the `isStarting` path, `services.go:323`) to the
  `service_start` span, reason `service`, with interval. Same joiner pattern as §3.1/§3.2.
- **(c) Neutralize the availability span explicitly** rather than relying on the dangle argument.
  Mark the long `exec <args>` service span with a `kind=service_availability` marker (§3.6) and have
  the loader give availability ops **zero self-time** (they are not work — the daemon idles). This
  is honest emit (availability genuinely isn't work), not loader inference, and it makes the §5
  "no self-time > makespan" invariant robust even if a service is mis-parented as a root by some
  edge case. *(Alternative: have the analyzer exclude `kind=service_availability` from what-if
  candidates. I prefer zeroing at load because it keeps the analyzer untouched.)*

### 3.5 Session / query phases

**Today.** Each served query is a `<METHOD> <path>` span marked `ui.passthrough`, created only when
the incoming context is traced (`engine/server/session.go:1330-1349`). Native additionally splits
per-query `session_phase` ops (attachables wait, workspace/module load, schema build, query)
(PR §Design; `daggerSession` carries `attachables`/`workspaceLoaded`, `session.go:70`,`201`).

**Where it breaks.** Minimal. The query span is a faithful synchronous root per query. The phases
are uninstrumented, so the *time inside them* shows as the query span's self-time (or dead air if a
phase blocks on something off-tree, e.g. attachables) rather than as named, rankable classes.

**What to emit.** v1: nothing — the query span is a fine session root. v1.1: phase child spans
mirroring native; these are synchronous children (the server does them inline before resolving), so
R1 holds. Lower priority than §3.1–§3.4.

### 3.6 Cross-cutting emit infrastructure (the part that makes the above safe)

1. **A dedicated wait-edge vocabulary.** Define `LinkPurposeWait = "wcprof.wait"` alongside the
   existing `cause`/`error_origin` purposes (otel-go `attrs.go:93-100`). A wcprof wait edge is a
   `trace.Link{ SpanContext: <target op's span>, Attributes: { link.purpose=wcprof.wait,
   wcprof.wait.reason=<…>, wcprof.wait.start_unix_nano, wcprof.wait.end_unix_nano } }` attached to
   the **waiter** span. Encoding the interval as link attributes means **zero extra spans** for the
   common case and lets the loader recover the exact block window (needed for self-time, §1). Reason
   ∈ {`singleflight`,`call_exec`,`lazy`,`service`,`lock`,`exec`,`io`} mirrors
   `wcprof.WaitReason`. *(If link attributes prove lossy through the Cloud pipeline — verify in the
   oracle — fall back to a tiny child "wait" span per blocking joiner: more faithful timing, more
   spans. Decide from the §5 oracle, not from guessing.)*

2. **A node-kind marker.** Add `wcprof.kind` ∈ {`call_exec`,`lazy`,`service_start`,
   `service_availability`,`exec`,`processRun`,`session_phase`,…} to the spans we create or augment,
   so the loader maps span → `wcprof.OpKind` without heuristics. Plain dagql call spans need no
   marker (loader infers `kind=call` from presence of `dag.digest` + absence of a more specific
   kind). `WorkType` rides along as `wcprof.work` ∈ {`engine`,`user`,`external`}.

3. **Raise the span link/event caps.** The TracerProvider is built with **no `SpanLimits`**
   (`engine/server/session.go:666-708`), so OTel's default **128 links/span** applies. A single
   resolver that joins many shared sub-results (e.g. a fan-out selecting the same cached object)
   can exceed it and **silently drop** wait-edge links — exactly the brief's practical warning, and
   exactly what the prior-attempt commit "raise OTel span link/event caps so causal edges aren't
   dropped" addressed. Add `sdktrace.WithRawSpanLimits(limits)` (with link/attr-per-link counts
   raised, e.g. to a few thousand / unlimited) to `tracerOpts` here. **This is a correctness
   prerequisite for §3.1b/§3.2a/§3.4b**, not a tuning knob.

4. **`ui.internal` + `ui.passthrough` on the nodes we add** (`call_exec`, `service_start`) so dagui
   collapses them and the user-facing tree is unchanged, while they still export to Cloud (the
   ingest source). Verify in the oracle that `ui.internal` does not suppress *export* (it governs
   UI rendering, `core/telemetry.go:129-131`).

5. **Leave the cache-input seam alone.** `dag.inputs` (`core/telemetry.go:119-127`) is the
   cache-key input graph for the future cache-diff sibling. The loader **must not** read it as wait
   edges (R3). Document it as a separate, out-of-scope graph (brief §3, "two graphs, one IR").

---

## 4. The loader (deliberately trivial)

A new analyzer source: `wcanalyze.LoadOTel(trace) (*Graph, error)`, sibling to `Load`/`LoadMulti`
(`graph.go:94-142`). It reads one OTel trace (the Cloud trace API; `otlpdump` JSONL locally for
dev) and produces **the same `wcprof.DumpHeader` + `[]wcprof.DumpEvent`** that the native dump
produces, then calls the **existing** `Build` (`graph.go:147`). The analyzer, replay, and report
are reused byte-for-byte.

**Compile rules (mechanical, no inference):**

1. **Time frame.** `epoch = min span start` in the trace; all `start/end` → recorder-relative nanos.
   One trace → one graph, one time frame (the replay requires a single frame, `replay.go:30-34`).
2. **Span → op.** For each span we keep: `OpID` = a stable hash of the span ID; `Kind` from
   `wcprof.kind` (or `call` when `dag.digest` present and unmarked); `Class` = span name (already
   `Type.field`, `core/telemetry.go:51`); `Ident` = `dag.digest`; `ClientID` = resource/service
   attrs; `WorkType` from `wcprof.work`; `Outcome` from `dag.cached`/`dag.canceled`/error status
   (`core/telemetry.go:258-266`); `Start/End` from the span. **`service_availability` ops get
   self-time zeroed (§3.4c).**
3. **Span-parent → structural parent** (`ParentID`). Remote parents (nested clients) resolve the
   same way — this is the free stitching (§3.3). Spans with no in-trace parent are roots.
4. **`wcprof.wait` links → wait edges.** Waiter = the link's owning span's op; Target = the op for
   the link's target span ID (index span-ID→op while building); `Start/End/Reason` from link attrs.
   That's the entire wait construction.
5. **Drop everything else.** `cause`/`error_origin` links, `dag.inputs`, OTel events, metrics — all
   ignored. Spans the engine marks uninteresting (`ui.internal` introspection/meta that we did *not*
   create) can be dropped or kept; keeping them only adds harmless leaf nodes.
6. **What the loader must NOT do** (anti-inference, brief §6.2): no synthesizing ops absent from the
   trace; no reparenting by timestamp containment; no inferring a wait from a time gap; no turning a
   `cause` link into an edge; no deriving causality from `dag.inputs`. If structure is wrong, it's
   fixed in §3, never here.

The output being literal `DumpEvent`s (not a bespoke graph) is deliberate: it keeps **one IR**, lets
`LoadMulti`-style merging and every existing diagnostic work unchanged, and makes the cross-source
oracle a byte-level comparison of two event streams when we want it.

---

## 5. Validation plan (first-class — a gate, not a report)

All checks run **only on traces from the corrected engine** (brief §6.1/§7). Reuse the analyzer's
own diagnostics — they already exist (`report.go`, `replay.go`).

1. **Structural invariants (hard gate on every loaded graph).** Run a baseline
   `NewSimulation(g,nil).Run()` and assert:
   - `CycleWarnings == 0` (`replay.go:343`, surfaced `report.go:163`). **Any** cycle fails the
     build — it is mis-emitted data (brief §6.3).
   - `FallbackAnchors` below a small bound (`replay.go:334`) — large counts mean detached/misparented
     spans.
   - No op `SelfNS() > makespan` and no op self-time implausibly large (`graph.go:401`,
     availability zeroed per §3.4c).
   - `OrphanWaits` bounded (`graph.go:266`, `report.go:267-273`) — a `wcprof.wait` link whose
     waiter/target didn't resolve means a dropped or mis-targeted edge (also catches the 128-link-cap
     regression of §3.6.3).
2. **Cross-source oracle (the strongest check).** On a dev engine with **both** native wcprof and
   the OTel augmentation active, run one workload; compile both to the model; compare. Native is
   ground truth (brief §2, "treat the analyzer/replay as validated"). Concretely:
   - per-class total self-time agrees within a tolerance, **especially `WorkType=user`** (user
     self-time should match native essentially exactly — divergence localizes to un-augmented
     buildkit/leaf spans);
   - the top-N what-if rankings agree;
   - simulated baseline makespan agrees and both are ≈ actual.
   Build this as a test harness, not a one-off. *(This is the same cross-source methodology the
   native PR used to find its three replay bugs — here it validates the OTel **emit** instead.)*
3. **Known-answer injection.** Behind a file-gate (as the PR's bring-up aid did), inject a fixed
   delay into one class on a **strictly serial** synthetic chain; assert that class ranks #1 with a
   predicted save ≈ injected delay, and that **parallel/off-path** work does **not** rank
   (the `HTTPState._resolve`-style discrimination). Disarm → assert it leaves the rankings (no false
   positive).
4. **Drift gate on a *representative complex* workload.** `dagger call engine-dev container sync`
   cold-cache is the PR's testbed (12-way module suites for stress). Gate on
   `|simulated − actual| / actual` below a threshold. Toy workloads hide over-serialization (brief
   §7) — the gate must run on something with real concurrency, dedup, services, and a module call.

**Order matters:** invariants (1) are cheap and catch the catastrophic failures (cycles, dropped
edges) first; the oracle (2) catches subtle over/under-serialization; (3)/(4) catch counterfactual
errors. Wire (1) into the loader as a hard error so a bad trace never silently produces a plausible
wrong answer.

---

## 6. Implementation sequence

1. **Loader + harness skeleton.** `wcanalyze.LoadOTel` over `otlpdump` JSONL → `Build`; structural
   invariants (§5.1) as a failing gate; the cross-source oracle harness (§5.2). *Build this first so
   every emit change below is validated the moment it lands — never reverse-engineer from an
   uncorrected trace (brief §6.1).*
2. **Emit infrastructure (§3.6).** `LinkPurposeWait` + interval attrs, `wcprof.kind`/`wcprof.work`
   markers, and **raise the span link cap** (`session.go:666-708`). Nothing downstream is faithful
   until the cap is raised.
3. **dagql singleflight (§3.1) — the crux.** `call_exec` span on `ongoingCall`; joiner + executor
   wait edges at `c.wait`. Gate against the oracle: user-work self-time must match native.
4. **Lazy eval (§3.2).** Stash the resume span context; lazy-joiner edges; class the resume span;
   confirm the loader ignores `cause` links (add a deliberately-cause-linked case to the oracle).
5. **Services (§3.4).** `service_start` span + start-join edges + availability zeroing.
6. **Validation hardening.** Known-answer injection (§5.3) and the drift gate on the representative
   workload (§5.4) in CI-shaped form.
7. **Refinements (§3.3 v1.1, §3.5).** `processRun`/phase spans, session phases — additive, do after
   the core is green.
8. **Cloud ingest.** Swap the dev `otlpdump` reader for the Dagger Cloud trace API as the loader's
   input; re-run the oracle on a Cloud-fetched trace of the same workload to prove parity end to end.

Steps 1–3 deliver the headline ("why was my run slow," user work first-class) on dev traces; 5–8
generalize and harden.

---

## 7. Pushback on the brief (per §9)

1. **"Faithful emit, trivial loader" needs one explicit carve-out: the service availability span.**
   The honest emit there is "this span is availability, not work," and the loader acts on that by
   zeroing self-time (§3.4c). That's a *classification* the emit hands over, not loader inference —
   but it's worth naming so it isn't mistaken for a violation of the "trivial loader" rule. I argued
   the availability span is *also* harmless via the dangle argument (§3.4), but I still recommend the
   explicit marker as a guard; relying solely on "it ends after its parent so the join skips it" is
   true but fragile to any future edge that makes a service span a root.

2. **The `call_exec` span is a real volume add the brief's "respect OTel volume" should expect.**
   Correctness here *requires* a stable execution node (the emitter≠executor race is ~50/50 under
   concurrency, not rare). I come down on the side of "+1 transparent span per execution" because it
   mirrors the validated native model and kills two faults at once — but the brief frames volume and
   faithfulness as a balance, and this is the one place I'd spend volume. Flagging so it's a
   conscious decision, with the lighter conditional-creation variant noted (§3.1).

3. **Wait edges need an *interval*, not just a link.** The brief's §5 practical note is about link
   **caps**; just as important is that a bare link can't carry the block window, and the replay needs
   it for both the join classification and self-time (§1, R2). My design puts the interval in link
   attributes; if the Cloud pipeline drops link attributes, the fallback is a per-wait child span.
   This is a concrete thing the oracle must check early.

4. **One brief premise is *more* favorable than stated: nested-client stitching.** The brief lists
   "container exec / nested-client work nests under the exec span" as a place faithfulness "tends to
   break." In OTel it's the opposite — traceparent propagation already stitches it for free
   (`session.go:1330-1349`), and the loader needs nothing where native needed an explicit link. Worth
   correcting because it *removes* loader work rather than adding it.

5. **Scope clarification on "why was my CI run slow?"** A CI run is often several `dagger`
   invocations = several traces with independent time frames. The replay assumes one frame
   (`replay.go:30-34`), so v1 analyzes one trace at a time; cross-trace stitching (an OTel analog of
   `LoadMulti`) is future work, not a v1 requirement. The brief's framing implies a single artifact;
   I'd make the per-trace scope explicit.

6. **Minor:** the worktree is based on a pre-#13393 commit, so `engine/wcprof/**` isn't checked out
   here (§0). Not a design issue, but the implementation branch should start from a commit that
   actually contains the analyzer.

---

## 8. Open questions to resolve during implementation (cheaply, via the oracle — not by guessing)

- **Does `ui.internal`/`ui.passthrough` keep a span out of the user UI while still exporting it to
  Cloud?** Design assumes yes (`core/telemetry.go:129-131`); verify in the oracle before relying on
  `call_exec` being invisible-but-present.
- **Do `trace.Link` attributes survive the engine→client→Cloud export intact?** Determines
  interval-in-link-attrs (preferred) vs. a per-wait child span (§3.6.1).
- **Are there resolver-spawned async children that end *before* their parent and are not awaited?**
  These are the only remaining over-serialization risk under R1. The drift gate + `DriftOrigins`
  (`replay.go:668`) localize them; fix per-case by re-pointing or marking, never in the loader.
- **Lazy resume-span stash timing:** a joiner can arrive between `lazyEvalWaitCh` being set
  (`cache.go:2940`) and the resume span being created (`2962`). Design has it fall back to no edge
  (bounded dead air) rather than race; confirm the window is negligible or set a placeholder span
  context earlier.
