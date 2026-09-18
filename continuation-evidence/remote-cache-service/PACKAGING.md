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
physical snapshot tests, skipped by the denied bind-mount probe in
testutil.requireNativeMount; see the skip accounting below). Engine-suite changes are CI-only.

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
8.950 s, ok dagql 2.092 s; 973 top-level PASS, 0 FAIL, two unexecuted tests: one top-level SKIP, `TestSnapshotTransferTypedAdoptionAndRestart` (read-only bind mount denied, "operation not permitted", core/snapshot_transfer_test.go:45), and one inherited nested SKIP, `TestCacheContextCancel/last_waiter_canceled_fn_returns_value_still_releases` (TODO skip, cache_test.go:2289). The
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

## Step 2 preparation: A0 probe

Throwaway worktree /tmp/pkg-a0-probe on #14093's original tip
`17f7dd89f4`, cherry-picking batch 7's packaged test-store commits (from
`manifest/batch-7.json`, branch `b7-packaging/remote-cache/b7-verification`):

- `2e43236077` (original `e4b65210ea`) "engine/snapshots/testutil: run
  the real test store without privileges", inplace.go (+253) and
  store.go: applies cleanly, `go build` of engine/snapshots and core ok.
  A0 proper.
- `0513487968` (original `b073363f07`) "core/schema: read demanded bytes
  in the test store's directory": adds demanded_read_test.go and edits
  directory_scratch_test.go, http_lazy_test.go, query_lazy_test.go, which
  do not exist on the A-series tip (delete/modify conflicts). Two
  placements offered to the coordinator: A0 with only the new test file
  (a split), or whole in the first packaged PR carrying those lazy tests.
- `46ee7035b8` (original `ed7a4a47f9`, the helper fix): store.go hunk
  swaps `ContentStore: s.Content` for the `observed` wrapper; that
  wrapper (`BeforeWrite`) first appears in `4e2515e704` on b4-acquisition
  (A3); the import_test.go hunk adds a test beside later-batch tests. By
  Erik's backport rule it belongs in A3 above `4e2515e704`; A0 (the
  commission's wording) has no observed wrapper. Placement asked, not
  guessed.

Coordinator's placements (A0 is the unprivileged test store itself, so
every store test from A0 upward executes in CI; a fix lives with what it
fixes, never in A0 with hand-invented context):

- (a) `2e43236077` is A0, alone: one commit on #14093's current head
  (inplace.go, store.go; applies cleanly, builds).
- (b) `0513487968` whole, unsplit, into the first A-batch PR that carries
  the three lazy test files it edits (A1 or A2, whichever introduces
  directory_scratch_test.go, http_lazy_test.go, query_lazy_test.go),
  directly above the commit that introduces them; the demanded-read test
  is coverage of those lazy paths and is worth nothing in A0 without
  them.
- (c) `46ee7035b8` (helper fix, original `ed7a4a47f9`) into A3, directly
  above `4e2515e704`, which introduces the observed wrapper it fixes; the
  commission's A0 wording predates knowing where the wrapper lands, and
  "the fix goes where the defect was introduced" decides it.

Corrections table additions: `ed7a4a47f9` → A3 above `4e2515e704`
(was: A0); `b073363f07`/`0513487968` → A1 or A2 above the lazy tests'
introducing commit (was: A0 wiring).

#14219 golang:test-all fail (trace `a910d36ea38fd05c73a0917e36209664`):
e2e/helm TestInstallK3S/default_daemonset, engine pod ImagePullBackOff
for five minutes, the registry outage window again (same as #14043 and
#14049's golang:test-all). Rerun once. #14219 now has a human approval
on GitHub (vito); merge when green.

#### #14051 placement note (reviewer's finding, coordinator's ruling)

`6c727985e2` → `6fe4db0974` ("dagql: release abandoned arbitrary values
after completion") fixes a defect present at the pre-stack base
`0d031c08ef` (dagql/cache_arbitrary.go drops an entry when its final
waiter cancels; an OnReleaser returned afterwards has no owner and no
late cleanup; Close has no independent callback token; the file is
untouched between `0d031c08ef` and `9375bbb985`). Kept as Erik's
original placement: step 1 rebases Erik's PRs with contents preserved,
and the main-defect rule governs step 4's backports, not Erik's own
commits. Erik may have it split into a main PR if he wants it merged
independently; unless he says so, nothing changes. The fixture-gated
skips in #14051's run (27 SKIP lines, 12 top-level) are unexecuted
tests, not physical-store evidence.

#### #14051 approval, skip accounting, push

Reviewer approved `d709b706bd`. Skip accounting for its run
(/tmp/pkg-track10-tests.log), to be repeated in the PR description: 27
SKIP lines, 12 top-level and 15 nested; 26 are physical-fixture cases,
all skipped by the same denied read-only bind-mount probe in
testutil.requireNativeMount (not by the fixture-root variable being
unset; corrected per review), and one is the inherited last-waiter skip;
all unexecuted. Six PASS parents contain only skipped
fixture children and so prove no physical behavior. This run supplies
no physical-store proof; that proof stays with an in-place-store run (A0
and above) or the top-PR CI job, per the coordinator's ruling.
Force-pushed with lease on the original tip `62d62bd0c3`:
`sipsma/remote-cache-snapshot-chains` = `d709b706bd`; GitHub shows base
`sipsma/remote-cache-container-part-persistence`, 17 commits. Fresh check
runs watched.


### Merge of #14219 (main PR)

Merged #14219 at main `ba9d016800` by the Stack integrator under the
merge rule: 85 pass, 1 skipping, 0 pending, 0 failed (the registry-window
failures cleared on their single reruns), human approval by vito, head
`577d0a04f5` unchanged; plain `gh pr merge --merge` (not a stack member).
The known-failure entry for TestAgentDebugServerContextCancellation is
therefore "fixed on main at `ba9d016800`"; PRs rebased onto a main that
contains it no longer carry the known failure.

#### #14093 approval and push

Reviewer approved `1f5fc77117` (record corrections above applied).
#14051's head was still `d709b706bd`, the candidate's base. Force-pushed
with lease on the original tip `17f7dd89f4`:
`sipsma/remote-cache-deferred-filesystem-restoration` = `1f5fc77117`;
GitHub shows base `sipsma/remote-cache-snapshot-chains`, 9 commits. Step
1 (the seven-PR rebase) is complete; fresh check runs watched.

A0 probe, second round (worktree /tmp/pkg-a0, `a0dd492db0` =
`2e43236077` on the pushed `1f5fc77117`):

- engine/snapshots before A0 (from #14051's run): 10 PASS, 9 SKIP (the
  chain import/export tests, skipped by `requireNativeMount`'s denied
  bind mount); with A0: 19 PASS, 0 SKIP, 0 FAIL (`go test -v -count=1
  -timeout 60s ./engine/snapshots/ ./engine/snapshots/testutil/`, log
  /tmp/pkg-a0-tests.log, exit 0).
- core before A0 (from #14093's run): `TestSnapshotTransferTypedAdoptionAndRestart`
  skipped by the same probe at snapshot_transfer_test.go:45; with A0 the
  store no longer probes, and the test FAILS at line 67:
  `Directory.Entries` reads through core.MountRef, whose read-only bind
  mount is denied ("operation not permitted"). Log
  /tmp/pkg-a0-core-tests.log: 471 PASS, 1 FAIL, 0 SKIP.
- Batch 7's core companion `bbbe792279` "core: run the real-store tests
  without privileges" does not apply on A0: seven of its eight files do
  not exist on the A-series tip (b2: value_transfer_chain_test.go,
  value_transfer_restart_test.go; b4: git_lazy_test.go, http_lazy_test.go,
  lazy_operation_execution_test.go, part_acquisition_test.go; b5:
  part_offer_admission_test.go), and its snapshot_transfer_test.go hunk
  (four call sites moved to `demandedDirectoryEntries`/`demandedFileContents`)
  needs helpers the same commit adds to lazy_operation_execution_test.go
  (b4), so core does not compile with that hunk alone
  (/tmp/pkg-a0-core-tests3.log, build failed). Probe reverted; options
  sent to the coordinator, recommendation: A0 = `2e43236077` plus the
  helpers and four call-site changes at A0, `bbbe792279`'s other hunks
  placed with their files (split by file).

## Step 2: A0, the unprivileged test store (`sipsma/remote-cache-test-store`)

Candidate `13830a571b` on `pkg/a0` (worktree /tmp/pkg-a0), two commits on the pushed #14093 head `1f5fc77117`. First, `a0dd492db0`: packaged `2e43236077` (original `e4b65210ea`, batch 7,
"engine/snapshots/testutil: run the real test store without
privileges": inplace.go +253, store.go), cherry-picked cleanly. Second, `13830a571b` "core: read demanded snapshot bytes in place in the transfer test": the A0-context part of batch 7's `bbbe792279` (original `397168d119`, "core: run the real-store tests without privileges"), by the coordinator's option (1): the `demandedFileContents`/`demandedDirectoryEntries` helpers in a new core/demanded_read_test.go (Erik's code and comments, unchanged) and the four call sites in core/snapshot_transfer_test.go that move from `Directory.Entries`/`File.Contents` to them (the commit's own hunk, applied as a patch). `bbbe792279`'s other seven files do not exist on the A-series tip; their hunks go with the files (see the corrections table below). Erik's authorship and
signoff, no trailer. `go build ./...` ok.

Per-package before/after (before = the #14051 and #14093 runs on the
A-series tip; the skips there are `requireNativeMount`'s denied
read-only bind mount, "operation not permitted"):

- engine/snapshots: before 10 PASS, 9 SKIP (TestImportChainLocalStores,
  TestImportChainConcurrentPrefix, TestExportChainConcurrentCancellation,
  TestImportChainCandidateLostBeforePin, TestExportChainExistingBlobPins,
  TestImportChainSnapshotWithLostHistoricalBlob,
  TestImportImageSharesChainReuse, TestExportChainCanceledWaiter,
  TestImportChainCanceledWaiter). With A0: 19 PASS, 0 FAIL, 0 SKIP; all
  nine print PASS. `go test -v -count=1 -timeout 60s ./engine/snapshots/
  ./engine/snapshots/testutil/` on `13830a571b`, clean tree
  (/tmp/pkg-a0-final-tests.head), log /tmp/pkg-a0-snapshots-final.log,
  exit 0, ok engine/snapshots 8.833 s, testutil no test files.
- core: before 1 SKIP, `TestSnapshotTransferTypedAdoptionAndRestart`
  (plus the inherited nested last-waiter skip). With the store commit
  alone it FAILS (bind mount in Directory.Entries, above). With both A0
  commits: `go test -v -count=1 -timeout 60s ./core/` on `13830a571b`,
  log /tmp/pkg-a0-core-final.log, exit 0, ok core 10.192 s; 472
  top-level PASS, 0 FAIL, 0 top-level SKIP, 0 nested SKIP;
  `--- PASS: TestSnapshotTransferTypedAdoptionAndRestart (0.35s)`.
  (The inherited nested last-waiter skip is in the dagql package, not
  core; unchanged.)
- Still skipping with A0: nothing in these two packages. Physical-store
  proof for the stack's fixture-backed tests now comes from these runs
  on an unprivileged host; the remaining privilege-dependent tests
  (mount-based paths in core.MountRef users) are those the later batches
  adapt with `bbbe792279`'s other hunks.

Corrections table, split of `bbbe792279` (original `397168d119`) by
file, per the coordinator: core/snapshot_transfer_test.go and the two
helpers → A0 (`13830a571b`); core/value_transfer_chain_test.go and
core/value_transfer_restart_test.go → A2 (b2-transfer introduces them);
core/git_lazy_test.go, core/http_lazy_test.go,
core/part_acquisition_test.go → A3 (b4-acquisition); the
lazy_operation_execution_test.go hunk → A3 minus the part that adds the
two helpers (A0 has them; no move, no duplicate);
core/part_offer_admission_test.go → A4 (b5-offers).

Branch name for the PR: `sipsma/remote-cache-test-store`, on
`sipsma/remote-cache-deferred-filesystem-restoration`. Sent to the
reviewer; not pushed.

#14051 `d709b706bd` test-split:test-provision fail (trace
`b3b49c097548a774625634b6b1d9479d`, logs /tmp/ci-logs-14051-provision.log):
registry.dagger.io 500s again (109 lines: blob GETs and the v0.16.1
manifest HEAD), TestProvision's image-driver subtests; registry, not the
tree. Rerun once (`-W` form). #14043: all 85 checks pass after its
reruns (test-base included: the codegen nested-init deadline did not
recur), human approval (grouville), head `4a09602c18`; asynchronous
merge enqueued (uuid `17be4ea4-b670-4e48-96ae-a36a889743d4`).

### Merge of #14043

Merged #14043 at main `1daceb3a34` (19:55:59 UTC) under the merge rule:
85 pass, 1 skipping, 0 pending, 0 failed (after the single reruns of
test-provision, golang:test-all and test-base); human approval by
grouville; head `4a09602c18` equal to the reviewed candidate;
asynchronous merge endpoint, sha pinned (uuid
`17be4ea4-b670-4e48-96ae-a36a889743d4`, result "merged"). Automatic
rebase of the PRs above, trees identical (range-diff all equal in every
case): #14049 → `b910097a74` (8 commits, base now `main`), #14050 →
`00eaf56c66` (15), #14051 → `31f7c8dc10` (17), #14093 → `21f73b7a33`
(9). Fresh check runs watched. Fixes redirected to main because #14043
is merged: E15 (`3587242fe7`, per-part evaluation) was to go into
#14043 in step 4 and now becomes a small PR against main instead. The
next merge candidate is #14049, which has no human approval on GitHub
yet (REVIEW_REQUIRED).

#### A0 published: #14220

Reviewer approved `13830a571b`; moved onto #14093's post-merge head
`21f73b7a33` as `e4e18ce47d` (`git range-diff 1f5fc77117..13830a571b
21f73b7a33..e4e18ce47d` all equal: an equal A0 patch series and unchanged
tested packages, not identical full trees; the base moved by main's
`577d0a04f5`, nine lines in internal/cmd/dagger/shell.go, the separately
merged debug-listener fix; commits now `f69225af99` store, `e4e18ce47d`
core). Pushed as `sipsma/remote-cache-test-store` (new
branch, no prior remote branch or PR). PR
https://github.com/dagger/dagger/pull/14220, base
`sipsma/remote-cache-deferred-filesystem-restoration`, head
dagger/dagger:sipsma/remote-cache-test-store @ `e4e18ce47d`, ready for
review, not draft, title "engine/snapshots/testutil: run the real-store
tests without mount privileges", body per the coordinator's list (what
A0 is, the two commits and originals with the per-file destinations of
the rest, the measured before/after, the reviewer's scope statement, the
two CI gaps). Stack registration was done after Erik noticed the PR as
a cross-branch PR (a gap of a few minutes between `gh pr create` and
`gh stack link`; next PRs link immediately after creation): `gh stack
link 13937 14220` → "Added 1 PR to stack #13937"; server-side stack
13937 (id 487371, base main) now has 12 members, #14220 at position 12
above #14093 (verbatim in /tmp/pkg-14220-stack.txt). CI watched.

### CI round after the #14043 merge (heads moved, fresh runs)

- registry.dagger.io still 500: test-provision failed on #14049 (trace
  `8e61269776353f94bf925813e45b2271`), #14050 (`2400b3b351373ff2a8b4ce19900c9388`),
  #14051 (`c19c2f0dff7fc4554a4f0d224ec5b533`), #14093
  (`494fab3c2ccee78f5643e3208c210970`) and #14220
  (`2e9f0b0a18bf3c2c8bf771f1ef730ed4`); 17–24 manifest-HEAD 500s each
  (/tmp/ci-logs-<pr>-provision*.log). Reruns once for #14049, #14050,
  #14051, #14093; #14049's came back green; the rest await the registry.
  #14093 test-container (trace `a5b298d7dcced06eed8d125b9f5e371b`):
  TestContainer/TestSystemCACerts/wolfi_basic, cgr.dev 500 listing
  wolfi-base tags; rerun once.
- #14051 golang:test-all (trace `cdf34bade8d6557ca6cf1ec9bb0b440c`):
  e2e/installers TestBashScript/install_DAGGER_COMMIT_head, the installer
  downloading dl.dagger.io/dagger/main/head/dagger_head_linux_amd64.tar.gz
  exits 1 while the sibling release downloads pass; rerun once.
- #14051 test-local-cache (trace `6caa4ce66f0efb71c9f6aebbd86bd9aa`):
  TestLocalCache/TestDagqlMetadataGCProtectsActiveZeroDiskResults, "timed
  out waiting for metadata workload session to close" after 72 s. NOT
  rerun: #14051's dagql change is the arbitrary-value cache commit whose
  conflict was merged with main's client-scope lease, and a session-close
  timeout is what a held lease would look like; whether the test passed
  on the pre-rebase identical tree could not be confirmed. Reported to the
  coordinator for a ruling before any rerun.
- #14220 golangci-lint:lint-all (trace `066a10f8917f237bb93c8bd20f69c9a9`):
  gocyclo 33 on `(inPlaceDiffer).Compare` (inplace.go, the packaged
  batch-7 code). Follow-up `f97c446e23` on `pkg/a0`: the media-type and
  compressor selection moves verbatim to `diffCompression`; Compare 28;
  no nolint. engine/snapshots tests once on `f97c446e23`, clean
  (/tmp/pkg-a0-fix-tests.head), log /tmp/pkg-a0-fix-tests.log, exit 0, 19
  PASS, 0 FAIL, 0 SKIP. Local lint: LINTRESULT2

golang:test-all red on #14049 (trace `af58d68fd62ed1723170893d535d450f`),
#14050, #14093 and #14220 (`d5e2af53e69ea4d6142a1c626a7bf2a4`) in the
same window: e2e/helm again, the engine pod in ImagePullBackOff
(TestInstallK3S/default_daemonset on #14049; TestCustomProbes on
#14220), the registry serving the engine image. Coordinator's policy:
registry.dagger.io is intermittent (200 three times from this host at
20:11 UTC); one rerun per red PR only when its last failure is older
than an hour; cgr.dev's 401 from this host is the normal auth challenge.
No reruns issued for these yet.

## Step 3: A1 (`sipsma/remote-cache-transfer-foundations`... name per branch rule), in progress

Source: packaged `transfer-foundations` (10 commits) + `b1-producers`
(10), `a7d4bad229..42a57419de` (a7d4bad229 is the predecessor's copy of
the original #14093 tip on the old base; the packaged branches contain
no stack commits, merge-base `0d031c08ef`). Rebased onto A0's pushed
`e4e18ce47d` in /tmp/pkg-a1 (`pkg/a1`). Twenty originals all paired
(/tmp/pkg-map-a1.txt); adaptations so far, each an Erik-authored,
signed-off commit placed above the commit it reconciles:

- Conflicts: core/schema/git.go twice (`d54f0748ce`, `39136200f6`):
  main restructured `tree` into a `__fullCheckout` Select for
  default-argument trees and a `ref.Tree` branch otherwise; the release
  defer and the producer recording were placed inside the branch that
  builds the tree; `fullCheckout` untouched (Erik: faithful history, no
  extension; A3 replaces producer recording with lazy outputs).
- Test signatures (folded into `785bc6c524`/`d54f0748ce`'s successors):
  `repo.EncodePersistedObject(ctx, dagql.NewPersistEncodeContext(cache,
  0, nil))`, `DecodePersistedObject(ctx, dagql.NewPersistDecodeContext(srv,
  0, nil), …)`, and the fake `GitRefBackend.Tree` gains main's
  `[]core.GitRemote` parameter.
- "core: run the eager producer tests without privileges": the
  A1-context part of `397168d119` (`bbbe792279`), seven hunks by hand on
  core/eager_producer_execution_test.go, which b4 renames to
  lazy_operation_execution_test.go (`dfe2076ee4`, 75%); helpers not
  duplicated (A0 has them). Split-table correction: that file's hunk is
  A1's, not A3's; A3 carries git_lazy_test.go (see next), http_lazy_test.go,
  part_acquisition_test.go only.
- "core: read git producer directories in place in their test": the
  git_lazy_test.go hunk of the same original, on A1's
  git_completed_producers_test.go (renamed in b4, 57%). Split-table
  correction: A3 carries http_lazy_test.go and part_acquisition_test.go.
- "core/schema: list main's two new objects among the persisted-family
  exceptions": GitPushResult, WorkspaceCommitPick.
- "core/schema: give the workspace checkout stub's directories their
  accessors": Dir "/" and a test snapshot; the alternative (tolerating
  accessor-less directories in RecordCompletedProducer) rejected as a
  contract change for a stub.
- "core/schema: exercise the tree-building branch in the producer
  resolver tests": depth 1 on the two `tree` calls (Erik's (3) decision).

State: 25 commits; `go build ./...` ok; core/schema `go test -v -count=1
-timeout 60s`: ok, 148 PASS, 0 FAIL (/tmp/pkg-a1-schema-tests4.log). core:
six of the ten formerly privilege-blocked tests print PASS; four remain
red for privileges batch 7 never removed from these tests (b4 rewrote or
deleted them): TestGitCompletedProducersEvaluate,
TestGitBundleCompletedProducerEvaluate (mutable ref built and bundles
checked through core.MountRef), TestGitCompletedProducersRemoteEvaluate
(remote fetch `setns` into the clean mount namespace the fixture no
longer creates), TestAuditedEagerProducersEvaluate (bind mount inside
the production compute-paths path). Proposal to the coordinator: a
privilege probe skip at the top of each; awaiting the ruling.

Andon (A1, four privileged producer-era tests): CI's green test-base for
#14093 (trace `22d7a6026715a5fd3d63a7d7d605eeb1`) skips
TestSnapshotTransferTypedAdoptionAndRestart with the same denied
bind-mount reason as this host, together with the nine engine/snapshots
chain tests and two engine/engineutil prepared-image tests
(/tmp/ci-trace-14093-base.log lines 7–26): CI's runner denies bind
mounts too, so a probe-and-skip on the four A1 tests would run them
nowhere. Stopped per the coordinator's rule; nothing added. Batch 7
resolved the four by b4's rewrite and the later deletion of the four
git tests (`76311056c7`), never by making them unprivileged. Side
finding: A0 (#14220) is the first time the store tests execute in CI.

### #14222 `sipsma/localcache-workload-close-wait` (main PR, under the same watch)

The coordinator's local-cache wait-bound fix (`7e3c431108` on main,
+5/-1 in core/integration/localcache_test.go), reviewer-approved with
one dev-engine run of the test passed (trace
`8b5b41b77a0cd55f1aefb997a58567e5`). Same stewardship and merge rule as
#14219; not a stack member. #14220's description now carries the side
finding (the store tests executed nowhere before A0; CI's test-base
runner denies bind mounts like this host).

#14050 test-split:test-base fail (trace `0ac823885591b7451d798612f1e2a55b`,
started 20:28 UTC as the run's last check): ran 29m43s, ERROR with no
failing test: logs carry every package's `ok` line except
core/integration, whose summary never appears, no `--- FAIL`, no panic
(/tmp/ci-logs-14050-test-base.log); the checks job has
`timeout-minutes: 30` and #14093's same check took 23m49s. Job-timeout
cut on a slow runner in the registry window; infrastructure; rerun
under the one-hour rule with the others (earliest 21:10 UTC).


### A1 candidate `90f6cd5039` (`pkg/a1`, worktree /tmp/pkg-a1)

25 commits on A0's `e4e18ce47d` (A0's lint follow-up `f97c446e23` came
after; A1 moves onto it before push).
The 20 originals (`a7d4bad229..42a57419de`) all paired (map below) plus
five adaptation commits, all Erik-authored and signed off, no trailers,
no evidence files, no TLA or generated-file changes:

- `577900a239` "core: run the eager producer tests without privileges"
  (above the fixture file's introducing commit): the A1-context part of
  `397168d119`, seven hunks by hand, helpers not duplicated.
- `a8e7d01a6b` "core/schema: list main's two new objects among the
  persisted-family exceptions" (after the last commit editing the list).
- `d25ee382cb` "core/schema: give the workspace checkout stub's
  directories their accessors" (above the tree-recording commit).
- `013addb8d0` "core/schema: exercise the tree-building branch in the
  producer resolver tests" (Erik's (3): depth 1; `fullCheckout` not
  producer-recorded, A3 replaces producer recording with lazy outputs).
- `90f6cd5039` "core: drop the four producer-era tests that no
  environment can run" (tip; Erik's option 1): core/git_completed_producers_test.go
  and core/audited_eager_producers_test.go removed whole (every helper in
  them was used only by the four tests; no other file references them);
  the earlier in-lineage read-helper adaptation for that file was
  dropped from the series as moot.

Hand-adapted conflict resolutions in core/schema/git.go (two commits)
as recorded above; test-signature folds as recorded above.

Corrections table: `397168d119`/`bbbe792279` split by file: A0 (helpers +
snapshot_transfer_test.go, done in #14220), A1 (eager_producer_execution_test.go,
done here; the git_lazy_test.go hunk is void since A1 drops that file),
A2 (value_transfer_chain_test.go, value_transfer_restart_test.go), A3
(http_lazy_test.go, part_acquisition_test.go; the
lazy_operation_execution_test.go hunk is A1's, and its helper-adding part
is A0's), A4 (part_offer_admission_test.go). Four producer-era tests
dropped from A1 per Erik (need mount privileges no environment grants;
never made unprivileged by batch 7; superseded by A3's native tree tests,
`dfe2076ee4`, `76311056c7`). The A1 and A2 PR descriptions open with the
sentence that A3 supersedes producer recording with the lazy-outputs
design, and A1's adds that the four tests were dropped for that reason.
Standing rule from Erik: the next time producers cost anything, stop;
the answer then is collapse.

Tests on `90f6cd5039`, clean tree (/tmp/pkg-a1-tests5.head): `go build
./...` ok; `go test -v -count=1 -timeout 60s ./core/ ./core/modules/
./core/schema/ ./dagql/ ./dagql/call/` (the packages A1 touches outside
the engine suite), log /tmp/pkg-a1-tests5.log, exit 0: all five ok (core
11.172 s, core/modules 0.023 s, core/schema 17.342 s, dagql 2.392 s,
dagql/call 0.004 s); 1070 top-level PASS, 0 FAIL, 0 top-level SKIP, one
inherited nested SKIP (`TestCacheContextCancel/last_waiter_canceled_fn_returns_value_still_releases`).
Earlier runs on the way (for provenance): /tmp/pkg-a1-tests.log (14 FAIL,
before adaptation), /tmp/pkg-a1-tests2.log (8), /tmp/pkg-a1-tests3.log (6),
/tmp/pkg-a1-schema-tests4.log (core/schema green). The engine-suite
change (one core/integration file and the persisted-core-returns
testdata module) is CI-only. LLM rule (reviewer's correction): A1 does
touch core/llm.go, at lines 288/297 and 3150/3159 of `90f6cd5039`, where
the LLMTokenUsage and LLMVariable persistence methods adopt main's
PersistEncodeContext/PersistDecodeContext signatures; these are the
source commit's required API adaptations with bodies otherwise
unchanged, and nothing main removed is reintroduced. Local lint:
LINTA1

Hash map (`a7d4bad229..42a57419de`, 20 commits, to `e4e18ce47d..90f6cd5039`, 25):

```
397e02758e -> 32098ffb40  dagql: preserve typed values and references across cache saves
7463ac2ec9 -> 40514e6b1b  docs: restrict remote-cache work to the requested foundations
55d420a9d7 -> 7d9f95116c  core: record ordinary producers for merged changeset directories
d58f806664 -> d07c7b658e  core: preserve supplied exec metadata when persisting inputs
dfed2f750e -> e9310b73b8  core: retain completed container producer inputs
17c8d52177 -> e0058a5f5d  core: retain completed file and directory producers
d7223c6c8d -> a3dc348036  cache: label extra digests for remote transfer
e3a0437432 -> 4e1a644795  cache: capture live persisted records without evaluation
fa388f1962 -> 81a0c4c3d6  fix(cache): exclude attachment and direct evaluation from capture
93ee3e933b -> 2e15c686bd  fix: preserve quiescent container persistence during reader contention
d54f0748ce -> df5e9285d0  fix: release eager output resources on failed construction
5e01d645c3 -> f0be6172f8  core: record completed filesystem producers before publication
38013d0d20 -> 13d97565d9  core: retain producers for cleaned and imported Git directories
39136200f6 -> da72febda6  core: retain exact Git ref and commit tree producers
0dcd81d970 -> 4fed892ea5  core: retain stateless HTTP File producers
f6d063060f -> d5d5805675  core: retain builtin Container and generated schema producers
e60bf15c79 -> 2af675b970  test: verify eager producer persistence and execution
967a17eafa -> 37ebf13094  test: cover advanced bundle hints on decoded repositories
ca869972d8 -> 39281a4b35  test: cover recording rejection at every eager producer site
42a57419de -> 9f65ee1c8b  docs: state completed recipe publication ownership
(new) -> a8e7d01a6b  core/schema: list main's two new objects among the persisted-family exceptions
(new) -> d25ee382cb  core/schema: give the workspace checkout stub's directories their accessors
(new) -> 013addb8d0  core/schema: exercise the tree-building branch in the producer resolver tests
(new) -> 577900a239  core: run the eager producer tests without privileges
(new) -> 90f6cd5039  core: drop the four producer-era tests that no environment can run
```

A1 local lint on `90f6cd5039` (/tmp/pkg-a1-lint.log): ERROR, 29
findings: 28 in A1's packaged code (gocyclo 36 `TestRecordCompletedProducer`,
41 `decodePersistedResultEnvelope` plus its sibling's now-unused nolint
directive; staticcheck S1016 ×6, ST1016 ×6; unused ×2; unparam ×1;
dogsled ×4; ineffassign ×2; whitespace ×4) and A0's Compare (fixed in
`f97c446e23`). The batch-7 branch was never run through lint-all under
main's configuration. Stopped under Erik's standing rule (several sit in
producer code); options sent to the coordinator.

#14049 (approved by vito, merge-blocking): the coordinator issued its
three reruns at 20:36 UTC (test-base "Dagger Cloud Engine capacity did
not become available within 15 minutes", trace
`d5e54bee206cab45c982d058b356af59`; provision registry 500s; golang:test-all
k3s image pull). Provision failed again on the registry (trace
`426669edcb1f39a035a630af135d3a99`, 35 × 500); registry probe 200 at
20:5x, rerun once more under the approved-PR tweak (rerun as soon as the
cause is confirmed cleared).

A1 lint adaptation (coordinator's option (a)): `943ca7e681` "lint: meet
main's golangci-lint configuration" at the tip (26 commits), 14 files,
+315/-285, all 28 findings, no nolint added, behavior unchanged; the two
gocyclo functions split by verbatim moves (three named subtest functions;
three envelope-kind decoders with the caller applying the session
resource handle); the unused directive on encodePersistedResultEnvelope
removed (gocyclo 29). Tests on `943ca7e681`, clean
(/tmp/pkg-a1-tests6.head): the same five packages once, log
/tmp/pkg-a1-tests6.log, exit 0, 1070 top-level PASS, 0 FAIL, 0 top-level
SKIP, one inherited nested SKIP. Second local lint on `943ca7e681` (/tmp/pkg-a1-lint2.log): ERROR, exit
1, SEVEN findings (my first report said three; the dupl form
`file.go:NNN` without a column escaped my pattern, corrected in
/tmp/pkg-lint-findings.sh, which now prints the terminal line and every
finding): A0's Compare (gone on `f97c446e23`); unparam on my two
extraction signatures (fixed in `34cf231b26`); dupl core/directory.go:106-140
vs core/file.go:97-129 (both directions) and core/git.go:1507-1528 vs
:1596-1617 (both directions), pending the final-tip lint. Standing
step from here: local lint-all on every candidate before review; findings
in code main owns and the batch does not touch are recorded, not fixed
in the stack (A2's vet shows main's own `session_attachables.go:211`
lostcancel, `74c2889afc`, identical on main).


### A2 candidate (`pkg/a2`, worktree /tmp/pkg-a2), in progress

Source: packaged `b2-transfer`, `42a57419de..d46b43fc0a` (19 commits),
rebased onto A1's `90f6cd5039` (no conflicts; moves onto A1's final tip
before review). 21 commits: the 19 originals all paired (map below) plus
two adaptations, Erik-authored and signed off, no trailers, no evidence,
TLA, generated or LLM files:

- Folded into `fc866aedae`'s successor ("test: verify selected chains
  from both Git backends"): main's `[]GitRemote` argument on the two
  `LocalGitRef`/`RemoteGitRef.Tree` calls (nil).
- `867f7ec5b3` "core: read the restored file's bytes in place in the
  final-offer restart test" (above the file's introducing commit): the
  value_transfer_restart_test.go hunk of `397168d119`; its
  value_transfer_chain_test.go hunk does not apply (that version has no
  Contents call and fails earlier).
- `4a541e4e93` "core: drop the two value transfer tests that no
  environment can run" (coordinator: privilege class, not a producer
  cost): TestValueTransferPartsGitTrees (LocalGitRef.Tree's checkout
  bind-mounts; absent from batch 7) with its transferGitServer, and
  TestValueTransferPartsSelectedChain (subdirectory evaluation mounts in
  this batch's code; survives in batch 7 only through A3's lazy-outputs
  code) with mustTransferPath; transferObservedSnapshots stays (the
  container mount test uses it); seven imports dropped.

Corrections table: `397168d119` split, A2 part done (restart hunk; chain
hunk void at A2). Two tests dropped from A2 for the privilege reason;
A2's description says so next to the "A3 supersedes producer recording"
sentence. Main's own `engine/server/session_attachables.go:211`
lostcancel (`74c2889afc`, identical on main) is recorded, not fixed.

Tests on `4a541e4e93`, clean (/tmp/pkg-a2-tests2.head): `go build ./...`
ok; `go test -v -count=1 -timeout 60s ./core/ ./core/schema/ ./dagql/
./dagql/call/ ./engine/server/`, log /tmp/pkg-a2-tests2.log, exit 0, all
five ok; 1235 top-level PASS, 0 FAIL, 0 top-level SKIP, one inherited
nested SKIP. Earlier: /tmp/pkg-a2-tests.log (3 FAIL, before the drop and
the restart hunk). Local lint: after the move onto A1's final tip.

Hash map (`42a57419de..d46b43fc0a`, 19 commits, to `90f6cd5039..4a541e4e93`, 21):

```
8e1be7d57d -> 80383007b1  dagql: retain transfer offers through independent owners
cb58fe4c3b -> d88648381d  core: encode foreign values as validated pending shells
9802fbe86b -> 7b09c39493  core: guard filesystem persistence and version output publication
eadfe80a28 -> ff7f732c95  dagql: transfer held value graphs with atomic import publication
57cb706470 -> a76bae3f77  core: recover schemas with installed modules and guard foreign host paths
afd57f2f4c -> 6dd12c07ce  test: add gated transfer fixture and preserve cold runtime blocker
786a687f9d -> a4dce3228a  test: verify selected chains from both Git backends
15a1b1ab16 -> b34e6c3c92  test: retire redundant snapshot offers after local restart
2b8ab74d16 -> 951a2f4757  test: accept standalone schema recovery with prepared runtimes
969dbc214d -> 37ba0b8fb8  dagql: return not-ready when pending offer copies fail
55d0464c64 -> 6269ac723b  dagql: verify inaccessible installed schema candidates fall through
a3305edabf -> 7dad4df9de  core: preserve pending Container parts in derived values
f33cd37259 -> b941a0c520  docs: leave value transfer design authority on the designer branch
cef4324818 -> 6c342040f8  test: distinguish transfer root pruning from persistence resets
5c95faa982 -> 6cb615a169  test: skip GC diagnostic outside its allocation bound
6aabb5f879 -> e53e71e216  test: require opt-in for default GC pressure diagnostic
769f80181f -> 95382e5ac3  test: snapshot completed producers without copying mutexes
7316dfcc9a -> 77e4d73f24  core: attach the completed Container recipe's inputs at publication
d46b43fc0a -> 945fa415ec  core: publish produced outputs through the versioned writers
(new) -> 867f7ec5b3  core: read the restored file's bytes in place in the final-offer restart test
(new) -> 4a541e4e93  core: drop the two value transfer tests that no environment can run
```

### Merge of #14049

Merged #14049 at main `601d12f424` (20:55:50 UTC) under the merge rule:
85 pass, 1 skipping, 0 pending, 0 failed after the single reruns
(test-base's engine-capacity error, provision's registry 500s twice,
golang:test-all's k3s pull); human approval by vito; head `b910097a74`
pinned; asynchronous endpoint (uuid `e2091d74-8590-4ccc-a58f-7bd2f2a7ab2f`,
"merged"). Automatic rebase above, every series equal by range-diff:
#14050 → `40b7df8381` (15, base main), #14051 → `a4f7b28366` (17), #14093
→ `23f71a77d4` (9), #14220 → `e9372bccdb` (3). Fresh runs watched. Fixes
redirected to main because #14049 is merged: none in the corrections
table target it. A1 moves onto `e9372bccdb` before publication.

A1 follow-ups after the lint commit: `f143286c05` (the two TerminalTarget
diagnostic literals restored; no other renamed receiver touched a
literal), `34cf231b26` (the scalar decoder loses its unused ctx, the
list decoder its unused server), `25d92459b9` "core: share the
filesystem dependency attachment and the git tree evaluation" (the four
dupl findings: `attachFilesystemDependencyResultsKinds`, generic over
the recipe's value type, for Directory/File; `evaluateGitTreeInto` with
an input-checking closure for the two git tree recipes; no exclusion).
Series moved onto A0's `f97c446e23` and then, after #14049's merge, onto
A0's `e9372bccdb` (range-diff all equal both times; the second move
changes no tree). Then `9db965f4e2` "dagql: drop the scalar envelope decoder's unused
decode context" (the final-tip lint on `25d92459b9`, /tmp/pkg-a1-lint5.log,
ERROR exit 1, one finding: unparam on the decode context my previous
follow-up left on the scalar decoder; dagql once on `9db965f4e2`,
/tmp/pkg-a1-tests8.log, exit 0, 383 top-level PASS, 0 FAIL, one inherited
nested SKIP). Tip `9db965f4e2`, 30 commits. Tests on `25d92459b9`,
clean (/tmp/pkg-a1-tests7.head): the five packages once, log
/tmp/pkg-a1-tests7.log, exit 0, 1070 top-level PASS, 0 FAIL, 0 top-level
SKIP, one inherited nested SKIP. Final-tip lint on `9db965f4e2`: /tmp/pkg-a1-lint6.log, LINTA1FINAL.

A2 conflict on the move onto A1's lint commit: `95382e5ac3` "test:
snapshot completed producers without copying mutexes" edits the
Directory and File subtest bodies that A1's lint commit moved into named
functions; its two hunks (a pointer snapshot of every field instead of a
struct copy, and pointer comparison) were applied inside
`testRecordCompletedProducerDirectory` and `testRecordCompletedProducerFile`
one indentation level up, nothing else.

#14051 test-split:test-workspaces fail on the post-#14049 run (head
`a4f7b28366`, trace `2276b0176a080b137b1b7e820581716b`):
TestWorkspace/TestWorkspaceExportLocalWorkdirAndFrom/from_baseline,
workspace_export_test.go:760, expected "host prior", actual "earlier
overlay"; the rest of the check passed. Main's test; the same #14051
content passed this check on `31f7c8dc10`. Reported with a one-rerun
proposal; not rerun.

### Merge of #14222 (main PR)

Merged #14222 at main `067d12ef0b` under the merge rule: 85 pass, 1
skipping, 0 pending, 0 failed; human approval by grouville; head
`7e3c431108`; plain `gh pr merge --merge`. The local-cache wait bound is
therefore fixed on main; PRs rebased onto a main containing
`067d12ef0b` no longer carry the TestDagqlMetadataGCProtectsActiveZeroDiskResults
timing failure. #14050's post-#14049 run (base before that merge) hit
exactly that failure once more (trace `eef8788c455af4621172e64af0b00bed`,
"timed out waiting for metadata workload session to close", 78.87 s);
rerun once as the known race.

#14051 test-workspaces: passed on its rerun (trace
`2312669ed6d65b690887d76d12e6473f`, 8m11s), so the from_baseline
assertion was a flake on this run; the coordinator's condition for a
local repro (a second failure on the same assertion) did not arise.
