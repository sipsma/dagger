# wcprof × OTel: a second data source for wall-clock bottleneck analysis

Status: **design + implementation plan, pre-implementation.** Review artifact.
Targeted at agent reviewers (and Erik). No code has been written yet.
**Revision 5** — Codex rounds 1–3 + Erik round-4 feedback (ingest correction,
availability modeling, first-class validation plan); see §12 Changelog.

Confidence tags: **[code]** verified against current source · **[empirical]** verified
via an `otlpdump` capture during design · **[inferred]** reasoned from code ·
**[open]** needs verification before/while implementing.

---

## 0. TL;DR for reviewers

`wcprof` (merged in #13393, `engine/wcprof/**`) records an explicit **waits-for
graph** at the engine's blocking choke points; an offline analyzer runs
**counterfactual replay** ("scale class X's self-time by f → how much makespan is
saved?") to rank true bottlenecks.

This plans a **second data source**: the engine's **OTel telemetry**, compiled to
the **same analyzer IR**, so the same analysis runs over a trace we already collect
from every local run and every CI run (via Dagger Cloud). Motivating feature
(user-facing): *"why was my run slow?"* on a CI trace. Deferred sibling: cache
*"why was this missed?"* via run-diffing.

Two correctness principles drive the whole design:

1. **Anti-inference** (§1.2): the engine emits explicit causal edges + explicit
   identity; the analyzer never guesses. All runtime wait edges are explicit
   `dagger.io/wcprof.wait.*` records emitted **at the wcprof wait sites**;
   `link.purpose=cause` provenance links are UI-only and ignored by the runtime graph.
2. **Bounded targets** (§1.3, from round-2 review): a wait edge is only honored by
   the replay as a *join* if its target op **ends at the unblock point**
   (`replay.go:165`: `wait.End ≥ target.End − ε`, else `actWaitNoop`). Therefore
   every wait must target a **bounded** op (e.g. `service_start` ending at readiness,
   a synthesized `call_exec` ending when the shared execution returns), **never** a
   long-lived lifetime span (service lifetime, the first-caller's whole call span).

---

## 1. Design

### 1.1 Problem & north stars

- **Primary (this project):** ingest a whole run's OTel trace — incl. a large CI
  test run from Dagger Cloud — and answer **"why was my run slow / what took so
  long?"** Eventually **user-facing**, so the user's **own** work (e.g. *"your
  `go build` took 38s"*, at exec granularity — not process internals) is a
  first-class part of the answer.
- **Secondary (sibling, deferred — seams only):** cache **"why was this missed?"**
  via diffing two runs, keyed on stable op identity.

Two complementary sources, never a migration: native wcprof (fine-grained,
engine-dev-only, uncollectable from CI) and OTel (coarser, already collected
everywhere, carefully augmentable). The goal is **not** parity and **not** analyzing
unmodified OTel: it is **enough explicit information that the analyzer never guesses
a causal relationship**, within OTel's real volume limits.

### 1.2 The anti-inference invariant

> The engine emits both **explicit causal edges** and **explicit descriptive
> identity/metadata** for every op. The analyzer **infers neither**. Its only jobs:
> **group** ops into *op-sets* by explicit, possibly user-supplied rules, and
> **simulate**.

- Bar = **"no more inference than native already does"** — not zero: native uses the
  replay's **implicit-join** for *synchronous parent→child* (drift-validated −0.0%).
  OTel inherits exactly this.
- Every cross-tree runtime dependency arrives as an **explicit, `wcprof.wait.*`
  windowed edge emitted at the wait site**. We never repurpose generic
  `link.purpose=cause` provenance links as runtime edges.
- Identity is explicit (argv/refs/kind/owner attrs); the analyzer never name-parses,
  the engine never decides two ops are "the same."

### 1.3 The bounded-target invariant (round-2)

> Every wait edge MUST target a **bounded** op whose end ≈ the moment the waiter
> unblocks. The replay only treats a wait as a join when `wait.End ≥ target.End − ε`
> (`replay.go:165`); a wait to a still-running/late-ending target silently becomes
> `actWaitNoop` and contributes nothing. So emitters must point waits at bounded
> start/acquire operations, not at lifetime spans.

Consequences, baked into §3–§4:
- service waits target a **bounded `service_start`** (start→readiness), **not** the
  long-lived service lifetime span.
- singleflight joins target a **synthesized bounded `call_exec`** (the shared
  execution interval), **not** the first caller's whole call span.
- the loader must keep these targets' `End` equal to the recorded unblock instant.

### 1.3a Availability ≠ work (round-4 follow-up)

> A long-running daemon (a service's *lifetime* span) is **availability, not work**:
> its wall-time is mostly idle-waiting-to-be-stopped, and its end is gated by teardown
> (everything finishing), not its own CPU. Native has **no daemon work-op** (only the
> bounded `service_start`); the lifetime span is an OTel-only artifact (UI/logs). So
> the loader does **not** import it as a scheduled work op — matching native's IR.
> Ranking is then **intrinsic**: a daemon shows as a bottleneck *only* if something is
> genuinely blocked on it on the critical path (a true finding), never merely because
> it ran a long time. Correct modeling, **not** a heuristic exclusion.

### 1.4 Settled decisions (do NOT relitigate; critique *within* them)

1. Anti-inference (§1.2) + bounded-target (§1.3).
2. **User work is first-class** — never filtered out.
3. **Augment, but respect OTel limits** — no "span everything"; `ShouldEmitTelemetry`
   suppression of repeated/cache-hit calls stays.
4. **Cache why-missed is a sibling, later** — seams only.
5. **Ingest target = Dagger Cloud trace API** — every run (local *and* CI) already
   forwards its telemetry to Cloud, so one source covers all. If Cloud lacks needed
   fidelity, the remedy is to **fix Cloud** (expand scope), not read engine internals.
   Accepted intermediate fallback: the **OTel stream the CLI itself receives/exports**
   (`cmd/dagger` gets the full telemetry; `otlpdump` taps exactly this for the dev
   loop). **NOT** engine `clientdb` SQLite — that is per-client storage *inside the
   engine's worker dir* (`srv.workerRootDir/clientdbs`), not a client-accessible source.
6. **Clock skew dropped**.

### 1.5 Two-graphs / one-IR

IR = `wcanalyze.Graph`: a forest of ops (structural parent/child) + edges,
**self-time = duration − child intervals − wait intervals**. Two edge sets:
- **Runtime causal/wait graph** — drives the counterfactual replay. From native wait
  events, or OTel `wcprof.wait.*` edges + nesting (never provenance links).
- **Cache-key input graph** — `dag.inputs`; drives the cache-diff sibling.

`graph.go` self-time + `replay.go` counterfactuals reused unchanged for the runtime
graph.

### 1.6 One hook set → two sinks

The augmentation lives at **exactly the choke points wcprof already instruments**.
At each, when telemetry is active, we *additionally* emit the OTel op/edge — sharing
the same values (bounded target, window, argv). The `wcprof` package stays
**OTel-free**; the OTel helper lives in a low-level package importable by both
`dagql` and `core` (§3.2 — *not* in `core`, which imports `dagql`).

### 1.7 Dependency audit (revised through round 2)

| Dependency | Native | OTel today | Plan |
|---|---|---|---|
| Sync parent→child call | implicit-join | span tree (`parentId`) **[empirical]** | inherit implicit-join |
| Caller→own execution | wait | call span nests resolver | inherit nesting |
| **Singleflight in-flight join** | wait | joiner span **suppressed** **[code]** | `wcprof.wait.*` link → **synthesized bounded `call_exec`** (R-B, R-O) |
| **Lazy eval wait/join** | wait | `lazyResumeLinks` are **provenance** **[code]** | explicit windowed wait edge at `evaluateOne`, target = **resume span** (bounded; R-P capture) |
| **Service-start wait** | wait | `serviceOriginLink` are **provenance**; lifetime span is long-lived **[code]** | explicit wait edge → **bounded `service_start` span** (R-O) |
| **Cache-volume lock wait** | wait (ident) | absent **[code]** | fixed windowed **event** by lock key → `actWaitFixed` |
| Exec wait (caller→executor) | wait | exec under caller (after R-C) | inherit nesting |
| **Nested-client boundary** | explicit link | propagation uses `causeCtx` **[code]** | exec span = propagation parent; `causeCtx` kept as **log-target + provenance** (R-C split) |
| Op classification (kind/work/owner) | fields | partial | explicit attrs (R-E) |
| Op identity (argv…) | partial | partial | structured metadata (R-D/R-Q) |

Round-1/2 correction tags used below: **R-A** provenance-vs-runtime · **R-B/R-O**
bounded targets (call_exec, service_start) · **R-C** exec span + propagation/log
split · **R-D** exec setup/process split as child ops with typed fields · **R-E**
owner-vs-work · **R-F** CI root scheduling · **R-G** loader dedup · **R-H** 128 caps ·
**R-I** Cloud spike first · **R-P** lazy span capture · **R-Q** processRun metadata
inheritance · **R-helper** package boundary.

---

## 2. Background the reviewer should verify first

`engine/wcprof/{record,wcprof,dump}.go`; `engine/wcprof/wcanalyze/{graph,replay,
report}.go` (esp. `joinUpTo`, and **`replay.go:165`** `actWaitJoin` gating + `Run`
root chaining ~263–296); `dagql/cache.go` (`getOrInitCall`/`wait`/`evaluateOne`;
`lazyResumeLinks`; the lazy callback-ctx wrapper whose `SpanContext()` returns the
**install** span, ~2797/2990); `dagql/telemetry.go` (`ShouldEmitTelemetry`);
`core/telemetry.go` (`AroundFunc`, done callback ~138); `core/services.go`
(`serviceOriginLink`, `Services.Get` ~326, `startWithKey` ~955/971/1026);
`core/service.go` (service span ~748/900/912; `causeCtx` set for **log routing**
~815–826); `core/container_exec.go` (`causeCtx := trace.SpanContextFromContext(ctx)`
~1304; lock wait ~559); `engine/engineutil/executor.go` (exec choke point in `Run`;
cleanup defer ~169) + `executor_spec.go` (`state.causeCtx` used by `setupOTel` for
**both** traceparent and `SpanStdio` ~751/767; `setupNestedClient` ~1013;
`"Container started"` ~started callback; native `exec.processRun` ends right after
`callWithIO` ~1398); `engine/clientdb/schema.sql` ("duplicates … append-only") +
`span.go` (`DroppedLinks/Events` ~169); `engine/server/telemetry.go` (`InsertSpan`,
preserves dropped counts ~347); `github.com/dagger/otel-go/attrs.go`; OTel SDK
`trace/span_limits.go` (`DefaultLinkCountLimit = DefaultEventCountLimit = 128`).

Empirical (`otlpdump`, small repo-workspace `dagger query`): 1764 spans; per-call
spans carry `dag.digest` (= wcprof ident), `dag.inputs`, `dag.call`, `ui.internal`;
coherent cross-process tree; `resume *` spans carry provenance `cause` links;
leaf-I/O spans exist; **no** engine-side exec/phase spans.

---

## 3. Implementation plan — engine augmentation (emit side)

Active only when telemetry is on; independent of `wcprof.Enabled(ctx)` unless noted.
Does not touch the wcprof recorder or its disabled-path cost.

### 3.1 New attribute vocabulary

**New file:** `engine/telemetryattrs/wcprof.go` (low-level, already imported by
`dagql` and `core`).

```go
package telemetryattrs

// classification (R-E: two axes)
const (
	WcprofKindAttr  = "dagger.io/wcprof.kind"  // call|exec|lazy|service_start|service_lifetime|io|session|internal
	WcprofWorkAttr  = "dagger.io/wcprof.work"  // engine|user_process|external|availability  (user_process RESERVED for exec process time; availability = daemon/idle, never scheduled work — §4.6)
	WcprofOwnerAttr = "dagger.io/wcprof.owner" // engine|user|sdk
)

// identity / description
const (
	WcprofExecArgvAttr  = "dagger.io/wcprof.exec.argv"   // []string, bounded + scrubbed
	WcprofExecExitAttr  = "dagger.io/wcprof.exec.exit"   // int64
	WcprofExecImageAttr = "dagger.io/wcprof.exec.image"  // string, when known
	WcprofIOTargetAttr  = "dagger.io/wcprof.io.target"   // image addr / git ref / host path
)

// explicit runtime wait edges (the ONLY thing the loader treats as runtime causality)
const (
	WcprofWaitReasonAttr = "dagger.io/wcprof.wait.reason" // singleflight|lazy|service
	WcprofWaitStartAttr  = "dagger.io/wcprof.wait.start_unix_nano"
	WcprofWaitEndAttr    = "dagger.io/wcprof.wait.end_unix_nano"
	// resource (lock) waits: no target span → a span EVENT
	WcprofLockWaitEvent = "dagger.io/wcprof.lock_wait"
	WcprofLockKeyAttr   = "dagger.io/wcprof.lock.key"
)

// bounded shared-execution interval for singleflight (R-O/R-T): carried on the
// JOINER'S wait link (NOT the first-caller span, which may end early under
// context.WithoutCancel — dropping late attr writes). The loader synthesizes a
// bounded call_exec op from it and reparents the resolver's in-window children
// under it. No per-execution call_exec SPAN (would be one extra span per call).
const (
	WcprofCallExecStartAttr = "dagger.io/wcprof.call_exec.start_unix_nano"
	WcprofCallExecEndAttr   = "dagger.io/wcprof.call_exec.end_unix_nano"
)

// precise exec process window (R-D/#3): "Container started" already exists; add an
// explicit process-end so processRun excludes executor cleanup.
const WcprofProcessEndAttr = "dagger.io/wcprof.exec.process_end_unix_nano" // or a "Container exited" event
```

### 3.2 Explicit, BOUNDED, windowed wait edges at the wait sites (R-A + R-O)

**Helper location (R-helper):** `addWaitEdge` must be callable from **both**
`dagql/cache.go` and `core` services. `core` imports `dagql`, so the helper cannot
live in `core`. Put it in a low-level package already importable by `dagql` (which
uses `engine/telemetryattrs` today) and by `core` — e.g. `engine/telemetryattrs`
(it only needs `go.opentelemetry.io/otel/{trace,attribute}` + the attr keys, no
`dagql`/`core` deps).

```go
// engine/telemetryattrs (or sibling low-level pkg)
func AddWaitEdge(ctx context.Context, target trace.SpanContext, reason string, startUnixNano, endUnixNano int64) {
	if !target.IsValid() { return }
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() { return }
	span.AddLink(trace.Link{SpanContext: target, Attributes: []attribute.KeyValue{
		attribute.String(WcprofWaitReasonAttr, reason),
		attribute.Int64(WcprofWaitStartAttr, startUnixNano),
		attribute.Int64(WcprofWaitEndAttr, endUnixNano),
	}})
}
```

`AddWaitEdge` is the base helper. Singleflight additionally needs the execution
interval on the link, so it uses a variant (`AddSingleflightWaitEdge`) that also
appends `WcprofCallExecStart/EndAttr` (R-T). Other sites use the base helper.

**(a) Singleflight join** — `dagql/cache.go` `wait(... joined bool)`. **R-B/R-O/R-T/R-U.**
The target must be a **bounded** op covering only the shared execution, with the
resolver's children adopted under it.

- `ongoingCall` stores `execStartUnixNano int64` (set when `fn(oc.sharedWorkCtx)`
  begins) and `execSpanCtx trace.SpanContext` (the first caller's call span).
- **R-T (cancellation-safe carrier):** the shared execution is detached with
  `context.WithoutCancel` (`dagql/cache.go:3667`), so the first caller may cancel and
  **end its call span while the execution continues** for other waiters — and OTel
  drops attribute writes after span end. So the execution interval is carried on the
  **joiner's own (live) wait link**, not the first caller's span. On unblock the
  joiner knows `execEnd` (when `oc.waitCh` closes) and reads `oc.execStartUnixNano`,
  then adds a link to `oc.execSpanCtx` tagged `reason=singleflight`, the joiner's
  blocked window `[joinStart, execEnd]` (`WcprofWaitStart/EndAttr`), **and** the
  execution interval `[execStart, execEnd]` (`WcprofCallExecStart/EndAttr`).
- `call_exec` is synthesized **only when a joiner exists** (a real in-flight
  collision); the common no-collision case needs none.
- **Loader (R-O/R-U):** synthesize a bounded `call_exec` op `[execStart, execEnd]`
  under `execSpanCtx`'s op, **reparent** that call span's direct children whose
  intervals fall within the window under `call_exec` (a true structural wrapper like
  native — otherwise `call_exec` self-time would absorb the children's work *and* act
  as a fixed self-floor masking their counterfactual savings), and **retarget** the
  singleflight wait edge to `call_exec` (so `wait.End ≈ call_exec.End`, satisfying
  `replay.go:165`).

Zero extra spans; the replay gets a correctly-bounded, counterfactually-responsive
join target.

**(b) Lazy eval wait** — `dagql/cache.go` `evaluateOne`, at both existing
`wcprof.BeginWait(... WaitReasonLazy)` sites. The **lazy resume span** is a bounded
target (it ends ~when eval completes / waiters unblock), so it can be the wait
target directly. **R-P:** capture `resumeSpan.SpanContext()` **directly at span
creation** and store it on `sharedResult` — do **not** derive it later from the
callback ctx, whose `SpanContext()` deliberately returns the *install* span
(`dagql/cache.go` ~2797/2990). Waiter emits `AddWaitEdge(ctx, lazyResumeSpanCtx,
"lazy", waitStart, waitEnd)`. Existing `lazyResumeLinks` provenance links untouched.

**(c) Service-start wait** — `core/services.go` `Services.Get`/`startWithKey`. **R-O:**
the existing service span is **lifetime-scoped** (ends at service exit) — unusable as
a join target (`replay.go:165`). Emit a **bounded `service_start` span** around the
start→readiness work (the interval that ends when `starting.done` closes), store its
context on `startingService` immediately, and have waiters
`AddWaitEdge(ctx, serviceStartSpanCtx, "service", waitStart, readyTime)`. Keep the
existing lifetime span classified `service_lifetime` but **not imported as a runtime
work op** (R-S / §1.3a): a daemon's lifetime is *availability, not work*, and native
has no daemon work-op (only the bounded `service_start`). Importing it would both
corrupt self-time/joins (`graph.go:339` subtracts child intervals beyond the parent;
`replay.go:353` won't join a late-ending child) **and** fool the counterfactual (a
daemon is often the last span to end → falsely looks makespan-determining). It is kept
only in a provenance view; service ranking is then intrinsic (a service ranks only if
something truly blocks on it).

**(d) Cache-volume lock wait** — `core/container_exec.go` (existing
`BeginWaitIdent(... WaitReasonLock)`). No holder span is known (only the key
**[code]**); emit a **span event**:

```go
trace.SpanFromContext(ctx).AddEvent(telemetryattrs.WcprofLockWaitEvent,
	trace.WithAttributes(
		attribute.String(telemetryattrs.WcprofLockKeyAttr, key),
		attribute.Int64(telemetryattrs.WcprofWaitStartAttr, startUnix),
		attribute.Int64(telemetryattrs.WcprofWaitEndAttr, endUnix)))
```

Loader → `actWaitFixed` by key (exactly how native models lock waits). Holder→waiter
edges need a separate owner registry (out of scope).

### 3.3 Exec span (first-class) + propagation/log split (R-C) + process window (R-D)

Add **one span per exec** at the executor choke point (`Run`); **not** per-phase
spans.

```go
// engine/engineutil/executor.go, in Run, alongside the existing wcprof execOp:
argv := procInfo.Meta.Args              // [code]
work, owner := telemetryattrs.WorkUserProcess, telemetryattrs.OwnerUser
if execMD != nil && execMD.Internal {   // [code]
	work, owner = telemetryattrs.WorkEngine, telemetryattrs.OwnerSDK
}
ctx, execSpan := Tracer(ctx).Start(ctx, execSpanName(argv, execMD),   // R-K: conservative name
	telemetry.Internal(),                                            // R-V: ui.internal — hidden in TUI/Cloud, still EXPORTED for the analyzer
	trace.WithLinks(causeLink(causeCtx)),                            // causeCtx → provenance link
	trace.WithAttributes(
		attribute.String(telemetryattrs.WcprofKindAttr, "exec"),
		attribute.String(telemetryattrs.WcprofWorkAttr, work),
		attribute.String(telemetryattrs.WcprofOwnerAttr, owner),
		attribute.StringSlice(telemetryattrs.WcprofExecArgvAttr, scrubArgv(argv, execMD)),
	))
defer func(){ execSpan.SetAttributes(attribute.Int64(telemetryattrs.WcprofExecExitAttr, exitCode)); telemetry.EndWithCause(execSpan, &rerr) }()
```

**R-V — present in data, hidden in UI (Erik approval).** The exec span is tagged
`telemetry.Internal()` (`ui.internal`): `dagui` marks it `Internal` and `Hidden()`
suppresses it below `ShowInternalVerbosity` (`dagql/dagui/spans.go` **[code]**), but
the span is still **exported** (internal is a render hint, not an export filter) — so
the analyzer gets it while the TUI/Cloud show no new row. With the exec span hidden the
TUI renders nested work under the exec's nearest visible ancestor (≈ the cause, as
today); the analyzer reads the exec as the structural parent. Per Erik: keep the span,
just don't surface it visibly.

**R-C — split propagation parent from log target (round-2 #4).** Today `causeCtx`
serves **two** roles in `setupOTel`: (i) the traceparent propagated into the
container/nested client, and (ii) the active span for `SpanStdio`, which **routes
service/exec logs to the installing API call's row** (`core/service.go` ~815–826,
`executor_spec.go` ~767 **[code]**). We want the **exec span** as the propagation
parent (so nested-client spans nest under it) but must **preserve** `causeCtx` for log
routing **and error-origin tracking** (`core/exec_error.go`, `core/container_exec.go`
~2027). Split executor state:

```go
type execState struct {
	// ...
	propagationParent trace.SpanContext // NEW: exec span ctx → traceparent + nested client
	logTarget         trace.SpanContext // = old causeCtx → SpanStdio log routing, error-origin tracking, provenance link
}
// setupOTel: traceparent uses propagationParent; SpanStdio uses logTarget.
// setupNestedClient: uses propagationParent.
```

Result: nested-client spans nest under the exec span via traceparent (pure nesting,
no analyzer reparenting), while service/exec logs still attach to the installing call
(no UI regression).

**R-D — precise exec process window (round-2 #3).** Native ends `processRun` right
after `callWithIO` returns (`executor_spec.go` ~1398), **before** executor cleanup
(`c.run` defer ~169). The exec span ends *after* cleanup, so using its end for
`processRun` charges cleanup/telemetry-shutdown to `user_process`. Emit an explicit
**process-end** marker at the native point. **Phase-0 finding:** the trace **already
emits** `"Container created"` / `"Container started"` / `"Container exited"` events
(confirmed in the live Cloud trace), so the markers largely exist already — verify
`"Container exited"` fires at the `callWithIO`-return point (and add
`WcprofProcessEndAttr` only if its timing is off). The loader (§4.5) then builds
`processRun = [started, exited]` and leaves `[exited, execEnd]` as engine cleanup.

> **R-K:** keep argv **out of the span name** (names are more exposed / hard to
> scrub). Conservative name (executable basename or `"exec "+callDigest`); bounded,
> scrubbed argv only in `WcprofExecArgvAttr` (honor `execMD.SecretEnvNames/
> SecretFilePaths`). argv is already in telemetry via `dag.call`.

> **Review-me 3.3-vol / R6:** the exec span is added to **normal** telemetry (one per
> exec) — a TUI/Cloud surface change; the R-C propagation change moves nested work
> under it. Intended (better tree) but a product surface change. **Flag for Erik.**

### 3.4 dagql call + io/service descriptive metadata (light touch)

- **call spans** (`AroundFunc`): add `WcprofKindAttr="call"`, `WcprofWorkAttr=
  "engine"` (R-E: a call span's self-time is glue even for module calls),
  `WcprofOwnerAttr` (`user` when `req.Module!=nil`, else `engine`). The
  `WcprofCallExecStart/EndAttr` interval attrs are **not** set here — they ride the
  joiner's wait link (R-T). Existing `dag.*`/module attrs unchanged.
- **io spans** (already exist): `kind=io`, `work=external`, `WcprofIOTargetAttr`.
- **service spans:** the bounded one `kind=service_start`; the lifetime one
  `kind=service_lifetime`.

### 3.5 What we deliberately do NOT emit (volume balance)

No span per cache-hit/suppressed-repeat; **no `call_exec` span** (cheap attrs +
loader synthesis instead, R-O); no span per micro singleflight join (a link, only on
real in-flight joins); no per-phase exec spans (events cover the splits); no
provenance-link repurposing (R-A); engine never groups/classifies. Respect the **128
link/event cap** (R-H): the singleflight join-link rides the joiner's parent span —
bound fan-in there.

---

## 4. Implementation plan — ingest + loader (consume side)

### 4.1 Source-agnostic span representation + dropped counts (R-G + R-H)

**New package:** `engine/wcprof/wcotel`.

```go
type ProfSpan struct {
	TraceID, SpanID, ParentID string
	Name, Scope               string
	StartUnixNano, EndUnixNano int64 // End==0 ⇒ live/incomplete record
	Attrs  map[string]any
	Links  []ProfLink
	Events []ProfEvent
	// R-H: dropped counts must travel so the loader can warn/fail on wcprof-relevant loss.
	DroppedAttrs, DroppedLinks, DroppedEvents int
}
type ProfLink  struct { TraceID, SpanID string; Attrs map[string]any }
type ProfEvent struct { Name string; UnixNano int64; Attrs map[string]any }
```

**R-G — dedup:** the engine **live-exports** spans (a start record, then a final
record on completion — the mechanism behind `OTEL_EXPORTER_OTLP_TRACES_LIVE`; the
`clientdb/schema.sql` *"duplicates … append-only"* note is evidence the span *stream*
carries multiple records per span). So any received stream has duplicates. The loader
**dedups by `(TraceID,SpanID)`**, keeps the record with the largest `End` (the final
OTLP export is a complete snapshot, so it supersedes the on-start record — no field
merging needed), and surfaces still-incomplete spans (`End==0`) as `Open`.

Sources → `[]ProfSpan` (all yield the same OTLP spans):
- **Dagger Cloud trace API** — the primary product target; the Phase-0 spike (§9/R-I)
  must confirm it returns links, events, custom attrs, **and dropped counts**.
- **The CLI-received OTel stream** — the accepted intermediate fallback: `cmd/dagger`
  receives the full telemetry from the engine and re-exports it, so an in-process tap
  (or an `otlpdump`-style local collector the CLI exports to) yields the same spans.
- **`otlpdump` JSONL** — the dev-loop/test instance of the CLI-received stream (extend
  it to carry dropped counts, or mark that diagnostic unsupported there).

**We do NOT read engine `clientdb` SQLite** — engine-internal per-client storage
(`srv.workerRootDir/clientdbs`), not client-accessible. (The native wcprof dump used
for cross-source validation comes from the engine's `/debug/wcprof/dump` *debug
endpoint* on a dev engine — not from clientdb.)

### 4.2 `LoadOTel`: spans → `Graph`

`wcanalyze.LoadOTel(spans []wcotel.ProfSpan, opts LoadOpts) (*Graph, error)`, sibling
to `Load`/`LoadMulti`; emits the same `*Graph`.

```go
func opID(traceID, spanID string) uint64 { return hash64(traceID + ":" + spanID) } // R-J: multi-trace safe

op := &Op{
	ID: opID(s.TraceID, s.SpanID), ParentID: parentOpID(s),
	Kind:     kindOf(s),                          // explicit attr; missing ⇒ "unknown"+diag (R-L)
	WorkType: attrStr(s, WcprofWorkAttr, ""),
	Class:    s.Name, Ident: attrStr(s, DagDigestAttr, ""),
	StartNS:  s.StartUnixNano, EndNS: max(s.EndUnixNano, s.StartUnixNano),
	Meta:     buildMeta(s),                       // argv, image, exit, io.target, owner, module ref
}
```

- **Runtime edges (R-A):** only `WcprofWaitReasonAttr` links → `WaitEdge`s (window
  from the wait attrs); `WcprofLockWaitEvent` events → `actWaitFixed`. Generic
  `link.purpose=cause` ignored for the runtime graph.
- **Bounded-target synthesis (R-O/R-U):** for each `singleflight` wait link (carrying
  `WcprofCallExecStart/EndAttr`), synthesize a bounded `call_exec` op
  `[execStart, execEnd]` under the linked call span, **reparent** that call span's
  direct children **fully contained** in `[execStart, execEnd]` (± a small epsilon)
  under `call_exec` (true structural wrapper — else `call_exec` self-time absorbs the
  children and masks their counterfactuals; partially-overlapping children stay put
  and emit a diagnostic), and **retarget** the wait edge to `call_exec`. **Idempotent:**
  multiple joiners of the same execution carry equivalent links, so group by
  `(target span, execStart, execEnd)` and synthesize **one** `call_exec`, retargeting
  all those wait edges to it. Only when a joiner exists.
- **Availability ≠ work (R-S / §1.3a):** daemon lifetime spans (`service_lifetime`,
  etc.) are *availability, not work* and have no native work-op counterpart, so the
  loader does **not** import them as scheduled work ops (matching native's IR) —
  retaining them only in a provenance view. Correct modeling, not a ranking band-aid:
  the counterfactual then ranks them intrinsically (importing them would corrupt
  self-time/joins per `graph.go:339`/`replay.go:353` **and** fool makespan via their
  teardown-gated end).
- **Cache input edges:** `dag.inputs` → `Op.InputDigests` (sibling only).
- **Exec split (R-D / §4.5).**
- **R-H diagnostics:** if `DroppedAttrs/DroppedLinks/DroppedEvents>0` on a
  wcprof-relevant span (one carrying wait/call_exec/kind/work attrs), warn loudly (or
  fail) — the graph may be missing edges *or* metadata (classification and `call_exec`
  synthesis both depend on attrs).

> **R-L:** `kindOf` uses explicit `WcprofKindAttr`; missing ⇒ `"unknown"` + a
> diagnostic count, **not** a scope/name guess — except an explicit `--backfill` mode
> may apply a fixed scope/name→kind table to pre-augmentation traces.

### 4.3 IR changes (`graph.go`)

```go
type Op struct {
	// ... existing (incl. WorkType, Ident, Outcome) ...
	Meta         map[string]string // descriptive identity (argv, image, exit, owner, io.target, module)
	InputDigests []string          // dag.inputs; cache sibling only
}
```

> **R-M — native event-size tension.** `wcprof.Event` is fixed-size; rich `Meta`
> (argv) doesn't fit. **Decision: rich `Meta` is OTel-source-primary** (native stays
> lean: Class/Ident). If native later needs argv-based user-classes, add one interned
> `MetaID uint32`. Defer.

### 4.4 Causal edges in the loader — no inference, no provenance repurposing

1. **Nesting** → sync parent→child via `ParentID` (implicit-join).
2. **`wcprof.wait.*` links** → bounded `WaitEdge`s (after R-O retargeting).
3. **`wcprof.lock_wait` events** → `actWaitFixed` by key.
4. **Nested-client** → nothing (R-C makes traceparent nest it).

### 4.5 Exec split as synthetic child ops with TYPED fields (R-D + R-Q)

For an exec op with `"Container started"` at `tStart` and process-end at `tEnd`
(`min(processEnd, execEnd)`), synthesize **two child ops, setting the analyzer's
typed fields (not just `Meta`)** — round-2 #6:

```go
containerStart := &Op{
	Kind:"exec_phase", Class:"exec.containerStart", Parent: execOp,
	StartNS: execOp.StartNS, EndNS: tStart,
	WorkType: "engine",                                  // TYPED field (report/replay read Op.WorkType)
	Ident: execOp.Ident, Outcome: execOp.Outcome,
}
processRun := &Op{
	Kind:"exec_phase", Class:"exec.processRun", Parent: execOp,
	StartNS: tStart, EndNS: tEnd,
	WorkType: "user_process",
	Ident: execOp.Ident, Outcome: execOp.Outcome,
	Meta: inherit(execOp.Meta, "argv","image","owner"), // R-Q: so `argv^=go build` matches the USER-PROCESS op
}
// [tEnd, execEnd] remains the exec op's engine cleanup self-time.
```

`processRun` **must** carry argv/owner so user class-rules target the op that
actually holds the user time. `SelfSegments` is unchanged (children subtract from the
exec op). **Service/daemon exception:** a service exec's `processRun` runs until
teardown — *availability, not user work* — so it is classed `availability`, not
`user_process` (see §4.6).

### 4.6 Availability elision — descendant policy (R-S, round-5)

A daemon lifetime span (`service_lifetime`) is **not a leaf**: its descendants include
the service's container-start + healthcheck (real *readiness* work) and the daemon's
own long-running `exec.processRun` (availability). "Don't import the lifetime span" is
insufficient without saying what happens to those descendants. The loader applies:

- **The availability node itself** → not a scheduled work op (§1.3a); retained in the
  provenance view (`--show-availability`).
- **Descendants within the readiness window** (`[service start → ready]`, e.g.
  container-start + healthcheck) → **reparent to the bounded `service_start` op**: this
  *is* the readiness work waiters block on, and it must rank if it's slow.
  **Emission invariant (chunk 5):** `service_lifetime` is emitted **under**
  `service_start`, so readiness descendants reparent to `service_start` via the
  loader's nearest-non-availability-ancestor rule — **no ident correlation or
  inference** (an explicit `cause`/parent marker, not a guess).
- **Post-readiness daemon descendants** (the service container's `exec.processRun`
  running until teardown) → **availability overlay, not scheduled work**
  (`work = availability`). The engine tags service execs (launched via
  `core/service.go startContainer`), so the §4.5 split classes their `processRun`
  availability — a daemon never becomes a giant phantom `user_process` bottleneck.
- **Partial / unknown overlaps** → availability overlay **+ a diagnostic** (never a
  silent reparent of an ambiguous span).

Net: a service's *readiness* work is attributed to `service_start` (and ranks iff it's
a real bottleneck); its *daemon runtime* is availability (never a phantom bottleneck);
makespan is unaffected by teardown timing. This is the concrete realization of §1.3a.

---

## 5. Implementation plan — analyzer generalization (source-agnostic)

Operates on `Op` fields both sources populate. Works on existing native dumps day one.

### 5.1 Op-sets: the unifying primitive

```go
// wcanalyze/opset.go (new)
type OpSet struct { Name string; Members func(*Op) bool }
//  DefaultClassOpSets(g) | SubtreeOpSet(rootOp) | PredicateOpSet(rule) | SingletonOpSet(op)
```

**User-defined classes are an explicit input** (invariant applied to identity):

```go
type ClassRule struct { Name string; Match []MetaMatch } // ANDed
type MetaMatch struct { Field, Op, Value string }        // Field: argv|class|kind|work|owner|module|io.target ; Op: eq|prefix|contains (glob dropped in impl — path-glob mishandles '/')
```

*"all my go builds"* = `{argv prefix "go build"}` (matches the `processRun` ops, R-Q);
a subtree/umbrella = `SubtreeOpSet`; individual = singleton. All feed one engine.

### 5.2 What-ifs over op-sets

```go
func RunWhatIfsForOpSets(g *Graph, sets []OpSet, factors []float64) ([]WhatIfResult, error)
```

Replay change: `Simulation.factorOf` becomes **per-op** (scaled-set members get `f`,
else `1`); compile-once flat program unchanged. Candidates: default classes (≤200),
subtrees (each `dagger call`/test/`asModule`), user rules, top-N individual ops by
self-time + on-demand.

> **R-N:** op-sets may overlap; each scaled **independently** (no simultaneous
> multi-set — out of scope). Bound `defaultClasses+subtrees+userRules+topK` and
> document the cost (one replay/factor, parallelized; ~74s on 9.7M events today) so
> user rules can't DoS the analyzer.

### 5.3 Report — user work first-class; role is a summary, never a filter

Top **individual ops** by makespan impact (incl. user `processRun` execs — *"`go
build` … 38s of 51s"*); top **default classes**; **user-defined classes**; a
**work/owner breakdown summary**. **Availability ops (R-S / §1.3a):** daemon lifetime
spans (`service_lifetime`, etc.) are *availability, not work* and are **not imported as
runtime work ops** (matching native, which has no daemon work-op). This is correct
modeling, **not** an explicit ranking exclusion — the counterfactual ranks them
**intrinsically**: a service shows as a bottleneck *only* if something is genuinely
blocked on it on the critical path (a true finding, surfaced normally), never merely
for running a long time. The lifetime span is available for context in a provenance
view via `--show-availability`.

`cmd/wcprof-analyze` flags: `--source`, `--from=cloud|otlpdump`, `--trace`,
`--root-mode=cli|trace`, `--class-rule …`, `--show-availability`.

### 5.4 CI-trace root & makespan (R-F)

The CLI root-chaining (`replay.go Run` ~263–296) is correct for sequential CLI dumps
but **wrong for CI** (independent roots could be artificially coupled, inventing
savings). Add `--root-mode`:
- `cli` (default for native): existing chaining.
- `trace` (OTel/CI): **anchor every root at its recorded absolute offset; no
  shift/coupling**; optionally a synthetic super-root for makespan; handles multiple
  traces (R-J).

`makespan = max(root.end) − min(root.start)`. Intra-root parallelism already
preserved.

> **R-F/R-I (make-or-break):** whether a real Cloud CI trace is a coherent navigable
> tree (few roots, intact CLI→engine→nested nesting) **must be validated against a
> real trace first** (Phase 0).

---

## 6. Cache "why-missed" sibling (seams only — deferred)

Same loader + stable identity (`Ident`=`dag.digest`); consumes `Op.InputDigests`
(`dag.inputs`); analysis = **diff across two runs** (match by digest; differing
inputs ⇒ the invalidation), not a replay. IR already carries both edge sets + stable
identity. `dag.digest`/`dag.inputs` survival hardening deferred with it.

---

## 7. Risks & dependencies (ranked)

- **R1 [VERIFIED ✅ — Phase-0 spike, live Cloud trace] — Cloud trace API fidelity.**
  The `spansUpdated` GraphQL subscription (`internal/cloud/trace.go`) returns — and a
  live fetch of a real trace confirmed round-trip of — a **coherent single-root tree**
  (`parentId`), **span links + attributes** (incl. `link.purpose=cause`), **span
  events** (incl. `Container started`/`exited`/`created`), and **custom `dagger.io/*`
  attrs** (`dag.digest`/`dag.inputs`/`dag.cached`/…, plus `ui.internal` — confirming the
  hidden-exec-span R-V round-trips). **Residual gap (minor):** the fragment does **not**
  expose dropped-counts (`droppedLinks/Events/Attributes`) — only `partial` +
  `childCount` — so the R-H dropped-count diagnostic can't run on Cloud data as-is
  (small backend+client add if the Cloud `Span` type carries them). Not a blocker; core
  fidelity is present. (If Cloud ever regresses: remedy is **fix Cloud**; stopgap is the
  CLI-received OTel stream — **never** engine `clientdb`.)
- **R2 [code] — 128 link/event cap (R-H).** Raise limits on the dagger-owned tracer
  provider; bound join-link fan-in on hot parent spans; loader warns/fails on dropped
  counts on wcprof-relevant spans.
- **R3 [open] — CI root coherence & cross-process nesting** (R-F; covered by R1 spike).
- **R4 [decided]** native rich-Meta = OTel-source-primary (R-M).
- **R5 [code]** nested-client + log routing both rode `causeCtx`; R-C splits them.
  One empirical check post-change that nested spans nest under the exec AND service
  logs still attach to the installing call.
- **R6 [open]** exec span + propagation change = normal-telemetry surface change
  (§3.3); product call.
- **R7 [code] — bounded targets** (R-O): service_start/call_exec must end at the
  unblock instant or the replay no-ops the wait (`replay.go:165`). Validate the
  synthesized intervals match the recorded unblock times.
- ~~Clock skew~~ — dropped.

---

## 8. Validation & testing

> First-class, **not** an afterthought. The original wcprof's validation earned its
> keep — the simulated-vs-actual drift gate alone caught three real replay-model bugs
> during bring-up. The OTel source adds new failure modes (loader mapping,
> augmentation correctness, suppression/dedup, bounded-target synthesis, CI-trace
> shape) and **will** surface issues. Six tiers + a standing gate.

### 8.0 Standing fidelity gate — simulated-vs-actual drift (every run)

The report already prints simulated baseline makespan vs actual makespan
(`report.go`); native reached **−0.0%** after its model bugs were fixed. For the OTel
source this is the first-line automatic correctness signal — a large drift means the
loader/model is wrong (mis-bounded targets, bad dedup, lost edges). CI asserts drift
within a threshold on the known workloads below; treat a drift regression as a bug.

### 8.1 Tier 1 — unit + golden (component correctness)

- **Loader** (hand-crafted `ProfSpan` fixtures → expected `Graph`): live-record dedup
  (start+final → final); op-ID keying by `(trace,span)` incl. multi-trace;
  kind/work/owner mapping; `wcprof.wait.*` link → `WaitEdge` w/ window; lock event →
  fixed delay; **`call_exec` synthesis** (bounded interval, in-window child
  reparenting, partial-overlap diagnostic, idempotent grouping); **exec child
  synthesis** (containerStart/processRun boundaries from started/exited events, typed
  `WorkType`, argv inheritance); `service_start` vs availability `service_lifetime`
  **including the hard case** (an availability span with bounded readiness children +
  a long daemon `processRun` child + teardown near trace end → assert: lifetime not a
  work op/root; readiness children reparented to `service_start`; daemon `processRun`
  classed availability and unranked; teardown timing doesn't move makespan — §4.6);
  dropped-count diagnostics fire.
- **Op-sets**: predicate matching (argv prefix/glob, kind/work/owner/module), subtree
  membership, default-class grouping, overlap independence, per-op factor application.
- **Replay** (extend `replay_test.go`): OTel-graph fixtures asserting bounded-target
  join semantics — `actWaitJoin` fires when `wait.End ≈ target.End`, no-ops otherwise;
  reparented `call_exec` propagates child scaling.
- **Golden snapshots**: small fixed OTel traces → checked-in expected
  ops/edges/self-times, to catch silent mapping regressions.

### 8.2 Tier 2 — cross-source equivalence (the oracle)

The strongest validation, unique to this project: **native wcprof is an already-
validated oracle** (the original PR). Run the *same* workload on a dev engine with both
enabled; compile the native dump (`/debug/wcprof/dump`) **and** the CLI-received OTel
(`otlpdump`) to `Graph`s; compare.

- **Concrete comparison (so it's not "by interpretation"):** a **checked-in
  native↔OTel class-mapping table** (e.g. native `exec_phase:exec.processRun` ↔ OTel
  `exec.processRun`; native `call:<Type.field>` ↔ OTel `call:<Type.field>`; …) + an
  **explicit exclusion list** for classes with no counterpart (OTel-suppressed
  repeats/cache-hits; native-only finest exec phases; OTel-only leaf-I/O). Ignore
  classes below the analyzer's `min-self` floor. (The mapping + exclusions are needed
  for *any* meaningful comparison — not optional.)
- **Rough sanity bounds (guidelines, not hard gates yet):** top bottlenecks should
  largely agree; makespan should be close; per-mapped-class self-times in the same
  ballpark. **Large divergence ⇒ stop and look** (a loader/augmentation bug, or a
  documented granularity difference). Run on ≥2 workloads (`engine-dev container sync`;
  a multi-test run); firm thresholds for CI gating come later, once real numbers show
  what's achievable.
- **Cloud↔CLI-stream parity (R1 guard):** for one controlled run, capture **both** the
  Cloud API trace and the CLI/`otlpdump` stream, canonicalize both to `ProfSpan`, and
  assert equality of span IDs, attrs, links, events, dropped counts, and root structure
  (modulo documented Cloud transforms). A *standing* test — not just the Phase-0 spike
  — so Cloud silently dropping links/events/attrs (R1) is caught.

### 8.3 Tier 3 — known-answer injection (end-to-end, as in the original)

Reuse the original technique — file-gated artificial delays — but validating the
**OTel** path. Inject a known delay into a specific, identifiable op and assert the
OTel-sourced analyzer finds it:

- targets: a user exec (`sleep` in a `withExec` → assert the `argv`-class/op ranks #1,
  predicted save ≈ injected×count); an engine phase (lazy/filesync); a
  singleflight-shared execution (assert joiner branches show the dependency); a service
  start.
- **the discriminations that matter:**
  - **serial, on the critical path** → ranked #1, predicted ≈ injected.
  - **parallel / off the critical path** → correctly **not** ranked (the
    total-time≠bottleneck discrimination the tool exists for).
  - **disarm** → the class drops out of the rankings entirely (no false positive).
  - **background daemon** with injected post-readiness busy-time → correctly **not**
    ranked (availability ≠ work, §4.6); but a delay injected into its *readiness* (so a
    waiter blocks longer) → **does** rank via `service_start` (a true finding). The
    availability discrimination.
- Each case has a *known* answer, exercising the full emit→ingest→analyze path.

### 8.4 Tier 4 — prediction-vs-actual (does the suggested fix actually help)

The ultimate test, kept deliberately **lightweight** (per Erik — don't over-engineer
this, and don't make it slow): the analyzer predicts "fixing X saves ~Y"; make the
change (or disarm the injection), re-run, and check the actual reduction roughly
matches.

- **Noise:** run each scenario **a couple of times** (2–3) to get a feel for the
  run-to-run spread — *not* a formal noise study.
- **Check:** does the actual reduction roughly line up with the prediction given that
  spread? If yes, good.
- **On a mismatch → stop and look.** A miss is an *investigation trigger*, not an
  automated pass/fail gate: go see whether it's plausibly noise or a real model bug.
  (When in doubt, re-run the counterfactual on the *same recorded trace* to separate
  model error from run-to-run variance — a handy technique, not a required step.)

Numbers won't be perfect; "close enough" is a judgment call, not a formula.

### 8.5 Tier 5 — real-workload sensibility ("why was my run slow" acceptance)

Run a realistic multi-test scenario (TestModule-suite-like / CI-shaped) where we can
*reason* about the expected bottleneck ("the go build dominates"; "a filesync tail"; "a
serial setup tax"). Assert the analyzer surfaces it and that user-facing outputs are
sensible: top individual ops (incl. user execs), the work/owner breakdown, and
**user-defined class rules** (e.g. "all go builds" via `argv^=go build`). Capture a few
canonical scenarios + expected answers as living acceptance cases.

### 8.6 Tier 6 — CI-trace end-to-end (north-star acceptance; gated on Phase 0)

Once Cloud ingest works, download a real large CI trace, run the analyzer, verify a
coherent "why was this slow" answer: root coherence (one navigable tree),
cross-process nesting (CLI→engine→nested), makespan = real run time, sensible top
bottlenecks, performance at scale. Compare against a same-commit native profile if
obtainable, else human reasoning.

### 8.7 Robustness / negative tests

Fragmented/multi-root traces; dropped spans; incomplete (`Open`) spans; dropped
link/event/attr counts >0 (assert diagnostics fire, no silent corruption); very large
traces (scale/perf); pre-augmentation traces (assert `unknown` classification +
diagnostics, no crash).

### 8.8 What "good" looks like (judgment, not rigid gates)

Standing drift small on known workloads; cross-source top bottlenecks largely agree;
injected serial bottlenecks identified with no false positives on parallel injections;
predictions roughly line up with actuals (given a couple-run spread); the analyzer's
top answer matches human reasoning on the canonical scenarios; a coherent answer on a
real CI trace. These are **goals, not automated pass/fail** — a miss is a
**stop-and-look** signal. Firm thresholds (e.g. for CI gating) come later, once real
numbers show what's achievable.

---

## 9. Proposed sequencing

0. **Cloud trace spike (R-I) — ✅ DONE.** Live fetch of a real trace via
   `internal/cloud` confirmed attrs / links (+`link.purpose=cause`) / events
   (+`Container started`/`exited`) / single-root tree round-trip; only dropped-counts
   are unexposed (R1, minor). Cloud confirmed as the production source. **Gate cleared.**
1. **Analyzer op-set generalization** (§5): op-sets, `RunWhatIfsForOpSets`, report
   (incl. R-S background handling), `--root-mode`, CLI flags. Validates on existing
   native dumps.
2. **`LoadOTel` + `wcotel`** (§4): dedup, spans→Graph, `wcprof.wait.*` edges, R-O
   call_exec synthesis + retarget, exec child synthesis (R-D/R-Q), R-H diagnostics.
3. **Engine augmentation** (§3): exec span + R-C split, bounded `service_start` span,
   call_exec interval attrs, the wait-edge sites, kind/work/owner attrs, raised link
   limits.
4. **Cloud-API ingest** (per Phase 0) — same loader.
5. **Validation (§8):** run the full tier plan — standing drift gate, cross-source
   oracle, known-answer injection, prediction-vs-actual. Expected to surface issues;
   budget for it (woven through 1–4, not a trailing step).
6. *(Later, sibling)* cache diff (§6).

---

## 10. Product calls — all resolved (Erik)

1. **Exec span + propagation change** — *approved.* Emit the exec span but tag it
   `ui.internal` (`telemetry.Internal()`, R-V) so it does **not** render in TUI/Cloud
   yet stays in the exported trace for the analyzer; nested work renders under the cause
   in the TUI as today; the analyzer reads the exec as the structural parent; logs stay
   routed to the installing call (R-C). ✓
2. **Singleflight target** (R-O) — engineering detail; synthesize a bounded `call_exec`
   from cheap link data (no extra span). Confirmed (your choice). ✓
3. **User class-rule surface** — minimal `MetaMatch`. Confirmed. ✓
4. **Background/availability ops** (R-S / §1.3a) — daemon lifetime spans modeled as
   availability (not work, matching native); ranking intrinsic. Confirmed reasonable. ✓
5. **Tier-4 "close enough"** (§8.4) — de-engineered per your steer: a couple of runs +
   eyeball predicted-vs-actual; a mismatch is a *stop-and-look*, not a gate. ✓

---

## 11. Anti-inference + bounded-target self-audit

| Site | Guess? | Target bounded? | Resolution |
|---|---|---|---|
| sync parent→child | implicit-join | n/a (nesting) | = native baseline |
| singleflight join | none | **yes** — synthesized `call_exec`, children reparented (R-O/R-U) | wait edge + exec interval on the joiner link (R-T) |
| lazy wait | none | **yes** — resume span (R-P capture) | explicit wait edge at `evaluateOne` |
| service-start wait | none | **yes** — bounded `service_start` span (R-O) | explicit wait edge; lifetime span = availability, not a work op (R-S/§1.3a) |
| lock wait | none | n/a (fixed delay) | explicit `wcprof.lock_wait` event → `actWaitFixed` |
| nested-client | none | n/a (nesting) | exec span = propagation parent (R-C); logs via split log-target |
| op kind | none | — | explicit attr; missing ⇒ `unknown`+diag (R-L) |
| op work/owner | none | — | explicit attrs (R-E) |
| exec engine/user split | none | bounded by `started`/`exited` events (R-D) | synthesized child ops w/ typed fields (R-Q) |
| op identity/class | none | — | explicit metadata; grouping user-defined |

---

## 12. Changelog

**Chunks 2–4 IMPLEMENTED (consume side) — Codex-CONVERGED, committed (unpushed).** Chunk 2: `engine/telemetryattrs/wcprof.go` (attr/value constants), `engine/wcprof/wcotel/` (ProfSpan/Link/Event + dropped-counts, `Dedup`, `ReadOTLPDump`), `loadotel.go` skeleton (`LoadOTel`→Graph, fnv64a op IDs, kind/ident/Meta, wait edges from `wcprof.wait.*` only), report diagnostics + `--source`/`--root-mode`/`--show-availability` (validated on a real otlpdump capture). Chunk 3: `loadotel_synth.go` (`synthesizeExecPhases` containerStart/processRun split; `synthesizeCallExec` keyed on `ceKey{target,execStart,execEnd}` + epsilon containment + no call_exec nesting). Chunk 4: availability elision (`elideAvailability`, `Graph.Availability`/`RootMode`), `trace` root-mode anchoring in `replay.go` (no root chaining), `service_lifetime`-under-`service_start` emission invariant (§4.6) so readiness descendants reparent to the bounded `service_start`.

**Chunk 5 IMPLEMENTED (engine emit: dagql/call) — Codex-CONVERGED, committed (unpushed).** `engine/telemetryattrs/wcprof_emit.go` (`AddWaitEdge`/`AddSingleflightWaitEdge` — windowed `wcprof.wait.*` links, the OTel twin of native BeginWait/End); `core/telemetry.go` `AroundFunc` tags every call span `kind=call`/`work=engine`/`owner=user|engine` (owner from `req.Module`); `dagql/cache.go` emits the **singleflight** join edge (joiner→shared call span, carrying the bounded `[execStart,execEnd]` call_exec interval) and the **lazy** wait edge (waiter→resume span). **Review-driven refinements (vs §3.2 as written):** (1) the singleflight `[execStart,execEnd]` interval is captured **oc-level inside the execution goroutine** (real `fn` boundaries), not per-joiner unblock times — required so all joiners report an identical interval and `synthesizeCallExec`'s `ceKey` dedups to **one** call_exec per shared execution (per-joiner wakeup jitter would otherwise mint N call_execs). (2) the lazy **resume span** carries explicit `kind=lazy`/`work=engine`/`owner=engine` — it is the OTel twin of native's `OpKindLazy` and the bounded target lazy waiters point at (anti-inference; matches the §6 loader fixture). (3) `lazyResumeSpanCtx` is cleared per lazy-eval attempt so a retry after a failed attempt can never emit a wait edge to a stale eval's span. Validated: `go test -race ./dagql/` green; emit-side end-to-end deferred to the engine-dev integration pass at the close of the emit side.

**Chunk 6 IMPLEMENTED (engine emit: services) — Codex-CONVERGED, committed (unpushed).** `core/services.go`: a bounded `service_start` OTel span (`telemetry.Internal()`, `kind=service_start`/`work=engine`/`owner=engine`) threaded into `svcCtx` in the first-starter branch, so `svc.Start` creates the service exec span beneath it (§4.6 emission invariant); ends at readiness (`defer startSpan.End()` declared after `defer close(start.done)` → LIFO → span ends before `done` closes → waiter unblock ≥ span end, the bounded-target ordering); `startSpanCtx` stored on `startingService`, set once before publish under `ss.l` (race-free). Service wait edges at the two `BeginWait(WaitReasonService)` sites (Get + startWithKey), completed-path only; the two teardown `done`-waits get none (match native). `core/service.go`: the existing service exec span classified `kind=service_lifetime`/`work=availability` (the loader elides it, reparenting readiness descendants to `service_start`). Created unconditionally (the OTel source is independent of `wcprof.Enabled`; no-op tracer when telemetry off → invalid ctx → wait edges no-op). **Design choice (Codex-confirmed faithful):** kept the literal §3/§4 `kind=service_lifetime` classification (container-setup time folds into `service_start` self-time after elision — still ranks via service_start, the §4.6 601 net goal) rather than reclassifying the lifetime span as `exec`+availability for a separate reparented containerStart op. Verified: build + `go test ./engine/wcprof/...` green.

**Chunk 7 IMPLEMENTED (engine emit: cache-volume lock wait) — Codex-CONVERGED, committed (unpushed).** `engine/telemetryattrs/wcprof_emit.go`: `AddLockWaitEvent` — a resource wait with NO target span, emitted as a span event (`WcprofLockWaitEvent` + `LockKeyAttr` + window), the analyzer modeling it as blocked self-time (not a join). `core/container_exec.go` `lockMountedCaches`: emit it at the native `BeginWaitIdent(WaitReasonLock)` site, ident `"cachelock:"+lockKey` to match native (cross-source oracle parity). Consume side already present + tested (loadotel.go:125-136, loadotel_test.go:43,105). Verified: build + `go test ./engine/wcprof/...` green.

**Chunk 8 IMPLEMENTED (engine emit: executor exec span + propagation/log split, §3.3) — Codex-CONVERGED, committed (unpushed).** `engine/engineutil/executor.go` `Run`: one Internal exec span per exec (`telemetry.Internal()` — hidden in UI/Cloud, still exported) with `kind=exec` + work/owner/argv/exit + `DagDigestAttr` identity; `work,owner` = (user_process,user) normally, (engine,sdk) for `execMD.Internal`, **availability** for `execMD.ServiceDaemon`. The exec-span ctx threads into `c.run`, so the existing "Container started/exited/created" events fire on it (verified runContainer doesn't replace the span) — giving the loader the precise process window with no `WcprofProcessEndAttr` ("Container exited" already fires at the callWithIO return, before cleanup). Helpers `execSpanName` (conservative name — argv never in the name), `boundArgv` (count cap; values already exposed via dag.call), `execExitCode`. **Propagation/log split:** `execState.causeCtx` → `logTarget` (SpanStdio routing + error-origin, unchanged) + new `propagationParent` (the exec span); `setupOTel` uses `propagationParent` for `PropagationEnv`, `logTarget` for SpanStdio; `setupNestedClient` uses `propagationParent` — so nested/in-container spans nest under the exec span while logs/errors stay on the installing call. **Codex-driven fixes:** (1) BLOCKER — a service daemon's exec (now an exec span under the elided `service_lifetime`) would survive as a phantom `user_process` bottleneck; fixed with `ExecutionMetadata.ServiceDaemon` (set in `core/service.go`) → `work=availability` → elided, while its containerStart reparents to `service_start` (new test `TestServiceDaemonExecAvailability`). (2) `EndWithCause` runs on a COPY of the return error so the hidden exec span never becomes the error origin. (3) `synthesizeExecPhases` reclassifies the exec op's residual self-time ([Container exited, exec end] = engine cleanup) to `engine` (unless availability), so cleanup isn't blamed on `user_process` (`TestSynthesizeExecPhases` extended). Verified: build (engine/core/cmd) + `go test ./engine/wcprof/...` green; end-to-end R-C behavior validated next via the engine-dev integration pass.

**Chunk 9 IMPLEMENTED (raise span link/event caps) — Codex-CONVERGED, committed (unpushed).** `engine/server/session.go`: the per-client tracer provider raises `LinkCountLimit`/`EventCountLimit` to 8192 (from `NewSpanLimits`, preserving the other limits + their env overrides, via `WithRawSpanLimits`). wcprof records causal edges as span links (waits/joins) + events (lock waits), and OTel's default of 128 drops the oldest on overflow — silently losing edges. Bounded (not unlimited); the loader's dropped-count diagnostic is the safety net for anything beyond. Codex confirmed this is the sole engine tracer provider for emitted spans, applied provider-wide (the SDK enforces limits at span creation/recording). Verified: build green. **The emit side (chunks 5–9) is now complete.**

**Chunk 1 IMPLEMENTED (analyzer op-set generalization, §5) — Codex-CONVERGED, committed (unpushed).** `engine/wcprof/wcanalyze/opset.go` (OpSet + Default/Subtree/Predicate/Singleton producers, ClassRule/MetaMatch), per-op `opFactor` in `replay.go` (replaced per-class `factorOf`; removed dead `classOf`/`classKeys`) + `RunWhatIfsForOpSets`/`OpSetWhatIf` + shared `runWhatIfSims`; `Op.Meta` added (graph.go); report gains individual-op what-ifs + user-defined classes + self-time-by-work-type; `cmd/wcprof-analyze` `--class-rule`/`--top-ops`; new `opset_test.go`. Baseline drift preserved at +0.0%. **Conscious divergences from this doc (Codex-judged acceptable, all MINOR):** (1) `glob` dropped from MetaMatch (§5.1) — path-glob mishandles '/'; eq/prefix/contains kept. (2) `--root-mode` (§5.4) deferred to the OTel-loader chunk — only meaningful for multi-root OTel/CI traces, untestable on native CLI dumps (moved, not dropped). (3) report breakdown is work-type-only; `owner` breakdown (§5.3) lands with the OTel `owner` metadata.



**Rev 8 (Phase-0 Cloud spike — R1 cleared):** live fetch of a real Cloud trace
(`internal/cloud` client path) confirmed the trace API returns a coherent single-root
tree, span links + attrs (incl. `link.purpose=cause`), span events (incl. `Container
started`/`exited`/`created`), and custom `dagger.io/*` attrs (incl. `dag.digest`/
`dag.inputs`/`ui.internal`). R1 → VERIFIED (§7); Phase-0 → done (§9). Two bonus
findings: the exec lifecycle events R-D needs **already exist** (§3.3), and
`ui.internal` round-trips (R-V validated). Residual minor gap: dropped-counts not
exposed by the Cloud GraphQL fragment.

**Rev 7 (Erik round-5 confirmations):** **Exec span approved** — tagged
`telemetry.Internal()` (R-V) so it's hidden in TUI/Cloud but still exported for the
analyzer (verified: `ui.internal` is a render hint, not an export filter; nested work
still renders under the cause, analyzer reads the exec as parent) (§3.3, §10#1).
**Tier-4 validation de-engineered** per Erik (was over-built): dropped the formal
noise-floor/CV/K=5–10/threshold machinery for "a couple of runs + eyeball
predicted-vs-actual; a mismatch is a *stop-and-look*, not a pass/fail gate" (§8.4);
§8.2 cross-source thresholds softened to sanity guidelines (mapping table + exclusions
kept — needed for any comparison); §8.8 → goals, not gates. Availability framing
(§1.3a/§4.6) and product calls #2–#4 confirmed.

**Rev 6 (Codex round 5):** **Availability elision policy (§4.6)** — defines what
happens to a daemon lifetime span's descendants: readiness children (container-start/
healthcheck) reparent to `service_start`; the daemon's own `processRun` is classed
*availability* (not `user_process`) so it's never a phantom bottleneck; partial overlaps
get a diagnostic (#1). **Validation hardened:** §8.1 adds the availability hard-case
fixture; §8.2 makes the cross-source oracle executable (checked-in class-mapping table,
exclusions, floors, concrete pass thresholds) + adds a Cloud↔CLI-stream parity gate;
§8.3 adds the background-daemon discrimination (#2,#3,#4). Ingest correction confirmed
sound by review.

**Rev 5 (Erik round-4 feedback):** **Ingest corrected** — dropped the bogus
`clientdb`-SQLite "fallback" (engine-internal per-client storage, not
client-accessible); sources are now the Dagger Cloud trace API (primary) + the
CLI-received OTel stream (intermediate fallback; `otlpdump` taps it for dev), and if
Cloud lacks fidelity we *fix Cloud* (§1.4#5, §4.1, §7-R1, §9). **Availability ≠ work
(§1.3a)** — daemon `service_lifetime` spans are modeled as availability (not imported
as work ops, matching native), so non-bottleneck ranking is *intrinsic* per Erik's
point — replacing the earlier "exclude from ranking" band-aid framing (§3.2c, §4.2,
§5.3). **Validation & testing is now a first-class section (§8)** — standing drift
gate, unit/golden, cross-source oracle, known-answer injection, prediction-vs-actual
with a "close enough" methodology, real-workload sensibility, CI-trace end-to-end,
robustness, acceptance bar.

**Rev 4 (Codex round 3):** R-U synthesized `call_exec` now **reparents** the
resolver's in-window children (true structural wrapper) — else its self-time absorbs
their work and acts as a fixed floor masking counterfactuals (#1). R-T the `call_exec`
interval is carried on the **joiner's live wait link** (sourced from `oc`), not the
first-caller span, which can end early under `context.WithoutCancel` (#3); `call_exec`
synthesized only when a joiner exists. R-S `service_lifetime`/background spans are
**detached from the runtime graph** (not just excluded from ranking) — as structural
children they corrupt self-time (`graph.go:339`) and joins (`replay.go:353`) (#2).
R-H diagnostics now also fire on `DroppedAttrs` (#4).

**Rev 3 (Codex round 2):** R-O **bounded targets** — singleflight now targets a
loader-synthesized `call_exec` from cheap interval attrs (not the late-ending
first-caller span, #2); service waits target a bounded `service_start` span (not the
lifetime span, #1); added the bounded-target invariant §1.3 (replay.go:165). R-C
**split** `causeCtx` into propagation-parent (exec span) vs log-target (preserve
SpanStdio routing, #4). R-D/#3 exec `processRun` ends at an explicit process-end
marker, excluding executor cleanup. R-Q/#6 synthetic child ops set **typed** fields
(`WorkType`/`Ident`/`Outcome`) and `processRun` inherits argv/owner so user rules
match it. R-S/#5 `service_lifetime` (and background ops) excluded from default
ranking. R-H/#7 `ProfSpan` carries dropped counts; loader diagnostics use them.
R-helper/#8 the wait helper moves to a low-level pkg importable by `dagql`+`core`.
R-P/#9 capture `resumeSpan.SpanContext()` directly for the lazy target.

**Rev 2 (Codex round 1):** R-A runtime edges all explicit `wcprof.wait.*` at the wait
sites (no provenance-link inversion, #1). R-C exec span as propagation parent (#4).
R-D exec split via child ops (#5). R-E split work/owner (#6). R-B singleflight = first
-caller span [superseded by R-O] (#2). R-F `--root-mode=trace` (#3). R-J multi-trace
op IDs (#10). R-G loader dedup (#7). R-H raise 128 caps (#8). R-I Cloud spike Phase 0
(#9). lock = fixed window by key (#11). R-K argv out of span name (#12). R-L missing
kind ⇒ unknown+diag (#13).
