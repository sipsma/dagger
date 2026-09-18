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

Every original commit of each PR paired with its candidate, in order,
generated from the immutable original ranges (the old base `0d031c08ef`
and the original tips `a5c4b2570a`, `73234a8d12`, `1d85bd34aa`), not
from the branch refs, which have since been force-pushed. Pairing is by
subject in order within each PR's own range; many-to-one, adapted and
dropped cases are annotated. "(new)" marks a candidate with no original.
First version of this section was generated from the moving refs and
was wrong (the reviewer caught it); this is the corrected one.

#13962 (`0d031c08ef..a5c4b2570a`, 16 commits, to `upstream/main..1867836d03`, 17):

```
4e993be3fb -> a3a8829369  dagql: model reader cancellation in the persisted-decode singleflight
e6b45caeac -> f53f017c30  dagql: add the decode_cancel configurations and accepted finding
86e0babb50 -> 50ef32e0b1  dagql: model the post-install decode failure and scope the barrier cancel arm
ed0f6cf2f5 -> dc3d0ecdfc  dagql: split the decode install from the finish and model channels as generations
22605cb304 -> e0de990aa3  dagql: tighten two decode comments
61c5790ca2 -> a652713ca6  dagql: retry persisted-decode joiners on leader cancellation, track pending lease sync
cb739d2b9c -> 5dc904eff6  dagql: model the decode cancellation retry and pending lease sync, close the finding
9180e61009 -> 715681dbe8  dagql: decide decode leadership on state read under the mutex
2f59ec6361 -> 2ec7c3a9bb  dagql: update the decode_cancel_liveness header for the retry
1042f8419b -> 998630bd54  dagql: sharpen the decode_cancel_liveness post-install wording
1ca59a2b59 -> c6653c6d04  chore: regenerate tla-check module bindings  (adapted: fresh regeneration on main, message kept)
ebad6b1c15 -> ce04303215  fix: isolate mutable state for concurrent K3S fixtures
241af6a1a2 -> bf1ca7c143  build: keep model checks in the dev environment
410d7cb525 -> 3cf71b8367  test: disable report heartbeats in telemetry goldens
13e8677318 -> d73da1533d  fix: avoid recursive cache locks in debug snapshots
a5c4b2570a -> 328429c0c1  test: signal queued writer while holding its lock
(new) -> 1867836d03  dagql/tla: declare DelegatedReleaseOnly in the decode_cancel configurations
```

#13969 (`a5c4b2570a..73234a8d12`, 52 commits, to `1867836d03..bd79ad1b35`, 53):

```
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
4320ca55a2 -> 27703c4d5c  chore: regenerate tla-check module bindings  (combined with a84c1aefc5 into one regeneration)
f67b1869f3 -> f022ea0fb7  test: expect the gated load refusal for cross-session secret ID replay
eb04bbe83a -> 72e7f6c98c  dagql: refuse session-resource deps on explicit retention edges
3b4894f09c -> 304df56930  dagql: re-check session resources after crossing the attach barrier
b069321d62 -> d41fc9e6a5  dagql: require clean attachment for result-ID load canonicalization
5f4b170a0a -> 5b26de4ffd  dagql/tla: model the growth fixes, close the gated-growth finding
c6f19394f9 -> 6d3cef4d33  dagql: freeze the session-resource handle of attached results
21cfa9e02f -> ba00a70a00  dagql/tla: track denied hits, exclude them from possession guards
a84c1aefc5 -> 27703c4d5c  chore: regenerate tla-check module bindings  (combined with 4320ca55a2 into one regeneration)
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

#14043 (`73234a8d12..1d85bd34aa`, 43 commits, to `bd79ad1b35..38e0bf8b32`, 44):

```
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
08133fe391 -> (dropped)  ci: align generated clients with the release schema  (no-op once main's generated clients were taken)
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
(new) -> 38e0bf8b32  chore: regenerate tla-check module bindings
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

Tests on `b16275d473` (the tip before the two commits above it, which
change only TLA configurations and the nested module's generated
`dagger.gen.go` source-map literals; the tested packages are unchanged),
clean tree, `go test
-v -count=1 -timeout 60s ./dagql/ ./dagql/dagui/ ./core/ ./core/schema/
./engine/telemetryattrs/`, log /tmp/pkg-track7-tests.log, exit 0: ok
dagql 2.063 s, ok dagql/dagui 0.010 s, ok core 7.094 s, ok core/schema
8.383 s, telemetryattrs has no test files; 1057 top-level PASS, 0 FAIL.
`go build ./...` ok. core/integration has no changes in this PR. TLC
was not run (dev-only; CI does not run tla-check either).

## CI stewardship (per push)

Convention from the coordinator (18:52 UTC): after every push the
PR's check results are recorded here (check, result, trace id) and every
failure is triaged before anything is pushed above it. The lowest
unmerged PR is the triage priority, since Erik merges from the bottom as
each turns green.

### Merge of #13962 and the automatic rebase above it

`#13962` merged into main by Erik at 18:51:57 UTC (main `8b129f76ce`).
GitHub's stacks feature then rebased #13969 and #14043 onto it
automatically (stamped with Erik's identity): heads `ef14efc563` and
`c979b2af31`. The same happens after every merge: heads move, trees stay
identical, fresh check runs start on new merge commits. Check by the Stack integrator: `git range-diff
bd79ad1b35..38e0bf8b32 ef14efc563..c979b2af31` shows every commit equal
(no re-review needed); `c979b2af31` has 44 commits above `upstream/main`
as before.

Fixes redirected to main because their PR is merged: #13962: none so
far (nothing in the corrections table targets it).

### Results on the previous heads (`bd79ad1b35`, `38e0bf8b32`)

Triaged by the coordinator, not by me (I had not watched the runs; the
convention above is the correction):

- #14043 golangci-lint:lint-all FAIL, real: gocyclo reports
  `(*Cache).evaluateGroup` (dagql/cache.go) at 31, limit 30. It was 30
  at the original tip `1d85bd34aa`; main's merged changes added a branch.
  The function does not exist at #13969's tip, so the fix belongs in
  #14043. Trace `dddc18e9df5ee38de1361dcee47bcbd1`.
- #13969 test-split:test-base FAIL: `TestAgentDebugServerContextCancellation`
  in internal/cmd/dagger (shell_test.go:183, "context cleanup must close
  the debug listener"). #13969 does not touch internal/cmd/dagger; the
  test is main's agent code. Trace `731073a1bb72040377314016ee6a6ccf`.
  Other test-base packages passed (core 413, dagql 337, core/integration
  42 and the rest).

`dagger cloud rerun --commit ef14efc563 --check test-split:test-base`
answered "no Cloud checks found for the target commit": Cloud keys the
checks by the PR merge commit, and Erik's rebase had already triggered
fresh runs on new merge commits (#13969 `a7684c7257`, #14043
`d3718d6694`), so the pending run on the new head is the rerun; no
second run issued. Results below when they land.

### #14043 gocyclo follow-up

Candidate `7b663cb1ad`, re-messaged without the agent attribution trailer as `0259a9c951` (same tree, Erik's signoff kept), on `pkg/track7-fix`, one commit on `c979b2af31`,
dagql/cache.go only (+29/-25): the goroutine's `runEval` closure in
`evaluateGroup` becomes the method `runLazyEvalBody(callbackCtx, shared,
lazyEval) (context.Context, bool, error)`, returning the leased context
(the input when the lease fails), bodyDone and the first error; the
caller reads the three values where it called `runEval()`. No nolint.
Local `gocyclo -over 25 dagql/cache.go`: evaluateGroup 26 (was 31).
Tests on the fixed tree (`/tmp/pkg-track7-fix-tests.head`: `c979b2af31`
plus the one dirty file, identical to `7b663cb1ad`): `go test -v
-count=1 -timeout 60s ./dagql/`, log /tmp/pkg-track7-fix-tests.log, exit
0, ok dagql 2.363 s, 349 top-level PASS, 0 FAIL, 0 top-level SKIP.
`dagger check golangci-lint:lint-all` on the container engine, log
/tmp/pkg-track7-fix-lint.log: exit 1, one finding, `dagql/cache.go:4173:4
ineffassign ineffectual assignment to callbackCtx`, no gocyclo finding.
I had pushed `0259a9c951` before this result (the coordinator's call,
withdrawn: a CI-fix commit now waits for the same check to pass locally
before review and push). CI lint-all on the new merge commit failed the
same way. Follow-up `7b3a3d5bd1`: `runLazyEvalBody` returns only
`(bool, error)`; the lease result is assigned straight to `callbackCtx`
inside it; the caller no longer keeps a `callbackCtx` variable (the
original closure never read it after `runEval()` either). Local
`ineffassign ./dagql/` clean, gocyclo evaluateGroup 26. Tests
(`/tmp/pkg-track7-fix2-tests.head`: `0259a9c951` + one dirty file,
pre-commit, identical to the commit), `go test -v -count=1 -timeout 60s
./dagql/`, log /tmp/pkg-track7-fix2-tests.log, exit 0, ok dagql 1.978 s,
349 top-level PASS, 0 FAIL. Lint on `7b3a3d5bd1`, log
/tmp/pkg-track7-fix2-lint.log: `golangci-lint:lint-all DONE [3m4s]`, exit
0, no findings. Reviewer approved code, tests and lint line; pushed,
`sipsma/remote-cache-track7-per-part-evaluation` = `7b3a3d5bd1`.

### #14049 `sipsma/remote-cache-track8-terminology`

Candidate head `90381213dd` (working branch `pkg/track8`), 8 commits on
#14043's `7b3a3d5bd1` (rebased from `38e0bf8b32` through `c979b2af31`,
`0259a9c951`, `7b3a3d5bd1` as #14043 moved; the seven commits applied
cleanly each time and the trees differ only by #14043's own commits): the 7 originals plus one regeneration
(`90381213dd`). The PR is a wording sweep: it renames the LLM history
field and the recording model namespace, the attachment-error and
resource-requirement names in the TLA model and its configurations, and
the word "gate" across comments and prose. Main moved under all of it:
newer LLM code (spawn/agent handles, `sessionAgent`, the replayed-results
mechanism), main's rename of the shared-work lease to the operation
lease, main's session barrier, and the PHP static reference removed on
main (`bc9cd6ef98`).

Conflicts and resolutions:

1. `ad9706a2f6` (LLM rename), 14 files. core/llm.go: main's
   `emitMessageSpan` signature (with `replayedResults`) and comment,
   `Replay` renamed to `EmitHistory`; main's new identifiers
   `replayedToolResult`/`replayedResults` keep main's names (they are
   main's mechanism, not part of the original rename). core/schema/llm.go:
   main's `spawn`/`agent` resolvers kept, `replay` resolver renamed.
   core/llm_recording_test.go (renamed from llm_replay_test.go): main's
   five newer tests kept, calling `recordingTestRecorder` and
   `EmitHistory`; the shared test's name takes the commit's
   `TestRecordedResponseProviderEmitsPerToolCallDisplaySpans`; main's
   own test names (`TestReplay…`) unchanged. internal/cmd/dagger/llm.go
   and shell_commands.go: main's `sessionAgent` receiver and
   `llm.Target()` call sites with the commit's `historyCtx` parameter.
   core/integration/llm_test.go header comment: main's "`dagger script`"
   with the commit's "permission check". Generated clients (Go, TypeScript,
   PHP): main's `spawn` bindings kept, `replay` dropped in favour of the
   commit's `emitHistory`; then regenerated at the tip (below). The three
   `docs/static/reference/php` files the commit edited were deleted on
   main; deletion kept.
   Adaptation inside the same commit: main's newer callers of the helper
   the commit renames (`cannedReplayModel` → `cannedRecordingModel`, 32
   call sites in core/integration/agent_*_test.go and
   llm_object_tools_test.go, plus `recordingTestRecorder` in
   core/mcp_test.go), and main's two literal uses of the old model prefix
   (`"replay/"` in core/integration/agent_runtime_test.go and the
   lazy-forcing dang testdata module) now use the renamed helper and the
   `recording/` prefix, since the commit removes the `replay/` prefix from
   the router. Without these the integration package does not compile
   (`go vet` caught it) and the two tests would route to a model that no
   longer exists.
2. `dfe3e125f3` (wording), 4 files, all comment-only after main's
   changes: core/agents.go main's `AgentMiddlewareGroup` receiver with the
   commit's comment; dagql/cache.go main's lease field names
   (`releaseOperationLeaseFn`, main's `450b88f330`) with the commit's
   comment wording; engine/server/session.go one comment word taken
   ("completeness check"), the other three hunks are blocks main rewrote
   or removed (the synchronous carrier flush replaced by main's barrier;
   the client-runtime construction) so main's text stands;
   internal/cmd/dagger/cloud_rerun_query.go main's command name with the
   commit's phrase.
3. `3d648f9eb0` (model rename), 11 files: ten configurations where main's
   added `DelegatedReleaseOnly = FALSE` line (and, in resources.cfg and
   rollback.cfg, main's `SharedLeaseReleasedWhenRetired` invariant) sat on
   the same lines as the renamed invariants; resolved by keeping every
   constant line and main's extra invariant and taking the commit's
   renamed invariant list. .dagger/modules/tla-check/main.go: the
   configuration map takes the commit's renamed keys plus main's
   `release_wait` and `orphaned_lease` (mutation) entries; the header
   comment takes main's sentence about mutation configurations with the
   commit's "check" wording.
4. The other four commits applied cleanly.

Checks on the tip: the corrected two-direction TLA audit (0 findings; 29
constants, 10 variables, 40 configurations); every INVARIANTS name in
every configuration is defined in the spec; the module's configuration
map and the files on disk agree both ways (the seven client names in the
map are `engine/server/tla/ClientLifecycle_*.cfg`, main's, unchanged);
no old name (`poisoned`, `drain_escape`, `release_steal`,
`resources_gated_growth`, `ReturnedGated`, `NoLaunderedServe`,
`NoRetainedPoisonedEntry`, `cannedReplayModel`, `"replay/"`) remains
under dagql/tla, the module, core or internal. TLC not run (dev-only; CI
does not run tla-check).

Regeneration (`90381213dd`): `dagger generate -y docs:references go-client:generate
typescript-client:client-library php-client:api python-client:client-library
elixir-client:client-library rust-client:apiclient go-sdk:generate` on
`remote-cache-engine` against the tree of the seven rebased commits (log
/tmp/pkg-track8-gen.log, exit 0). It changed two files: the tla-check
module bindings' description string (this branch's main.go wording) and
the PHP client's `emitHistory`, which the current generator emits in the
ID-returning form (`loadObjectFromId`) rather than the `return $this`
form the original commit carried. Every other generated file (Go,
TypeScript, Python, Rust, Elixir clients, docs/docs-graphql/schema.graphqls,
the other module bindings) was already identical to the generator's
output, so the hand resolution of the generated-client conflicts was
exact. `dagger.lock` unchanged this time.

Tests: on `90381213dd`, clean tree (/tmp/pkg-track8-tests.head), `go build
./...` and the tla-check module build ok; `go test -v -count=1 -timeout
60s` over the 13 packages the PR touches outside generated code and the
engine suite (cmd/codegen/generator/typescript/templates, core,
core/schema, dagql, dagql/dagui, dagql/idtui, engine,
engine/client/pathutil, engine/clientdb, engine/server,
engine/telemetryattrs [no test files], internal/cmd/dagger,
internal/cmd/dagger/llmconfig), log /tmp/pkg-track8-tests.log, exit 1:
eleven packages passed, one (engine/telemetryattrs) has no test
files, `internal/cmd/dagger` FAIL on one test,
`TestAgentDebugServerContextCancellation` (shell_test.go:183, "context
cleanup must close the debug listener", the dial after cancel succeeds).
Totals 1816 top-level PASS, 1 FAIL, 5 top-level SKIP. The same test
fails identically on pristine `upstream/main` `8b129f76ce` in a clean
worktree (`go test -count=1 -timeout 60s -run
TestAgentDebugServerContextCancellation ./internal/cmd/dagger/`, log
/tmp/pkg-main-debugserver.log, FAIL, same line and message); the test
and the code are main's (Alex Suraci, `ec459b73fe`, `7e3570bd2b`) and no
stack commit touches them. It is the test that failed in #13969's CI
test-base on the previous head. Reported to the coordinator as a main
defect; main's own test-base on `8b129f76ce` was still pending when
checked. Engine-suite changes (26 files under core/integration, all
renames per the check above) are CI-only.

LLM-code rule check (main is the source of truth for LLM code; our
commits apply only renames and plumbing there): `git diff upstream/main
`90381213dd` -- core/llm* core/schema/llm* core/agents.go core/mcp*
internal/cmd/dagger/llm* internal/cmd/dagger/shell*
internal/cmd/dagger/llmconfig core/integration/llm_test.go
core/integration/agent_* core/integration/testdata/modules/dang/lazy-forcing`
touches 29 files, 130 hunks (diff saved as /tmp/pkg-track8-llm.diff). A
classifier normalized every removed line through the commit's rename
table (`replay`→`recording`/`emitHistory` identifiers and prefixes,
`replayCtx`→`historyCtx`, `LLMReplayer`→`RecordedResponseProvider`,
`cannedReplayModel`→`cannedRecordingModel`, `replayTestRecorder`→
`recordingTestRecorder`, the renamed test function) and compared it with
the added lines: 60 hunks are exactly the rename, 62 change only
comment or docstring lines, and the remaining 8 (listed by the script)
are the same rename applied to local variable names (`replay`→
`recording`, `replayer`→`provider`) and to message strings ("is not
replayable"→"is not a recording", "failed to replay session history"→
"failed to emit session history", "must not replay"→"must not reapply").
No hunk adds, removes or changes a type, field, function, argument or
control flow beyond the rename; main's spawn/agent handles,
`sessionAgent`, `llm.Target()`, `replayedResults`, `AgentMiddlewareGroup`
and the operation-lease names stand as main has them. The files with
non-LLM conflicts (dagql/cache.go, engine/server/session.go,
internal/cmd/dagger/cloud_rerun_query.go, core/agents.go) were checked
the same way in their #14049 hunks: comment-only. Reviewer: please check
this explicitly.

Hash map (`1d85bd34aa..18f0d54c86`, 7 commits, to `7b3a3d5bd1..90381213dd`, 8):

```
ad9706a2f6 -> 513042de80  llm: name history emission and recorded-response providers precisely
dfe3e125f3 -> 26cc263af4  core: describe loading, telemetry, and patch operations precisely
3d648f9eb0 -> b7fcd33673  dagql: name attachment errors and resource requirements directly
3baa063814 -> 043e603f29  core: clarify session isolation and root-boundary checks
b98ae8094b -> 836acceb58  dagql: clarify wait-link validation prose
13953761d4 -> ae92face0e  build: sync generated module descriptions with source
18f0d54c86 -> 28c6447442  changes: document experimental LLM naming updates
(new) -> 90381213dd  chore: regenerate tla-check module bindings and the PHP client
```

### Known main failure: TestAgentDebugServerContextCancellation

Coordinator's ruling: not stack evidence, does not block. Later runs
cite this entry instead of re-explaining. `internal/cmd/dagger`,
shell_test.go:183, "context cleanup must close the debug listener" (the
dial after cancel succeeds). Fails identically on pristine `upstream/main`
`8b129f76ce` on this host (/tmp/pkg-main-debugserver.log) and in
#14049's candidate run (/tmp/pkg-track8-tests.log); failed once in
#13969's CI test-base on the previous head (trace
`731073a1bb72040377314016ee6a6ccf`). No stack commit touches the debug
server or the test (main's, Alex Suraci, `ec459b73fe`, `7e3570bd2b`).
Coordinator's finding: a scheduling race, not deterministic. On pristine
main `8b129f76ce` on this 16-CPU host it fails 8 of 20 runs at default
GOMAXPROCS and 0 of 3 at GOMAXPROCS 1 or 8. Cause: `http.Server.Close`
closes only listeners that `Serve` has already registered;
`startDebugServer` starts `Serve` in a goroutine, so a stop that wins
the race closes nothing and the listener is closed later by `Serve`'s
deferred close, after the test's dial. A fix (keep the listener on the
handler, close it directly in both stop paths) passes 20 of 20; it is
committed locally as `6d2b931998` on `sipsma/debug-server-close-listener`
in /tmp/main-8b129f76ce, not pushed, pending Erik's decision. Main's
push CI does not run test-base, so #13969's run is the only CI signal.
No action for the stack beyond this record. #13969's test-base on its
new merge commit `a7684c7257` passed, so the earlier CI failure was one
occurrence of the race.

### CI results on the current heads (running record)

- #13969 `ef14efc563` (merge commit `a7684c7257`): golangci-lint:lint-all
  pass; test-split:test-base pending at last check; no failed check.
  Human approval present (grouville, 18:51:24 UTC). Merge follows the
  rule (all green, human approval, head equals reviewed tree) once the
  pending checks settle.
- #14043 `7b3a3d5bd1`: golangci-lint:lint-all pass on the new merge
  commit (the fix holds in CI); other checks running.
- main `8b129f76ce`: 30 statuses, all success; its push workflow does
  not run test-split:test-base (only test-client-generator and
  test-llm), so main gives no CI signal on the known debug-listener
  failure; #13969's test-base run is the signal.

### Merge of #13969

Merged #13969 at main `32d989e377` (19:15:28 UTC) by the Stack
integrator under Erik's merge rule: 85 checks pass, 1 skipping
(check-for-changelog), 0 pending, 0 failed; human approval by grouville;
head `ef14efc563` equal to the reviewed candidate. `gh pr merge --merge`
is refused for stack members ("must be merged using the asynchronous
merge REST API"); used `PUT /repos/dagger/dagger/pulls/13969/merge-async`
with `sha` pinned to the head and `merge_method=merge` (accepted 202,
uuid `c68d8962-828f-4689-a61f-f6eecfa59609`, result "merged", sha
`32d989e377`). Automatic rebase of #14043 to `4a09602c18`, tree
identical: `git range-diff ef14efc563..7b3a3d5bd1
upstream/main..4a09602c18` all equal (46 commits); base retargeted to
`main`; grouville's approval carried. Fresh check runs on the new merge
commit are being watched. Fixes redirected to main because #13969 is
merged: none so far (nothing in the corrections table targets it).

#### #14049 approval and push

Reviewer approved `90381213dd` (record corrections above applied). The
base had moved under it (#13969 merged, #14043 automatically rebased to
`4a09602c18`, tree identical to `7b3a3d5bd1`): first force-push
`90381213dd` (on `7b3a3d5bd1`, lease on the original tip `18f0d54c86`),
then the eight commits rebased onto `4a09602c18` as `46bbd9b9ff`
(`git range-diff 7b3a3d5bd1..90381213dd 4a09602c18..46bbd9b9ff` all
equal, no conflicts) and force-pushed with lease on `90381213dd`.
`sipsma/remote-cache-track8-terminology` = `46bbd9b9ff`; GitHub shows
base `sipsma/remote-cache-track7-per-part-evaluation`, 8 commits. Fresh
check runs watched.

### #14050 `sipsma/remote-cache-container-part-persistence`

Candidate head `91b4f6247e` (working branch `pkg/track9`, worktree
/tmp/pkg-track9), 14 commits on #14049's pushed `46bbd9b9ff` (built on `90381213dd`, then moved onto `46bbd9b9ff` after #14049's re-base; `git range-diff 90381213dd..ef9a9bc2bf 46bbd9b9ff..91b4f6247e` all equal): the 12 originals
(`18f0d54c86..9375bbb985`) plus `af2c52cb15` "dagql/tla: assign every
declared constant in every configuration" and the bindings regeneration
`91b4f6247e`.

Conflicts: one, in `af8efcb81d` ("Model container part persistence and
local snapshot opening"), dagql/tla/CacheLifecycle.tla, one hunk: main's
`sessionRelease` record fields (`releaseReturned`, `waitRequested`,
`waitReturned`) and the commit's `flushed'.done` reset under
`ModelContainerPartPersistence` sat on adjacent lines; both kept. The
other eleven commits applied cleanly.

Adaptation (`af2c52cb15`): the corrected audit on the rebased tip found
the three container configurations this PR adds
(`container_joint_restore`, `container_part_restart`,
`container_sweep_restart`) without main's `DelegatedReleaseOnly`, and
main's two inherited configurations (`orphaned_lease`, `release_wait`)
without this PR's `ModelContainerPartPersistence`; all five now assign
FALSE (the value every other non-mutating configuration uses), inside
the CONSTANTS block. Audit on the tip: 0 findings; 30 constants, 10
variables, 43 configurations; every INVARIANTS name defined; module map
and configuration files agree both ways; no assignment outside a
CONSTANTS block. TLC not run (dev-only; CI does not run tla-check).

Regeneration (`91b4f6247e`): `dagger generate -y go-sdk:generate` on `remote-cache-engine` (log
/tmp/pkg-track9-gen.log, exit 0): the tla-check bindings only, 14
source-map lines (the struct moved from 136 to 141, the functions by the
same five lines: this PR's two map entries, the container comment and
the snapshot lines above them). No `dagger.lock` change.

LLM-code rule: the PR touches no LLM file (core/llm*, core/schema/llm*,
core/agents.go, core/mcp*, internal/cmd/dagger/{llm*,shell*,llmconfig},
the agent integration tests); nothing to check.

Tests on `86fbb2af93` (the pre-move hash of `af2c52cb15`; tree identical), clean tree (/tmp/pkg-track9-tests.head; the
regeneration above it changes only the nested module's generated
bindings, so the tested packages are unchanged): `go build ./...` and
the tla-check module build ok; `go test -v -count=1 -timeout 60s ./core/
./dagql/` (the two packages the PR touches outside generated code and
the engine suite), log /tmp/pkg-track9-tests.log, exit 0: ok core
8.436 s, ok dagql 2.674 s; 805 top-level PASS, 0 FAIL, 0 top-level SKIP plus one inherited
nested SKIP
(`TestCacheContextCancel/last_waiter_canceled_fn_returns_value_still_releases`).
The one core/integration file the PR changes is CI-only.

Review round 1 (B1): the rebased `925578f9ea` (original `9375bbb985`,
"test: place complexity notes with container persistence") adds
`//nolint:gocyclo` to `evaluateGroup`; with #14043's `runLazyEvalBody`
extraction in the base the function is at 27 (reviewer's count; local
gocyclo agrees), under the limit of 30, so the directive is unused and
`.golangci.yml`'s nolintlint rejects unused directives. Follow-up commit
on the candidate drops that one directive (the integration fixture's
directive in core/integration/engine_persistence_test.go stays; nothing
moves to #14043, which has no directive). Comment-only, no unit rerun;
the local lint check runs once on the result and its terminal line is
sent with the candidate.

Hash map (`18f0d54c86..9375bbb985`, 12 commits, to `46bbd9b9ff..91b4f6247e`, 14):

```
e0e2845fab -> ecbf755e1b  docs: design container part persistence
7804542907 -> 1044120e42  docs: refine container part persistence design
e6e88bb40c -> be28cb9973  docs: record container part persistence review convergence
af8efcb81d -> 226ae38b03  Model container part persistence and local snapshot opening
41654b9887 -> b0ff42d1ef  Preserve independent completion evidence across model restart
e3df77508b -> 53eab4dc2b  dagql: record bounded container persistence evidence
fdf133696a -> 1fde67954b  core: preserve completed container parts across restart
b1a9a7d714 -> 389c2728f7  core: verify restored container ownership and reporting
11a7b0b24d -> 3eaf7fd291  docs: record container part persistence validation
5484661e4f -> 34bb0d4607  changes: document container part persistence
b0f1bfe679 -> 26562832fe  core: clean up persisted container lint findings
9375bbb985 -> 925578f9ea  test: place complexity notes with container persistence
(new) -> af2c52cb15  dagql/tla: assign every declared constant in every configuration
(new) -> 91b4f6247e  chore: regenerate tla-check module bindings
```

### #14219 `sipsma/debug-server-close-listener` (main PR, under the same watch)

The debug-listener race fix (known-failure entry above), head
`577d0a04f5` on main `32d989e377`, Erik approved the change; a PR
against main, not a stack member. Same stewardship: checks recorded,
failures triaged, merge by the rule (all green, human approval on
GitHub) with plain `gh pr merge --merge`. Once merged the known-failure
entry becomes "fixed on main at <hash>" and PRs rebased onto a main that
contains it no longer carry it. First look: 76 pass, 8 pending, 1
skipping, 1 fail: test-split:test-provision, the same check failing on
#14043 (`4a09602c18`) and #14049 (`46bbd9b9ff`) at the same time;
triage below.

### CI triage, 19:22 UTC window: registry.dagger.io outage

Trace ids come from `gh pr checks <n>`'s description column ("Run
`dagger trace <id>`"); traces in /tmp/ci-trace-<pr>-<check>.log, full
check logs via `dagger cloud logs <id> --check <name> -o <file>`.

- test-split:test-provision failed on #14043 (`4a09602c18`, trace
  `84cc0212512994062b62c0d14674929c`), #14049 (`46bbd9b9ff`, trace
  `c94c28f2a4d5fa864ccd6070585f08ec`) and #14219 (`577d0a04f5`, trace
  `0d8a8d7c8d8ec26d2338a593398a8866`, coordinator's triage). Same
  failure in all three: TestProvision's TestImageDriver and
  TestImageDriverGarbageCollectEngines subtests fail resolving
  `registry.dagger.io/engine:v0.16.1` (and v0.16.0, and blob GETs) with
  "unexpected status from HEAD request ... 500 Internal Server Error"
  (/tmp/ci-logs-14043-provision.log: 6+6 manifest HEAD 500s;
  /tmp/ci-logs-14049-provision.log: 11 manifest HEAD and 21 blob GET
  500s). Registry, not the trees; the coordinator confirmed the manifest
  HEAD returns 200 from this host afterwards. Rerun once on each.
- golang:test-all failed on #14043 (trace
  `9ea1b714b2687e28e4b3871c58032725`) and #14049 (trace
  `d8b1eb78980a85c61198e0d2fd63c6c1`): e2e/helm
  `TestInstallK3S/default_daemonset`, the engine pod stuck in
  ImagePullBackOff for 5 minutes ("wait for engine pod
  dagger-dagger-helm-engine (condition=Ready)"), same window, the same
  registry serving the engine image; every other e2e test passed.
  Rerun once on each.
- test-split:test-base failed on #14043 (trace
  `f4dadcef70ca15a4fd3c200ccf2aadc7`): 2439 passed, 12 skipped, one
  failure, core/integration `TestRuntimeCodegen/TestPythonTrustedFilesUsed`
  (logs /tmp/ci-logs-14043-pytrusted.log); triage below before any rerun.
  The known debug-listener race did not fire in this run.

#### #14050 approval and push

Reviewer approved `52ee0e172e` (directive removal only; local lint
`golangci-lint:lint-all DONE [3m14s]`, exit 0). #14049's head was still
`46bbd9b9ff`, the candidate's base, so no move. Force-pushed with lease
on the original tip `9375bbb985`:
`sipsma/remote-cache-container-part-persistence` = `52ee0e172e`; GitHub
shows base `sipsma/remote-cache-track8-terminology`, 15 commits. Fresh
check runs watched.

Rerun attempts for the registry-outage failures: `dagger cloud rerun
--commit <sha> --check <name>` answers "no Cloud checks found for the
target commit" for the head SHAs, the PR merge commits, `--pr <n>`, and
from a checkout at the exact head (commands and answers in
/tmp/pkg-ci-reruns.log). The CLI's current Cloud org on this host is the
dagger org (`dagger cloud org info`), so the org is not the cause; the
workspace's `origin` remote is the sipsma fork, which may be how the
command resolves the repository; tried with the upstream repository as
the workspace (`-W`).

Reruns issued (once each, 19:5x UTC), with the working form `dagger
cloud -W github.com/dagger/dagger@<head sha> rerun --check <name>` (the
checkout's `origin` is the sipsma fork, which is why the plain form
found no checks; log /tmp/pkg-ci-reruns2.log): #14043 `4a09602c18`
test-split:test-provision, golang:test-all, test-split:test-base (the
nested-client init deadline in TestRuntimeCodegen/TestPythonTrustedFilesUsed,
`Post "http://dagger/init": context deadline exceeded`, read as load in
the outage window: the test and the python codegen path are untouched
by the stack and the test passed in #13969's run); #14049 `46bbd9b9ff`
test-split:test-provision, golang:test-all; #14219 `577d0a04f5`
test-split:test-provision. Results recorded when they land.

### #14051 `sipsma/remote-cache-snapshot-chains`

Candidate head `d709b706bd` (working branch `pkg/track10`, worktree
/tmp/pkg-track10), 17 commits on #14050's `52ee0e172e`: the 16 originals
(`9375bbb985..62d62bd0c3`), all paired, plus the bindings regeneration
`d709b706bd` (`dagger generate -y go-sdk:generate` on
`remote-cache-engine`, log /tmp/pkg-track10-gen.log, exit 0: 14
source-map lines in .dagger/modules/tla-check/dagger.gen.go, no other
file; the module's main.go gained the two snapshot map entries and the
`modelFiles` helper). Built on the pre-move `86fbb2af93` (44fda394a0 tip),
moved onto `52ee0e172e` as `838abf5a32` (range-diff all equal), then the
regeneration on top.

Conflicts:

1. `308fe514f1` ("snapshots: model immutable chain transfer lifetime"),
   .dagger/modules/tla-check/main.go. The commit adds a second spec,
   `SnapshotChain.tla`, with two configurations (`snapshot_import`,
   `snapshot_export`) run through the same expectation map, and a
   `modelFiles(name)` switch giving the spec file and configuration path
   for a name; it rewrote the old `runOne` to use it. Main had already
   restructured the runner (`runOne(ctx, base, specName, configPrefix,
   name, expect) *runFailure`, `reportFailures`, the ClientLifecycle
   check). Resolution: main's runner kept unchanged; `modelFiles` kept as
   the commit wrote it (the `One` function's hunks using it applied
   cleanly); `runConfigs` derives `specName`, `configPrefix` and the
   configuration's short name from `modelFiles(name)` before calling
   main's `runOne`, and restores the map key as the failure name. The
   two snapshot configurations therefore run as
   `SnapshotChain_import.cfg`/`SnapshotChain_export.cfg` against
   `SnapshotChain.tla` from the same source directory. Module builds and
   vets.
2. `6c727985e2` ("dagql: release abandoned arbitrary values after
   completion"), dagql/cache_arbitrary.go, three hunks in
   `getOrInitArbitrary`. Main had added the client-scope lease
   (`engine.DetachClientScope`, released in a defer before `close(waitCh)`,
   `res.cancel = nil` after the callback, `res.cancel != nil` guard in
   `waitArbitrary`); the commit adds a cache operation token around the
   callback (`beginCacheOperation`/`finish`) so a late release stays
   visible to Close, and publishes completion under `callsMu` (set
   err/value, close `waitCh`, `removeUnownedArbitraryLocked`, then run the
   returned release outside the lock). Merged goroutine: `defer
   callbackOp.finish(false)`; run the callback; release the client-scope
   lease (main's order: before completion is published); then one locked
   section that sets err/value, drops `res.cancel`, closes `waitCh` and
   takes the unowned release; then `runArbitraryOnRelease` with the
   callback context. The `beginCacheOperation` failure path releases the
   lease and cancels, as main's own failure path does. `DetachClientScope`
   returns a `WithoutCancel` context, so releasing the lease does not
   cancel the release callback's context. The commit's `waitArbitrary`
   and `removeUnownedArbitraryLocked` hunks applied cleanly. First
   resolution attempt double-locked `callsMu` (the commit's lock line
   merged cleanly above my hunk); caught by reading the result before
   staging, fixed in the same resolution.
3. The other 14 commits applied cleanly.

Checks on the tip: TLA audit on both specs, CacheLifecycle 0 findings
(30 constants, 10 variables, 43 cfgs) and SnapshotChain 0 findings (4
constants, 10 variables, 2 cfgs); no assignment outside a CONSTANTS
block; module map and files agree (43 cache + 2 snapshot + 7 client);
no trailers; no markers. TLC not run (dev-only; CI does not run
tla-check).

LLM-code rule: no LLM file touched.

Tests on `838abf5a32`, clean tree (/tmp/pkg-track10-tests.head; the regeneration above it changes only the nested module's bindings, so the tested packages are unchanged): `go build
./...` and the tla-check module build ok; `go test -v -count=1 -timeout
60s ./core/ ./dagql/ ./engine/engineutil/ ./engine/engineutil/imageexport/
./engine/server/ ./engine/snapshots/ ./engine/snapshots/testutil/` (the
packages the PR touches outside generated code and the engine suite),
log /tmp/pkg-track10-tests.log, exit 0: six packages ok (core 6.498 s,
dagql 1.994 s, engine/engineutil 0.153 s, imageexport 0.010 s,
engine/server 1.258 s, engine/snapshots 1.016 s), snapshots/testutil has
no test files; 1001 top-level PASS, 0 FAIL, 12 top-level SKIP (the
fixture-gated snapshot tests, `_DAGGER_TEST_REMOTE_CACHE_FIXTURE_ROOT`
unset, per ruling (e)). Engine-suite changes are CI-only.

Hash map (`9375bbb985..62d62bd0c3`, 16 commits, to `52ee0e172e..d709b706bd`, 17):

```
f1c44a28f5 -> e87a80dc6e  docs: define reusable snapshot chain foundations
308fe514f1 -> 5fd9d9fc5a  snapshots: model immutable chain transfer lifetime
c61b5c6bb8 -> f34a55f030  snapshots: check reuse evidence at byte boundaries
43e6181987 -> ac76bb00eb  snapshots: model new export content on existing owners
90fcda4e93 -> 3d00418fc0  snapshots: import immutable chains with owned resource pins
2a54926225 -> 96f15c0784  snapshots: cancel export waiters and verify local transfer ownership
6c727985e2 -> 6fe4db0974  dagql: release abandoned arbitrary values after completion
d13357dfe6 -> c58c765529  Allow canceled imports to leave shared layer waits
2319955ad7 -> 8c9161552c  Prove snapshot reuse from persisted SQLite rows
08f7fcabf6 -> 517692a4f6  Release previous snapshot transfers after restoring durable owners
4fcd77f7e2 -> 00710a31a7  Record snapshot model and physical transfer evidence
082defb040 -> 11e244645c  docs: explain snapshot transfer ownership and validation
7535d7af87 -> 7bbc811bfe  changes: record snapshot chain reuse and ownership fixes
cd0dfe7e2c -> aa70bbefd7  test: probe read-only mounts before snapshot fixtures
5d34f6259e -> b6af951149  test: report early prepared image completion
62d62bd0c3 -> 838abf5a32  snapshots: clarify import and lifetime lint intent
(new) -> d709b706bd  chore: regenerate tla-check module bindings
```

### #14093 `sipsma/remote-cache-deferred-filesystem-restoration`

Candidate head `1f5fc77117` (working branch `pkg/track11`, worktree
/tmp/pkg-track11), 9 commits on #14051's `d709b706bd`: the 9 originals
(`62d62bd0c3..17f7dd89f4`), all paired, no new commits. No conflicts:
every commit applied cleanly. No TLA, tla-check or generated file
changes, so no regeneration. No trailers, no markers.

LLM-code rule: no LLM file touched.

Tests on `b35e57d3c1` (the same nine commits on `838abf5a32`, before #14051's regeneration commit was inserted below them; `git range-diff` all equal, and the regeneration touches only the nested module's bindings, so the tested packages are unchanged), clean tree (/tmp/pkg-track11-tests.head): `go build
./...` ok; `go test -v -count=1 -timeout 60s ./core/ ./core/schema/
./dagql/` (the packages the PR touches outside the engine suite), log
/tmp/pkg-track11-tests.log, exit 0: ok core 6.958 s, ok core/schema
8.950 s, ok dagql 2.092 s; 973 top-level PASS, 0 FAIL, 1 top-level SKIP (`TestSnapshotTransferTypedAdoptionAndRestart`, fixture-gated, fixture root unset) and no nested skips. The
engine-suite changes (core/integration/engine_persistence_test.go and
the persisted-directory-list testdata module) are CI-only.

Hash map (`62d62bd0c3..17f7dd89f4`, 9 commits, to `d709b706bd..1f5fc77117`, 9):

```
09c7e3ca03 -> fcc31e1271  dagql: report stored whole-result opens as completed computation
b10f8034c1 -> f76bd2acc4  dagql: restore persisted list children through their exact rows
a0cef8d540 -> 5a932e6818  core: defer opening persisted directory and file snapshots
90d9134a27 -> 0c45aab7f2  core: observe saved snapshot opens across engine restarts
ebb12475db -> f078e1e776  core: assert saved list row decoding after restart
50f75609b4 -> bd178fa2f1  docs: explain deferred snapshot restoration and list identity
5f09dc400e -> ace9d09fb9  docs: clarify test revisions and fixture limits
a471f99518 -> 13a217121d  changes: record deferred snapshot restoration
17f7dd89f4 -> 1f5fc77117  docs: satisfy Markdown formatting rules
```
