# Packaging the remote cache engine work as stacked pull requests

Stack integrator, 19 September 2026. Private evidence branch
`stack-integrator-evidence`; nothing here goes into a pull request.

## Step 1: rebase Erik's stack onto `main`

Inputs, fetched from `upstream` (`dagger/dagger`):

| PR | branch | head before | commits above the PR below |
|---|---|---|---|
| #13962 | `sipsma/remote-cache-track5-reader-cancel` | `a5c4b2570a` | 16 above `main`'s merge base `0d031c08ef` |
| #13969 | `sipsma/remote-cache-track6-session-resources` | `73234a8d12` | 52 |
| #14043 | `sipsma/remote-cache-track7-per-part-evaluation` | `1d85bd34aa` | 43 |
| #14049 | `sipsma/remote-cache-track8-terminology` | `18f0d54c86` | 7 |
| #14050 | `sipsma/remote-cache-container-part-persistence` | `9375bbb985` | 12 |
| #14051 | `sipsma/remote-cache-snapshot-chains` | `62d62bd0c3` | 16 |
| #14093 | `sipsma/remote-cache-deferred-filesystem-restoration` | `17f7dd89f4` | 9 |

`upstream/main` at `06660c3cde`, 439 commits above the stack's base
`0d031c08ef`. Each branch above #13962 is a strict descendant of the one
below it. GitHub reports #14049 as CONFLICTING against its base before
any rebase; the rest MERGEABLE.

Rebase log follows, bottom first.

### #13962 `sipsma/remote-cache-track5-reader-cancel`

Candidate head `c6653c6d04` (working branch `pkg/track5`), 16 commits on
`upstream/main` `06660c3cde`. Map, original to candidate, in order:
`4e993be3fb`, `e6b45caeac`, `86e0babb50`, `ed0f6cf2f5`, `22605cb304`,
`61c5790ca2`, `cb739d2b9c`, `9180e61009`, `2f59ec6361`, `1042f8419b`,
`ebad6b1c15`, `241af6a1a2`, `410d7cb525`, `13e8677318`, `a5c4b2570a` are
each one candidate commit with the same message; `1ca59a2b59` (chore:
regenerate tla-check module bindings) is `c6653c6d04`, a fresh
regeneration on the rebased tree (`dagger generate -y go-sdk:generate`
on `remote-cache-engine`, source-map lines only), placed at the tip
rather than in its original position because its content depends on
every other commit's lines.

Conflicts and resolutions:

1. `e6b45caeac`, `.dagger/modules/tla-check/main.go` and
   `dagql/tla/README.md`: main added the client-lifecycle model, its
   expected-outcome map and the orphaned-lease mutation; the commit adds
   three decode configurations and a finding sentence. Kept main's text
   and inserted the commit's additions in place (both additive).
2. `cb739d2b9c`, README: the same paragraph, this time closing the
   finding. Kept main's mutation sentence and replaced the finding
   sentence with the commit's closed-finding wording.
3. `1ca59a2b59`, `dagger.gen.go`: main had regenerated the same file
   (its client-lifecycle API). Took main's file at that point and
   regenerated at the tip, above.
4. `241af6a1a2`, `dagger.toml`: main migrated the file to the
   `sdks.go.scopes` format and registers tla-check as a top-level
   module; the commit moves tla-check into the dev environment (old
   format). Resolved by moving main's `[modules.tla-check]` entry to
   `[env.dev.modules.tla-check]` in main's format. Note: main's
   `.github/workflows/checks.yml` filter matrix has no tla-check entry,
   so its `+check` functions do not run in CI on main either way; the
   commit's intent (dev only) is carried unchanged.

Mishap, corrected: the first resolution of 4 failed partway and the
rebase continued with conflict markers in `dagger.toml` for the last four
commits. Rewound to before that commit, redid it, re-applied the three
above it; no candidate commit carries markers (checked per commit).

Tests on `c6653c6d04`, clean tree, `go test -v -count=1 -timeout 60s
./dagql/ ./dagql/idtui/`, log /tmp/pkg-track5-c6653.log, exit 0: ok dagql
1.995 s, ok dagql/idtui 0.811 s; 547 top-level PASS, 0 FAIL, 2 SKIP (the
two live-cloud tests that need credentials, which skip on main too). The
branch's own tests all PASS: TestCacheCanonicalEquivalentSwapRacesSessionRelease
0.81 s, TestReportHeartbeatStopWaitsForInFlightWrite 0.10 s,
TestReportHeartbeatLine, TestReportHeartbeatLineNoChecks,
TestReportHandleFormFailsFast, TestReportRenderOptsRerunSuggestion and
the TestLive* golden tests. `go build ./...` ok. `e2e/helm` (the branch changes
`k3s.go`) is its own Go module whose tests connect to the host's own
`dagger`; on this host they fail identically on pristine main ("module
requires dagger v0.21.9, but you have v0.21.7", three tests), so they are
not evidence here and are left to CI.

#### #13962 follow-up

`1867836d03` on top of `c6653c6d04`: "dagql/tla: declare DelegatedReleaseOnly
in the decode_cancel configurations". main's model declares that constant
(its orphaned-lease mutation) and every configuration on main sets it;
the two configurations #13962 adds did not, so TLC would refuse them.
Both set it to FALSE. Placed in #13962 because the files are #13962's
(the Packaging reviewer's placement point). Approved and pushed
(`c6653c6d04` to `1867836d03`). TLC execution stays unverified here.

### #13969 `sipsma/remote-cache-track6-session-resources`

Candidate head `9285c661d4` (working branch `pkg/track6`), 52 commits on
#13962's `1867836d03`. Map: all 52 originals have a candidate by message;
`4320ca55a2` and `a84c1aefc5` (two "chore: regenerate tla-check module
bindings") map to one regeneration on the final line, `27703c4d5c`, a
fresh `dagger generate -y go-sdk:generate` on `remote-cache-engine` (it
also carries the Quick and Some runners' bindings); one candidate has no original:
`9285c661d4` "dagql/tla: declare DelegatedReleaseOnly in the
session-resource configurations" (the seven configurations this PR adds:
attach_release_reader and the six resources ones, each
`DelegatedReleaseOnly = FALSE`; `resources.cfg` also lists
`SharedLeaseReleasedWhenRetired`, since it replaced `core.cfg`, which
carried that invariant on main). Audit on the tip: all 35
`CacheLifecycle_*.cfg` declare the constant; every `expectedOutcome`
name has a file and every file is mapped. The generator also touched
`dagger.lock` (one new `alpine:latest` pin from its own run); discarded,
not part of the PR.

Conflicts and resolutions, in order:

1. `980d6aa39b`, CacheLifecycle.tla, the `sessionRelease` record in Init
   and Restart: main added `releaseReturned`, `waitRequested`,
   `waitReturned`; the commit adds `handles`. Union (TypeOK checks the
   fields individually).
2. `ddf65cf4a6`, tla-check main.go (four-JVM semaphore): main rewrote
   `runOne` (spec name and prefix arguments, a failure struct) and added
   `ClientLifecycle` with its own unbounded loop. Kept main's `runOne`
   call with the semaphore acquire around it, and gave `ClientLifecycle`'s
   `run` closure the same four-wide bound, since it fans out TLC JVMs the
   same way.
3. `8371bc5cba`, `2931e0bb4b`, `8851427778`, `d0e2a3cb73`, `94caf11d00`,
   `5f4b170a0a`, `4b77af35b4`, `73234a8d12`, README: the same finding
   paragraph each time. Resolved by one scripted rule: main's
   orphaned-lease mutation sentence kept at the head of the paragraph,
   the commit's finding text taken verbatim after it.
4. `39ec3f8068` (-Xmx8g): added to main's `runOne` command; the `One`
   command took it through the auto-merge.
5. `ad4935b899` deletes `CacheLifecycle_core.cfg` as duplicate coverage
   of `resources.cfg`; main had added `DelegatedReleaseOnly` and
   `SharedLeaseReleasedWhenRetired` to core.cfg. Deletion accepted; the
   invariant moved to resources.cfg in the fixup above.
6. `1dd7fed592`, `flush_roundtrip.cfg` and `persist.cfg`: main inserted
   the constant line above INVARIANTS; the commit extends INVARIANTS.
   Both kept.
7. `b6b3950278` and `83f4a74365`, CacheLifecycle.tla: main inserted the
   `SharedLeaseReleasedWhenRetired` definition between the
   `NoSpuriousErrors` comment and invariant; each commit rewrites that
   comment. main's definition kept, the commit's comment placed directly
   above the invariant.
8. `21cfa9e02f`, `7d18b64bc6`, `60f3b80f0d`, `df563e9150`, `92a73f34e6`,
   `4b77af35b4`, CacheLifecycle.tla, `FnComplete` and `FnWindDown`: main
   added its lease bookkeeping (`LET keepLease`, `![o].sharedLease`
   updates on every branch, `sharedLease |-> TRUE` in the record); the
   commits restructure the same actions (per-branch UNCHANGED, the
   acquiring reuse branch, `acq`, `acqPending`, `acqAdmitted`,
   `loadRefused`, `fnErrRefusal`, the `deniedEdges` variable). Resolved
   each time as the commit's structure with main's lease updates threaded
   into every branch, `deniedEdges` kept in the UNCHANGED lists, and the
   record literals as unions.
9. `4de76a5033` (checks out of CI, Quick and Some runners): main.go, the
   two generated files, dagger.toml. main.go: the commit's `runConfigs`
   runner kept, calling main's `base(m.Source)` and `reportFailures`;
   `ClientLifecycle` untouched. dagger.toml: the dev-environment entry
   already existed from #13962's resolution; only its comment changes to
   the commit's fuller one. Generated files: main's taken, regenerated at
   the tip.
10. `fd60355b08`, dagql/cache.go `initCompletedResult`: main wrapped the
    attachment in `if !resWasCacheBacked` (its returned-result reuse fix);
    the commit adds the barrier-error classification inside. Main's guard
    around the commit's body. One slip corrected before staging: a
    dropped closing brace, caught by the build.
11. Generated files at `4320ca55a2`, `a84c1aefc5`, `92a73f34e6`,
    `4b77af35b4`: main's taken each time; one regeneration at the tip.

Tests on `9285c661d4`, clean tree, `go test -v -count=1 -timeout 60s
./dagql/ ./core/integration/`, log /tmp/pkg-track6-9285.log: ok dagql
2.228 s, 337 top-level PASS plus one inherited nested SKIP; all 18 added
top-level dagql tests have PASS lines. core/integration FAIL at the
bound, which is not evidence and leaves that package unverified for this
PR: that package is the engine suite (its tests connect to an
engine) and the failures (`TestSchemaTools` subtests) are engine tests
the branch does not touch. The branch's one change there is an
error-message update in `TestModuleRuntimeBehavior`, an engine test,
left to CI per the step's rule against running the engine suites now.
`go build ./...` and vet ok; the tla-check module builds and vets.


#### #13969 follow-up and approval

`bd79ad1b35` on top of `9285c661d4`, TLA only, from the Packaging
reviewer's round: `orphaned_lease.cfg` and `release_wait.cfg` (inherited
from main) did not assign this branch's `Handles` and `ModelLateDeps`;
both now set `Handles = {}` and `ModelLateDeps = FALSE` (orphaned_lease
keeps its `DelegatedReleaseOnly = TRUE` mutation). Two UNCHANGED tuples in
CacheLifecycle.tla still named `deniedEdges`, a variable an earlier commit
of the branch removed, left by the FnComplete/FnWindDown resolution;
removed, no variable added. The reviewer independently checked the whole
CONSTANTS block against all 35 configurations. Approved and pushed
(`73234a8d12` to `bd79ad1b35`).

Audit tool, corrected: my first audit script ended a declaration block
at its first blank line, so it saw 6 constants where the spec declares 22
grouped by blank lines; the reviewer caught it. The script now reads
whole blocks (blank and comment lines inside included) and checks both
directions: every declared constant assigned in every configuration,
every assignment declared, every UNCHANGED and `vars` name declared, the
`vars` tuple complete. It is run on every PR tip before the tip is sent.
Rerun on `bd79ad1b35`: 0 findings over 22 constants, 10 variables, 35
configurations.

### Hash maps, original to candidate

Every original commit of each PR paired with its candidate, in order.
"(none)" marks an original with no candidate; "(new)" a candidate with
no original.

#13962 (`upstream/main..a5c4b2570a` to `upstream/main..1867836d03`):

```
a3a8829369 -> a3a8829369  dagql: model reader cancellation in the persisted-decode singleflight
f53f017c30 -> f53f017c30  dagql: add the decode_cancel configurations and accepted finding
50ef32e0b1 -> 50ef32e0b1  dagql: model the post-install decode failure and scope the barrier cancel arm
dc3d0ecdfc -> dc3d0ecdfc  dagql: split the decode install from the finish and model channels as generations
e0de990aa3 -> e0de990aa3  dagql: tighten two decode comments
a652713ca6 -> a652713ca6  dagql: retry persisted-decode joiners on leader cancellation, track pending lease sync
5dc904eff6 -> 5dc904eff6  dagql: model the decode cancellation retry and pending lease sync, close the finding
715681dbe8 -> 715681dbe8  dagql: decide decode leadership on state read under the mutex
2ec7c3a9bb -> 2ec7c3a9bb  dagql: update the decode_cancel_liveness header for the retry
998630bd54 -> 998630bd54  dagql: sharpen the decode_cancel_liveness post-install wording
ce04303215 -> ce04303215  fix: isolate mutable state for concurrent K3S fixtures
bf1ca7c143 -> bf1ca7c143  build: keep model checks in the dev environment
3cf71b8367 -> 3cf71b8367  test: disable report heartbeats in telemetry goldens
d73da1533d -> d73da1533d  fix: avoid recursive cache locks in debug snapshots
328429c0c1 -> 328429c0c1  test: signal queued writer while holding its lock
c6653c6d04 -> c6653c6d04  chore: regenerate tla-check module bindings
1867836d03 -> 1867836d03  dagql/tla: declare DelegatedReleaseOnly in the decode_cancel configurations
```

#13969 (`a5c4b2570a..73234a8d12` to `1867836d03..bd79ad1b35`):

```
4e993be3fb -> (none)  dagql: model reader cancellation in the persisted-decode singleflight
e6b45caeac -> (none)  dagql: add the decode_cancel configurations and accepted finding
86e0babb50 -> (none)  dagql: model the post-install decode failure and scope the barrier cancel arm
ed0f6cf2f5 -> (none)  dagql: split the decode install from the finish and model channels as generations
22605cb304 -> (none)  dagql: tighten two decode comments
61c5790ca2 -> (none)  dagql: retry persisted-decode joiners on leader cancellation, track pending lease sync
cb739d2b9c -> (none)  dagql: model the decode cancellation retry and pending lease sync, close the finding
9180e61009 -> (none)  dagql: decide decode leadership on state read under the mutex
2f59ec6361 -> (none)  dagql: update the decode_cancel_liveness header for the retry
1042f8419b -> (none)  dagql: sharpen the decode_cancel_liveness post-install wording
1ca59a2b59 -> 27703c4d5c  chore: regenerate tla-check module bindings
ebad6b1c15 -> (none)  fix: isolate mutable state for concurrent K3S fixtures
241af6a1a2 -> (none)  build: keep model checks in the dev environment
410d7cb525 -> (none)  test: disable report heartbeats in telemetry goldens
13e8677318 -> (none)  fix: avoid recursive cache locks in debug snapshots
a5c4b2570a -> (none)  test: signal queued writer while holding its lock
4f9753ced5 -> 6ba0f39cc2  dagql/tla: refresh drifted Go citations in the cache model
980d6aa39b -> e0317ec6cb  dagql/tla: model session-resource checks (inert until Handles is set)
98b17fcb5a -> ee0b97ead6  dagql/tla: add the resources configurations and the drift finding
ddf65cf4a6 -> cb0d5c705c  dagql: bound the TLA check's configuration fan-out to four at a time
0c9dd2ecf8 -> 1c59e2fd17  dagql: recognize initial-state invariant violations in the TLA check
8371bc5cba -> 1949995847  dagql/tla: correct the resources_restart attribution, README, and header comments
1c3c8fd0e4 -> 2b12051540  dagql/tla: check resources under SYMMETRY Symm
7808ab5831 -> da4201a0ed  dagql/tla: add resources_restart_gated, the harm of the requirement drift
39ec3f8068 -> e70bfeba5c  dagql: cap each TLA check JVM at 8 GiB
2931e0bb4b -> 9a6a851095  dagql/tla: README names both expected-red configurations
f9ec8a1b26 -> 4a2036594f  dagql/tla: refresh Go citations drifted by the reader-cancel fix
260b05bd8e -> 74ff8e94bf  dagql: recompute imported required session resources dependency-first
9c4566586d -> 0485ac4324  dagql: stop overwriting required session resources at decode install
0a0ae3ba57 -> d081a2b242  dagql: validate result-ID value loads against bound session resources
8851427778 -> c8d5674036  dagql/tla: model the dependency-first import recompute, close the drift finding
7c32e04f00 -> 6c9dc578f3  dagql: cascade required session resources to ancestors on late explicit deps
0fac594b9f -> 5c0ab735ed  dagql/tla: model late explicit dependencies and pin the retroactive-validation finding
09df2e64fb -> 7efbd6f5e6  dagql/tla: state exclusions as open modeling work, and the resource-validation guarantee
ad4935b899 -> a8bd19c265  dagql/tla: consolidate core into resources
1dd7fed592 -> fa2ee58ecd  dagql/tla: enable session-resource checks in release_prune, flush_roundtrip, persist
d0e2a3cb73 -> d524697587  dagql/tla: refresh citations for the resource-validation fixes, README names the open finding
94caf11d00 -> 8960a369a2  dagql/tla: restrict dep choices to code-reachable edges, re-attribute the resource-validation finding
b6b3950278 -> 157e565b88  dagql/tla: refine NoSpuriousErrors for self-released sessions, surface the joiner question
83f4a74365 -> 7c3baf6f3f  dagql/tla: reuse only session-owned results, complete the core absorption
3b4bec40eb -> 2a27cb905f  dagql/tla: guard held-result choices by ownership, not current satisfaction
1ad19046ed -> 32dd08bda6  dagql/tla: split selection-time checks from held-result ownership guards
b0f27529d4 -> 4e13de2ce3  dagql/tla: CanonicalPick fallback is a held-result guard
4320ca55a2 -> (none)  chore: regenerate tla-check module bindings
f67b1869f3 -> f022ea0fb7  test: expect the gated load refusal for cross-session secret ID replay
eb04bbe83a -> 72e7f6c98c  dagql: refuse session-resource deps on explicit retention edges
3b4894f09c -> 304df56930  dagql: re-check session resources after crossing the attach barrier
b069321d62 -> d41fc9e6a5  dagql: require clean attachment for result-ID load canonicalization
5f4b170a0a -> 5b26de4ffd  dagql/tla: model the growth fixes, close the gated-growth finding
c6f19394f9 -> 6d3cef4d33  dagql: freeze the session-resource handle of attached results
21cfa9e02f -> ba00a70a00  dagql/tla: track denied hits, exclude them from possession guards
a84c1aefc5 -> (none)  chore: regenerate tla-check module bindings
0c3fb25ed0 -> fcf19b1045  dagql: keep the registration guard on attached handle re-stamps
7d18b64bc6 -> 51731efac5  dagql/tla: model held-result choices as acquiring inner loads
60f3b80f0d -> d32d173b35  dagql/tla: split inner-load acquisition from consumption
df563e9150 -> 46f4b140d9  dagql/tla: give inner loads a pending phase between claim and delivery
4de76a5033 -> a2eb236e25  dagql/tla: move the TLA checks out of CI, add quick and some runners
92a73f34e6 -> 22cde98c19  dagql/tla: count pending inner operations, model attachment-time claims
4b77af35b4 -> f3f3c885dd  dagql/tla: separate admission from claim, pin the attach-release finding red
85a8df723c -> 09e568f8b3  dagql/tla: fail publication on any attachment claim error
4ef7cefef9 -> e82f53efc8  dagql/tla: order and latch attachment claim errors
804c4ae906 -> f7cc95f953  dagql/tla: record the round-nine counts
347d4a6582 -> 10c5b3d89a  dagql: allow requirement-carrying retention edges, re-validate at serve time
cc9fe6aaa4 -> 18bd6afad9  dagql/tla: model requirement-carrying retention edges, re-validate serves by selection capture
20a88f6ba1 -> f778419ecc  dagql: return a struct from sharedResultByResultID
fd60355b08 -> ff238cd1d0  dagql: convert parked readers to a miss when a producer's release fails attachment
1d7d1b1105 -> 8f7916dbb4  dagql: claim attachment targets before refresh, roll back failed publications
73234a8d12 -> 33f9c2a08c  dagql/tla: pin attachment targets, close the attach-release finding family
(new) -> 9285c661d4  dagql/tla: declare DelegatedReleaseOnly in the session-resource configurations
(new) -> bd79ad1b35  dagql/tla: assign this branch's constants in main's inherited configurations, drop two stale variable references
```

#14043 (`73234a8d12..1d85bd34aa` to `bd79ad1b35..38e0bf8b32`):

```
4e993be3fb -> (none)  dagql: model reader cancellation in the persisted-decode singleflight
e6b45caeac -> (none)  dagql: add the decode_cancel configurations and accepted finding
86e0babb50 -> (none)  dagql: model the post-install decode failure and scope the barrier cancel arm
ed0f6cf2f5 -> (none)  dagql: split the decode install from the finish and model channels as generations
22605cb304 -> (none)  dagql: tighten two decode comments
61c5790ca2 -> (none)  dagql: retry persisted-decode joiners on leader cancellation, track pending lease sync
cb739d2b9c -> (none)  dagql: model the decode cancellation retry and pending lease sync, close the finding
9180e61009 -> (none)  dagql: decide decode leadership on state read under the mutex
2f59ec6361 -> (none)  dagql: update the decode_cancel_liveness header for the retry
1042f8419b -> (none)  dagql: sharpen the decode_cancel_liveness post-install wording
1ca59a2b59 -> 38e0bf8b32  chore: regenerate tla-check module bindings
ebad6b1c15 -> (none)  fix: isolate mutable state for concurrent K3S fixtures
241af6a1a2 -> (none)  build: keep model checks in the dev environment
410d7cb525 -> (none)  test: disable report heartbeats in telemetry goldens
13e8677318 -> (none)  fix: avoid recursive cache locks in debug snapshots
a5c4b2570a -> (none)  test: signal queued writer while holding its lock
4f9753ced5 -> (none)  dagql/tla: refresh drifted Go citations in the cache model
980d6aa39b -> (none)  dagql/tla: model session-resource checks (inert until Handles is set)
98b17fcb5a -> (none)  dagql/tla: add the resources configurations and the drift finding
ddf65cf4a6 -> (none)  dagql: bound the TLA check's configuration fan-out to four at a time
0c9dd2ecf8 -> (none)  dagql: recognize initial-state invariant violations in the TLA check
8371bc5cba -> (none)  dagql/tla: correct the resources_restart attribution, README, and header comments
1c3c8fd0e4 -> (none)  dagql/tla: check resources under SYMMETRY Symm
7808ab5831 -> (none)  dagql/tla: add resources_restart_gated, the harm of the requirement drift
39ec3f8068 -> (none)  dagql: cap each TLA check JVM at 8 GiB
2931e0bb4b -> (none)  dagql/tla: README names both expected-red configurations
f9ec8a1b26 -> (none)  dagql/tla: refresh Go citations drifted by the reader-cancel fix
260b05bd8e -> (none)  dagql: recompute imported required session resources dependency-first
9c4566586d -> (none)  dagql: stop overwriting required session resources at decode install
0a0ae3ba57 -> (none)  dagql: validate result-ID value loads against bound session resources
8851427778 -> (none)  dagql/tla: model the dependency-first import recompute, close the drift finding
7c32e04f00 -> (none)  dagql: cascade required session resources to ancestors on late explicit deps
0fac594b9f -> (none)  dagql/tla: model late explicit dependencies and pin the retroactive-validation finding
09df2e64fb -> (none)  dagql/tla: state exclusions as open modeling work, and the resource-validation guarantee
ad4935b899 -> (none)  dagql/tla: consolidate core into resources
1dd7fed592 -> (none)  dagql/tla: enable session-resource checks in release_prune, flush_roundtrip, persist
d0e2a3cb73 -> (none)  dagql/tla: refresh citations for the resource-validation fixes, README names the open finding
94caf11d00 -> (none)  dagql/tla: restrict dep choices to code-reachable edges, re-attribute the resource-validation finding
b6b3950278 -> (none)  dagql/tla: refine NoSpuriousErrors for self-released sessions, surface the joiner question
83f4a74365 -> (none)  dagql/tla: reuse only session-owned results, complete the core absorption
3b4bec40eb -> (none)  dagql/tla: guard held-result choices by ownership, not current satisfaction
1ad19046ed -> (none)  dagql/tla: split selection-time checks from held-result ownership guards
b0f27529d4 -> (none)  dagql/tla: CanonicalPick fallback is a held-result guard
4320ca55a2 -> (none)  chore: regenerate tla-check module bindings
f67b1869f3 -> (none)  test: expect the gated load refusal for cross-session secret ID replay
eb04bbe83a -> (none)  dagql: refuse session-resource deps on explicit retention edges
3b4894f09c -> (none)  dagql: re-check session resources after crossing the attach barrier
b069321d62 -> (none)  dagql: require clean attachment for result-ID load canonicalization
5f4b170a0a -> (none)  dagql/tla: model the growth fixes, close the gated-growth finding
c6f19394f9 -> (none)  dagql: freeze the session-resource handle of attached results
21cfa9e02f -> (none)  dagql/tla: track denied hits, exclude them from possession guards
a84c1aefc5 -> (none)  chore: regenerate tla-check module bindings
0c3fb25ed0 -> (none)  dagql: keep the registration guard on attached handle re-stamps
7d18b64bc6 -> (none)  dagql/tla: model held-result choices as acquiring inner loads
60f3b80f0d -> (none)  dagql/tla: split inner-load acquisition from consumption
df563e9150 -> (none)  dagql/tla: give inner loads a pending phase between claim and delivery
4de76a5033 -> (none)  dagql/tla: move the TLA checks out of CI, add quick and some runners
92a73f34e6 -> (none)  dagql/tla: count pending inner operations, model attachment-time claims
4b77af35b4 -> (none)  dagql/tla: separate admission from claim, pin the attach-release finding red
85a8df723c -> (none)  dagql/tla: fail publication on any attachment claim error
4ef7cefef9 -> (none)  dagql/tla: order and latch attachment claim errors
804c4ae906 -> (none)  dagql/tla: record the round-nine counts
347d4a6582 -> (none)  dagql: allow requirement-carrying retention edges, re-validate at serve time
cc9fe6aaa4 -> (none)  dagql/tla: model requirement-carrying retention edges, re-validate serves by selection capture
20a88f6ba1 -> (none)  dagql: return a struct from sharedResultByResultID
fd60355b08 -> (none)  dagql: convert parked readers to a miss when a producer's release fails attachment
1d7d1b1105 -> (none)  dagql: claim attachment targets before refresh, roll back failed publications
73234a8d12 -> (none)  dagql/tla: pin attachment targets, close the attach-release finding family
0c6e4528f7 -> 39bc139bb1  hack/designs: stage 2 per-part evaluation design
cbadd77be9 -> 43301696a1  hack/designs: stage 2 revision 1 after review round 1
3f21c5d2dc -> 773d69cab5  dagql/tla: scope attach_release_reader to its scenario, close the run-budget breach
084f6ecb50 -> e4fcb750de  dagql/tla: per-group lazy evaluation
82b77ec884 -> dccff8b822  dagql: hold the whole-result lazy state as one inline evaluation group
8c3f85e879 -> c865ad8561  dagql: per-group lazy evaluation and EvaluateParts
1189ff914b -> f17357de9a  core: container part keys, per-group latching, and the parts routing layer
3f453d0861 -> f437ee8e59  core: convert the metadata-only container ops to per-group evaluation (template A)
3f8ff27cd1 -> 3f16f32163  core: refine the exec to a joint output group; withRootfs writes fs only
fea87f5ed5 -> f9ca38b32c  core: narrow the selectors and readers to the parts they touch
dcc2d15a00 -> 2cb53946c0  internal-docs: describe the per-part lazy evaluation contract
433001157b -> 55270b37ab  dagql/tla: scope resources_gated_growth off persistable intent
51060c4fe3 -> 99e22d2e31  hack/designs: stage 2 revision 2 after implementation review
caace209b6 -> e2b76f45d1  dagql: route empty group resolution to pending cache-side groups
259819ac13 -> 30377911ac  core: delegation always copies the parent part; a pre-set accessor proves nothing
b05bf66d46 -> 43baef2f6a  core: hold LazyMu across the consumed-check and the Lazy clear
171b8dfca6 -> 4c9d1aff48  core: pin the ruled VolatileEnv expand corner; record the ruling in the design flag
76ec619b0b -> 961a7548fe  hack/designs: stage 2 revision 3 after the fix round
ff82bdbaf7 -> 19b06c3bda  core: synchronize the routing reads of the lazy op pointer with the refined clear
b4cbea1504 -> 46690a5360  hack/designs: stage 2 revision 4, correct the op-pointer clear scope
7dfff49890 -> 2dd8b37a32  core: make the whole-op lazy latch atomic
a4f652c370 -> 6eb0532c29  core: one rule for the lazy op pointer - every post-construction access under lazyOpMu
393014ba8f -> 2b6c301b38  hack/designs: stage 2 revision 5, adopt the uniform op-pointer rule
8cfb965dc2 -> 0063824e1e  hack/designs: per-part lazy evaluation of containers, one holistic design record
3063576869 -> b648021afc  core: evaluate container image sources by part
6d45a3137d -> b1916b3da8  core: evaluate mount metadata mutations by part
cc92a028dc -> d2fde58acc  core: evaluate mounted snapshot sources by part
52c59b65d5 -> 8d5c2004f2  core: evaluate container path writes by part
75be0c4d88 -> 1a14a22e1d  core: strengthen per-part mount coverage
7f5f9f05fe -> 2ccb08772f  core: narrow container image preparation
af0569d88b -> 2909b966d3  core: preserve parallel image part evaluation
172e613d26 -> eed0c3c4f5  hack/designs: cite the conversion commits by title, not by hash
b9fbe32bee -> c4599f8677  ci: refresh generated clients and satisfy Go lint
08133fe391 -> (none)  ci: align generated clients with the release schema
6d353bb8fc -> f4f7af176e  core: consume final container delegations
7b908d2f06 -> 8d3a2c4483  dagql: evaluate resolved lazy groups concurrently
5a441a2f72 -> 7a7a14b01d  dagql: keep partial resume spans pending
6089d680f9 -> 2dbf669864  core: evaluate selector rootfs before nested calls
308f1ccc36 -> 320ebeda3e  dagql: classify abandoned lazy attempts
306245a006 -> 9fea10f760  dagql: assert concurrent resume completion
0861323d07 -> 75d18c6e2d  dagql: extract lazy group attempt preparation
1da70051da -> 6eda99b0cc  hack/designs: record the fix round and the decisions behind it
1d85bd34aa -> b16275d473  dagql/tla: model final parent copy sweeps
(new) -> 9b54658e9d  dagql/tla: assign every declared constant in every configuration
```

### #14043 `sipsma/remote-cache-track7-per-part-evaluation`

Candidate head `38e0bf8b32` (working branch `pkg/track7`), 44 commits on
#13969's `bd79ad1b35`: 42 of the 43 originals (`08133fe391`, "ci: align
generated clients with the release schema", became a no-op once main's
generated clients were taken and is dropped), plus two new: `9b54658e9d`
"dagql/tla: assign every declared constant in every configuration" and
`38e0bf8b32` "chore: regenerate tla-check module bindings". The
generator's `dagger.lock` change was discarded again.

Conflicts and resolutions:

1. `3f21c5d2dc`, `attach_release_reader.cfg`: the commit drops `SYMMETRY
   Symm` at the line where #13969's fixup had placed
   `DelegatedReleaseOnly`. Constant kept, symmetry line dropped as the
   commit does.
2. `b9fbe32bee` ("ci: refresh generated clients and satisfy Go lint"):
   four generated clients (cli-dev, engine-dev, tla-check and its
   internal client) conflicted with main's regenerated ones; main's
   taken. Its five hand-written `nolint` comments in core/container.go,
   core/container_parts_test.go and core/schema/container.go applied
   without conflict and are kept.
3. `08133fe391`: three generated clients; main's taken, the commit
   became empty and was dropped.
4. `5a441a2f72` (partial resume spans): engine/telemetryattrs/attrs.go
   and dagql/dagui/spans.go, both additive on both sides (main's
   `LogRoleAttr`/`DagLeftRunningAttr` beside the commit's
   `DagPartialAttr`); both kept.

Adaptations: the audit on the rebased tip (corrected script) found the
five `lazy_parts` configurations this PR adds without
`DelegatedReleaseOnly`, and the two inherited configurations without the
seven part-model constants this PR declares (`ReleaseSessions`,
`PersistableIntent`, `LazyGroups`, `LazyParts`, `PartGroupOf`,
`GroupNeeds`, `ModelPartDelegation`); `9b54658e9d` assigns them, the
inherited two taking the whole-parts, no-delegation values every other
non-parts configuration uses. One slip caught before the commit was
sent: `lazy_parts_delegate.cfg` puts SYMMETRY before CONSTANTS, and the
first insertion landed outside the block; moved inside, and every
configuration checked for an assignment outside its block (none). The
bindings' source maps were stale by 13 lines (the reviewer's read; struct
at 136, CacheLifecycle 201, ClientLifecycle 278, One 329); `38e0bf8b32`
regenerates them on `remote-cache-engine`, source-map lines only. Audit
on `38e0bf8b32`: 0 findings over 29 constants, 10 variables, 40
configurations.

Tests on `b16275d473` (the tip before the two TLA-only and
generated-only commits; the Go tree is identical), clean tree, `go test
-v -count=1 -timeout 60s ./dagql/ ./dagql/dagui/ ./core/ ./core/schema/
./engine/telemetryattrs/`, log /tmp/pkg-track7-tests.log, exit 0: ok
dagql 2.063 s, ok dagql/dagui 0.010 s, ok core 7.094 s, ok core/schema
8.383 s, telemetryattrs has no test files; 1057 top-level PASS, 0 FAIL.
`go build ./...` ok. Engine-suite changes (none in core/integration for
this PR) and TLC execution are left to CI.
