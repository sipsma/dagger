# A6 (#14241) test code: simplifications and maintainability concerns

Scope: the fixture (dagql/cache_fixture_barrier.go, dagql/cache_fixture_control.go,
dagql/cache_part_fixture.go, core/schema/remote_cache_fixture_control.go,
engine/server/remote_cache_fixture_controller.go, engine/fixturetransport),
the integration harness (core/integration/remote_cache_harness_test.go) and
the suite (core/integration/remote_cache_*_test.go, 18 files, about 5,100
lines; 1,233 lines of unit tests for the fixture itself). Line numbers are
at 40a0eee986. No code was changed. Ranked by expected payoff per unit of
change; 1–4 are the ones worth doing before this PR merges if any are.

## 1. The barrier protocol goes through the filesystem, one container exec per step

Every arm, wait and release writes a JSON record into the fixture volume by
running a shell `printf` in an outer alpine container
(`fixtureEngine.control` → `writeFile` → `volumeExec`,
remote_cache_harness_test.go:331-343, :290-307), then calls the hidden field
with the record's name, and the engine reads the record back under os.Root
(core/schema/remote_cache_fixture_control.go:130-137, :226-240). A wait
re-writes a `<key>-wait.json` token record each time; there are 19 such
sites across six files (encoded_restart 4, offers 4, sharing_finish 6,
pipeline 2, sharing_donor 2, controls 1). The design note at
remote_cache_fixture_control.go:21-24 says the controls "add no GraphQL
field or argument"; the file protocol is the price of that. A single
opaque `request: String` (JSON) argument on the already-hidden
`_remoteCacheFixture` field would carry every control request
(barrierArm, barrierWait, barrierRelease, releaseHold, observe, transport,
armRenewalReply, offer) with the same strict decoding, and remove the
volume exec from the hot path; bundles stay as files, since they are data.
Fewer moving parts, faster tests, and the request types stop being
"a file named by path".

## 2. Three copies of `await` and no barrier helpers on the harness

`await` is defined three times with the same body
(remote_cache_encoded_restart_test.go:62, remote_cache_offers_test.go:119,
remote_cache_sharing_finish_test.go:132-140) and inlined elsewhere
(http_restore :63, :266, :345; git_trees :166, :231). The arm reply
(`FixtureBarrierArmed`, cache_fixture_barrier.go:141-144) is exactly the
token that wait and release take (`fixtureBarrierToken`,
remote_cache_fixture_control.go:119-122), so the harness can own three
methods: `armBarrier(req) token`, `awaitBarrier(token) reached` (with the
one timeout), `releaseBarrier(token)`. That deletes the three `await`s,
the per-test key-naming (`"hold-%d"`, `"chain-"+tc.name`), and the
`-wait.json` convention in one move, and pairs naturally with item 1.

## 3. `importAndHoldR` walks every arrival because a barrier cannot name the row it wants

remote_cache_sharing_finish_test.go:96-129: the import starts a cascade of
sharing passes, R's row ID is unknown until the import returns, so the
test arms a barrier, waits, checks the row, arms the next, releases the
last, up to 64 times. Two ways to remove the loop: a selector by the
result's type or field (`FixtureBarrierSelector`,
cache_fixture_barrier.go:110-115, has ResultID, Address, TaskGeneration,
PassID; adding `Type`/`Field` would let the test say "the first Container
row"), or a "hold every occurrence until released" mode so one armed
barrier parks each arrival and the test releases the ones it does not
want. Either is a small change in `fixtureReachUntil`
(cache_fixture_barrier.go:347-399) and the selector's `matches` (:315-331).

## 4. Two observation streams with untyped kinds

Part events (`recordPartFixture*`, dagql/cache_part_fixture.go:91-138)
carry a free string `kind` ("owner-sync", "share-skipped", "lazy-enter",
"selected-delegation", "installed-delegation", "lazy-ref-released",
"lazy-ref-release-error", …) set at call sites across the cache; the
reached-point journal (`observeFixtureReach`, :171-184) carries typed
`FixtureBarrierPoint`s. The two share one sequence counter and one cap
(:43-47) but have separate locks (`mu`, `reachedMu`), separate report
lists (`Parts`, `Reached`) and separate hand-written filters in the tests
(`partEventsOf`, `partKindsOf`, `reachedOf`,
remote_cache_harness_test.go:407-437; `reachedAt`,
remote_cache_fixture_controls_test.go:28-36). The kinds are what the
sharing flake (TestSharedHostDirectoryLifetime) turned on: a test asserting
against a string it cannot see the producer of. Smallest fix: make the
kinds a closed constant set next to `FixtureBarrierPoint` and have one
`observe(event)` entry point; larger fix: one stream, one lock, one list.

## 5. The point list has no compile-time link to its reach sites

23 points are declared (cache_fixture_barrier.go:24-50) and registered in
`fixtureBarrierPoints` (:74-82) by hand; reach sites name them by literal
or by assignment (`lazyEvent.Point = FixtureOriginalSealed`), so nothing
checks that every declared point is reached somewhere or that every
reached point is declared. The suite arms 16 distinct points; whether the
other seven are reached at all needs a grep of two packages. A unit test
that reaches each point through the real code path is expensive; a cheaper
guard is to derive `fixtureBarrierPoints` from a `switch` with no default
(so an added constant without a case fails to compile) and to have the
controls test assert the journal's point set for one full scenario.

## 6. Fault actions duplicate their points

`FixtureBarrierAction` (:55-64) has five error actions, each legal at
exactly one point (`fixtureBarrierFaults`, :85-91), and `validateFixtureBarrier`
(:172-190) rejects the wrong pairs. The point already determines the
fault, so a `Fault bool` (or `Action: "fault"`) on the request would
delete the action enum, the pair map, the pair validation and the
"legal only at" branch of the controls test (:90-92) without losing any
behaviour; `fixtureFaultError` (:94-106) keys on the point instead.

## 7. Two copies of the bounded shutdown

`fixtureEngine.shutdown` (remote_cache_harness_test.go:182-217) and
`stopNestedEngine` (:224-262) are the same three-step algorithm, the
second written over pointer-to-pointer arguments "for the older tests that
keep their own engine records". Migrating those tests
(remote_cache_transfer_test.go's `runTransferSchemaRecovery`, :122, and
its callers) onto `fixtureEngine` deletes the second copy and the
`**dagger.Client` signature.

## 8. `nestedEngineEarlyExit` starts a second engine on the same state to read the first one's output

remote_cache_harness_test.go:96-121: on a failed start it re-runs the
engine container as a plain exec on the same state volume with
`timeout 30`, from inside `t.Fatalf`'s argument (:83). It can take 90 s,
it writes to the state volume the failed engine used, and it exists only
because a service's output is not in its start error. Now that the
opt-in goroutine dump and the debug endpoint exist (engine_test.go's
`withNestedEngineDebugEndpoint`), the cheaper diagnostic is the service's
own log through the outer client; if that is not reachable, at least run
the probe on a copy of the state, or drop it.

## 9. The wire types are declared twice

The integration package re-declares the schema's reply shapes as anonymous
structs: `fixtureExportSelectedResult` in
remote_cache_fixture_controls_test.go:39-50 mirrors
core/schema/remote_cache_fixture_control.go:84-98 field by field;
`transferFixtureMapping`/`transferFixtureReport` in
remote_cache_transfer_test.go:31-45 mirror the schema's report; the
`Bodies` element is an anonymous struct. The request side already uses
`dagql.FixtureBarrierRequest` directly. Moving the reply types into core
(where `RemoteCacheFixtureStorage`, `RemoteCacheFixtureRenewals` already
live, core/remote_cache_fixture.go:110-136) and importing them on both
sides removes the drift risk: a renamed JSON tag on one side is a silent
zero on the other.

## 10. File movement by shell string

`copyFixtureTo` (:390-397) builds a shell script by concatenating bundle
names; `readBundle` (:359-367) and `writeFile` (:340-343) run `cat` and
`printf` through env vars to avoid quoting. Safe as written, but each is
a container exec, and the harness could instead give the engine one
"copy these bundles from that volume" control, or mount both volumes once
per scenario. Low priority unless item 1 is done, after which these are
the last shell paths.

## 11. Every scenario struct repeats the two-engine setup

`pipelineScenario` (:41-71), `sharingFinishScenario` (:20-45),
`offersScenario`, `encodedRestartScenario`, `gitTreesScenario` each hold
`a, b *fixtureEngine`, start them with `name+"-a"` / `name+"-b"`, and
seed host files in a loop over `[]*fixtureEngine{s.a, s.b}`. A
`twoEngines(ctx, t, name)` helper returning the pair (and a `both(func)`
for the seeding loop) removes ~10 lines per scenario and makes the
"gated=false" control case (`TestFixtureControls/AbsentGate`) the only
place that calls `newFixtureEngine` with `false`.

## 12. Timeouts are scattered

`joinBounded` 3 min (:158-167), `await` 2 min (sharing_finish :134),
`closeClientBounded` and `fixtureEngineStopTimeout` 2 min (:141-176),
`nestedEngineEarlyExit` 90 s and its inner `timeout 30` (:97, :107),
`importAndHoldR`'s 64 iterations (:118). Each has a reason in a comment,
but a reader tuning one cannot see the others. One block of named
constants at the top of the harness, with the load-average note from
:169-175 once, would do.

## 13. Process-wide singletons in the fixture

`fixturetransport.current` (transport.go:104, `Enable` once per process),
`core.EnableRemoteCacheFixtureGit(root)` and
`schema.InstallRemoteCacheFixtureGitTransport()` (called from
engine/server/remote_cache_fixture_controller.go:94-105) are globals keyed
on the process, so two enabled engines cannot share a process and a unit
test that enables the transport leaks it to later tests unless it resets
`current`. Acceptable for a gated test engine; worth a comment on
`Enable` saying so, and a `Disable` for tests.

## Not concerns

The seams are right: the controls reach the engine through one optional
interface on Query.Server (core/remote_cache_fixture.go:39-60), the
barrier hook is one atomic load off-gate (cache_fixture_barrier.go:333-339),
faults only fail what production can fail (:52-54), and every pause has a
cancellation and an owner-ended exit (:385-398). The observation caps
fail loudly rather than dropping (cache_part_fixture.go:50-63). Those
should stay whatever else changes.
