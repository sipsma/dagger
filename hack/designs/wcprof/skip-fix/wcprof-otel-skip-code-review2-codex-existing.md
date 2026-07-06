# wcprof OTel skip fix convergence review 2 - Codex

Reviewed amended commit `4921d53662` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`
against base `4585bf413d`, with special focus on the native un-gate and the new
lazy/invalid-target tests.

Targeted tests run:

```sh
go test ./core ./dagql -run 'ProfileSkip|Skip|Singleflight'
go test -race ./dagql -run 'ProfileSkip'
```

Result: pass.

## Verdict

Sign off. The round-1 blocker is resolved by Erik's chosen reframing: native now
keeps the reflection/introspection class at full fidelity, while the OTel source
skips only its high-volume added profiling spans. The directly-called reflection
accessor case now has a coherent shape: native records full detail; OTel may keep
only the surviving normal `dag.call` coarse op; the cross-source oracle must compare
non-reflection classes.

I found no merge-blocking correctness issue.

## Verification

### Native un-gate is complete

The native gates from round 1 are gone from the code paths I audited:

- Outer native `OpKindCall` is gated only by `wcprof.Enabled`, nil `req`, and nil
  `ResultCall`, not by `ProfileSkip`: [dagql/cache.go:3606](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3606).
- Native shared `call_exec` is unconditional when wcprof is enabled:
  [dagql/cache.go:3768](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3768).
- Native waits are unconditional when wcprof is enabled:
  [dagql/cache.go:3996](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3996).
- Native `dagql.publishResult` is still keyed only on `oc.profOpID != 0`, which
  follows the now-ungated native execution op:
  [dagql/cache.go:4067](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:4067).
- Native lazy op and waits are ungated:
  [dagql/cache.go:3041](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3041),
  [dagql/cache.go:2988](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:2988), and
  [dagql/cache.go:3141](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3141).

The remaining `ProfileSkip` reads in `cache.go` feed only OTel decisions.

### OTel skip remains dangle-proof

The OTel gates still use the target-side decision:

- `ProfileSkip` is stamped before `IsSkipped` returns:
  [core/telemetry.go:41](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/telemetry.go:41).
- OTel `call_exec` is skipped for a skipped recipe:
  [dagql/cache.go:3784](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3784).
- `oc.profSkip` snapshots that target decision before waiters can attach:
  [dagql/cache.go:3806](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3806).
- OTel singleflight/call_exec waits are suppressed only when the target was
  skipped; otherwise invalid targets still emit and fail the gate:
  [dagql/cache.go:4016](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:4016).
- OTel `publishResult` follows the presence of the OTel `call_exec` span context:
  [dagql/cache.go:4080](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:4080).
- OTel lazy span and lazy waits gate on the producer's stored frame flag, not the
  forcer's recipe:
  [dagql/cache.go:3040](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3040),
  [dagql/cache.go:3061](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3061),
  [dagql/cache.go:3008](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3008), and
  [dagql/cache.go:3145](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3145).

That preserves the no-inference property: the loader/replay remain unchanged, and
the emit side produces a smaller but self-consistent OTel graph.

### New tests

The added tests cover the right behavioral surfaces:

- `TestProfileSkipGatesLazyEmit` proves skipped lazy producers emit no OTel lazy
  span or lazy waits, and kept producers do:
  [dagql/cache_profileskip_emit_test.go:192](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache_profileskip_emit_test.go:192).
- `TestProfileSkipLazyCrossRecipeForcerStaysClean` is the important N3 case:
  skipped producer, traced non-skipped forcer, zero OTel lazy spans/waits, clean
  structural gate:
  [dagql/cache_profileskip_emit_test.go:252](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache_profileskip_emit_test.go:252).
- `TestProfileSkipDoesNotBlindInvalidTargetDetector` exercises a non-skipped
  producer with an untraced lazy leader, proving a traced joiner still emits a
  targetless wait and the structural gate fails loud:
  [dagql/cache_profileskip_emit_test.go:343](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache_profileskip_emit_test.go:343).

One non-blocking precision nit: that invalid-target test is a lazy invalid-target
test, but its comment says "call_exec span" and "`oc.profSkip`"
([dagql/cache_profileskip_emit_test.go:343](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache_profileskip_emit_test.go:343)).
The tested behavior is still valuable and matches the same fail-loud invariant,
but the comment should say lazy span / producer flag, or a separate singleflight
invalid-target test should be added later.

### Comments/docs

The important comments now state the asymmetry correctly:

- `ProfileSkip` is OTel-only:
  [core/telemetry.go:426](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/telemetry.go:426).
- Directly-called reflection accessors retaining a coarse OTel `call` op is named
  explicitly as non-source-parity:
  [core/telemetry.go:448](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/telemetry.go:448).

Minor stale wording remains at [dagql/cache.go:1800](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:1800):
it says every singleflight wait gates so a wait is emitted iff the target's
`call_exec/native op` was. The code now gates only the OTel wait; native always
emits full detail. This is a wording nit, not a behavior issue, because the
nearby wait code is correct at [dagql/cache.go:3996](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3996)
and [dagql/cache.go:4016](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:4016).

## Remaining blockers

None. The amended commit is sound to land from this review's perspective.
