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
Reviewer's qualification: the git extraction is not literally
behavior-identical for doubly invalid input, since CurrentDagqlServer now
runs before the closure's missing-SHA check, so a missing server wins
over a missing SHA; the successful path and the cleanup are unchanged.
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
SKIP, one inherited nested SKIP. Final-tip lint on `9db965f4e2`: /tmp/pkg-a1-lint6.log (head file /tmp/pkg-a1-lint6.head), findings 0, `golangci-lint:lint-all DONE [3m33s]`, exit 0.

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

A2 moves: onto A1's `34cf231b26` (one conflict, the split completed-producer
test, resolved as recorded above), then `25d92459b9` (`875d0d24bc`,
range-diff all equal, tests once: 1235 PASS / 0 FAIL, /tmp/pkg-a2-tests4.log),
then `9db965f4e2` (`ff54e7c06c`, range-diff all equal). Tests on
`ff54e7c06c`, clean (/tmp/pkg-a2-tests5.head): the five packages once,
log /tmp/pkg-a2-tests5.log, exit 0, 1235 top-level PASS, 0 FAIL, 0
top-level SKIP, one inherited nested SKIP. Local lint on `ff54e7c06c`
(/tmp/pkg-a2-lint2.log, head file /tmp/pkg-a2-lint2.head): `golangci-lint:lint-all
ERROR [3m11s]`, exit 1, 23 findings: 13 mechanical (two unused dupl
directives in core/schema/directory.go, an unused gocyclo directive in
dagql/cache_egraph.go, gofmt/goimports on two foreign_module_context
tests, unparam innerEnvFile, errorlint ×2 and gocritic elseif in
cache_offer_owner.go, S1023, QF1001, a leading newline) and 10 gocyclo
overages (validateValueBundle 54, ValidateForeign 53, ImportValues 47,
runRemoteCacheFixture 43, runTransferSchemaRecovery 42, WithExportedValues
41, holdTransferClosure 37, validateTransferEnvelope 36,
visitPersistedEnvelope 33, encodePersistedResultEnvelope 32). Policy
question (extraction versus main-style justified directives) sent to
the coordinator; the lint commit follows the ruling. Candidate sent to
the reviewer for source and placement with this stated.

#### A1 published: #14224

Reviewer approved `9db965f4e2` for publication (the two code follow-ups
reviewed; the git extraction qualified as above). Published under the
stack rule: pushed `sipsma/remote-cache-transfer-foundations` =
`9db965f4e2` (new branch); `gh stack link 13937
sipsma/remote-cache-transfer-foundations` created PR #14224 with base
`sipsma/remote-cache-test-store` and registered it: stack 13937 (id
487371) 13 members, #14224 at position 13 above #14220 (verbatim
/tmp/pkg-a1-stack.txt). Title and body set with `gh pr edit`; the tool
had created it as a draft, marked ready. Read-back
(/tmp/pkg-a1-readback.txt): https://github.com/dagger/dagger/pull/14224,
OPEN, draft false, base `sipsma/remote-cache-test-store`, head
dagger/dagger:sipsma/remote-cache-transfer-foundations @ `9db965f4e2`,
title "remote cache: transfer foundations and completed producers", 30
commits, body per the conventions (A3-supersedes-producers sentence, the
four dropped tests, commits and conflict resolutions, validation
commands and counts, the two CI gaps), no attribution text. CI watched.

#### #14224 follow-up: the completed-recipe attachment fix moved from A2

Reviewer's blocking placement finding on A2, agreed by the coordinator:
A2's `b561d4e4c0` (packaged `7316dfcc9a`, "core: attach the completed
Container recipe's inputs at publication", core/container.go:1150-1156
plus TestContainerCompletedProducerAttachesParentAtPublication) fixes
the completedRecipe mechanism A1 introduced in `1643893edb` (packaged
`dfed2f750e`, original c3491f00ad), as its own message says, and its
regression uses only A1 APIs. Cherry-picked onto #14224's tip as
`b63ea739dc` (clean pick; message kept, one paragraph appended naming
the packaged hash, the series hash and the A1 commit). Tests on
`b63ea739dc`, clean (/tmp/pkg-a1-tests9.head): `go test -v -count=1
-timeout 60s ./core/`, log /tmp/pkg-a1-tests9.log, exit 0, ok core
10.307 s, 510 top-level PASS, 0 FAIL, 0 SKIP, the regression PASS.
Reviewer: same change as `b561d4e4c0` (patches identical, range-diff
differs only by the provenance paragraph), approved conditional on the
lint. Lint on `b63ea739dc`: /tmp/pkg-a1-lint7.log (head file
/tmp/pkg-a1-lint7.head), findings 0, `golangci-lint:lint-all DONE [4m10s]`,
exit 0. Pushed: `sipsma/remote-cache-transfer-foundations` = `b63ea739dc`
(fast-forward, lease on `9db965f4e2`), #14224 at 31 commits; one
"Follow-up" sentence appended to the coordinator's rewritten description
through the REST API (`gh pr edit` fails on a GraphQL projects
deprecation). Host note: the disk hit 99% (builds failed with ENOSPC)
during this work; /home/exedev/.cache/go-build was 286 GB and was
cleared with `go clean -cache` (regenerable), finished worktrees were
removed and CI logs over 5 MB gzipped in place (their recorded paths
gain a .gz suffix); 296 GB free afterwards. Corrections
table: `7316dfcc9a`/`b561d4e4c0` → A1 (#14224), out of A2. A2 is rebased
onto `b63ea739dc` so the commit drops out of its series. Reviewer's note
taken: core/schema runs at `-timeout 120s` from now on.

A3 (b4-acquisition, 44 packaged commits onto A2's `ff54e7c06c`; moves to
A2's final tip later), conflicts so far: `c97461e27b` dagql/cache.go
(evaluateGroup becomes a runLazyTask wrapper with part-task tokens; the
extracted `runLazyEvalBody` takes the token and continuation and runs
the commit's body verbatim: end the body phase, finish the continuation
or sync leases; Erik's `//nolint:gocyclo` on the 48-complexity
runLazyTask goes back in the lint step per the directive rule);
`2ba0ef6c08` core/value_transfer_chain_test.go (the selected-chain test
returns in its lazy-outputs form, taken whole with mustTransferPath);
`4e2515e704` dagql/cache_persistence_self.go (the object and list
decoders gain the cleanup join, part-host binding, inline borrow and
the indexed `dec.item`, threaded into the extracted helpers);
`8093fa8ec6` core/completed_producer_test.go (the new "scratch" subtest
added as `testRecordCompletedProducerScratch`, consistent with the lint
extraction).

### Open intermittent CI failure: TestWorkspace/TestWorkspaceExportLocalWorkdirAndFrom/from_baseline

Assertion at core/integration/workspace_export_test.go:760: expected
"host prior", actual "earlier overlay": the export compared `after`
against a `From` workspace that resolved to its pre-overlay state, an
identity-shaped failure (a stale identity hit, not stale content).
Occurrences: CI fail on #14051 (head `a4f7b28366`, trace
`2276b0176a080b137b1b7e820581716b`); CI fail on #14224 (head
`9db965f4e2`, trace `7e2a61f09d1df90a1aef8a1b93974ad5`); CI pass on
#14051's rerun (trace `2312669ed6d65b690887d76d12e6473f`); not
reproducible locally by the coordinator: 7/7 on #14224's head
(/tmp/workspace-export-repro-14224.log) and 35/35 with `--count 5` on
#14051's head (/tmp/workspace-export-repro-14051-x5.log). Other
authors' red workspace suites fail different subtests. Seen only on
stack trees so far; cause unknown. Rule: reruns on this assertion are
allowed, every occurrence is logged here with PR, head and trace, and
the third CI occurrence stops the work and becomes a named
investigation. #14224 test-workspaces rerun once (this entry's second
occurrence).

A2 lint commit `c5338475d2` (on `594ab859d9`, the series rebased onto
#14224's backport tip `b63ea739dc` with `b561d4e4c0` dropped): 16 files,
+96/-77; two one-block extractions (validateTransferEnvelopeKind,
visitPersistedObjectEnvelope), eight justified gocyclo directives under
the coordinator's rule, thirteen mechanical fixes (listed in the
message). Reviewer's qualification: the errorlint `%w` changes preserve
the message text and the ErrPersistStateNotReady matching and
additionally expose the underlying JSON errors to errors.Is/As; the rest
is behavior-equivalent. Reviewer approved `c5338475d2` (source,
packaging, lint by terminal line); publication held by the coordinator
on the #14224 cache-persistence question. Tests on `c5338475d2`, clean (/tmp/pkg-a2-tests6.head): four
packages at 60 s (/tmp/pkg-a2-tests6.log) and core/schema at 120 s
(/tmp/pkg-a2-tests6-schema.log, 48.358 s), exit 0, 1235 top-level PASS,
0 FAIL, 0 top-level SKIP, one inherited nested SKIP. Lint on
`c5338475d2` (/tmp/pkg-a2-lint3.log, head /tmp/pkg-a2-lint3.head):
findings 0, `golangci-lint:lint-all DONE [3m24s]`, exit 0. Publication
held pending the #14224 cache-persistence investigation.

#14224 test-split:test-cache-persistence on `b63ea739dc` (trace
`d42773c5d81557f51f2f5b0371010cf4`, logs
/tmp/ci-logs-14224-cache-persistence.log.gz):
TestCachePersistence/TestDiskPersistenceAcrossRestart/module_core_metadata_returns_survive_restart,
expected 8080, actual 9090; the same check passed on `9db965f4e2`, and
the only difference is the backported `b63ea739dc`. Treated as stack
evidence, not rerun; two dev-engine repros ordered by the coordinator
(`b63ea739dc`, then `9db965f4e2`; then A2's `594ab859d9` if the first
fails and the second passes), engine use coordinated with the
workspace-export investigator.

#14051 test-split:test-base on `a4f7b28366` (trace
`6ffb62d07beb1e1c12a1e48c8ab76a8c`): "check cancelled: max execution
time exceeded" with 2462 passed and none failed; the job limit again;
rerun once.

from_baseline occurrence log, continued: #14224's rerun on head
`9db965f4e2` passed (trace `1ea9f69d191f1e3d2391c7299c818288`). The
investigator found the same assertion outside the stack (vito's #14165,
head `fdb299ef9f26`, trace `e0f542ce9b4d114a0ac1156d075e3263`) and is
pursuing a phantom-diff hypothesis in Changeset.Export.

#14224 test-split:test-base on `b63ea739dc` (trace
`51c2f54bdfee7ae84f5f5ee95af89224`): core `TestHTTPProducerCleanup` FAIL
(an A1 unit test that passes on this host); assertion below.

`TestHTTPProducerCleanup/timestamp` (the #14224 test-base failure above):
the case re-executes the test binary under strace to inject a timestamp
fault, unconditionally; CI's test image has no strace. Follow-up
`0cd8b591af` "core: skip the HTTP producer timestamp fault when strace is
absent" (a tool probe, not a privilege probe, per the coordinator):
`t.Skipf` with the reason when LookPath fails. Tests on `0cd8b591af`,
clean (/tmp/pkg-a1-tests10.head): core once at 60 s,
/tmp/pkg-a1-tests10.log, exit 0, 510 top-level PASS, 0 FAIL, 0 SKIP (the
case PASS here, strace present). Lint on `0cd8b591af`: /tmp/pkg-a1-lint8.log (head /tmp/pkg-a1-lint8.head), findings 0, `golangci-lint:lint-all DONE [1m34s]`, exit 0. Reviewer approved; pushed, `sipsma/remote-cache-transfer-foundations` = `0cd8b591af` (fast-forward, lease on `b63ea739dc`), #14224 at 32 commits; one sentence added to the description's Validation paragraph (the case skips where strace is absent and is unexecuted in CI; any CI SKIP there counts as unexecuted, not as fault-injection proof).
#14050 release:publish-with-mock-endpoints cancelled at the job limit
(trace `15a51293f02c17b68136a2b783a608cb`); rerun once.

from_baseline, closed for the stack: the workspace-export investigator
reproduced it deterministically on main (trace
`e787d1cef8f22aa5670c92d790f86ae3`, /tmp/ws-main-phantom-repro.log):
Changeset.Export sends the whole stat-sensitive snapshot diff including
mtime-only entries while the declared changed paths are content-based.
Main defect, fix PR in progress; reruns on that assertion are free and
it no longer counts toward the stop rule.

### A3 candidate (`pkg/a3`, worktree /tmp/pkg-a3), in progress

Source: packaged `b4-acquisition`, `d46b43fc0a..a760512501` (44 commits),
rebased onto A2's `ff54e7c06c` (moves onto A2's final tip before
review). 46 commits: the 44 originals plus `0513487968` (the demanded-read
commit, whole, above `87b2aec8fd`, the last commit editing its three lazy
test files; coordinator's placement (b)) and "core: read demanded file
bytes in place in the HTTP chain and part acquisition tests" (the
http_lazy_test.go and part_acquisition_test.go hunks of `397168d119`).
Conflicts resolved (recorded above and here): the lazy attempt loop
(`runLazyEvalBody` takes the part-task token and continuation and, after
`dad18dc86f`, returns the leased context again for
`completeNativePartTask`); the selected-chain test's lazy-outputs form;
the envelope decoders' cleanup join, part-host binding, inline borrow and
indexed `dec.item`; the split completed-producer test's new "scratch"
subtest; `git.go`'s shared tree evaluation routed through
`dir.evaluateLazy` and then `validateLazyDirectoryReceiver`; the git
schema's `tree`/`commitTree` building lazy directories inside main's
pinned and `__fullCheckout` branches; the lazy-on-every-path commit
dropping `completedRecipe` (the shared attach helper loses that
parameter; `completed_producer_test.go` and `producer_path_cleanup_test.go`
take the commit's form; `producer_recording_fault_test.go` deleted with
it); the rename commit (the fixture file keeps A1's struct fixture,
`executionContext` and no mount namespace, with the commit's new type and
test names; `git_lazy_test.go` and `builtin_lazy_test.go`, renames of the
files A1 dropped, stay deleted); one stale `producedFileContents(t, ctx,
…)` call in http_lazy_test.go folded into its commit. TLA audit on the
tip: 0 findings (30 constants, 10 variables, 43 cfgs; one line changed in
CacheLifecycle.tla). `runLazyTask` is at 48; Erik's original directive
and justification return in the lint step per the directive rule.

Tests on `54ea95dea1`'s successor tip (/tmp/pkg-a3-tests2.head): core,
dagql, engine/snapshots at 60 s (/tmp/pkg-a3-tests2.log) and core/schema
at 120 s (/tmp/pkg-a3-tests2-schema.log): dagql and engine/snapshots ok;
1180 top-level PASS, 6 FAIL: four mount-bound tests (TestLazyEvaluatedFilesystemClones,
TestValueTransferPartsSelectedChain, TestBuiltinMetadataSelectors,
TestLazyStoredResultsWithoutBacking), TestGitResolvedFrames (expects 3
resolved frames, sees 4: main's `__fullCheckout` frame), and main's
TestWorkspaceGitCheckoutReuse ("no query in context": the lazy tree
reads the current query). Sorted and sent to the coordinator; nothing
adapted pending the ruling.

Correction to the line above: TestGitResolvedFrames does not count
frames. Its assertion (git_lazy_test.go:109) compares the persisted lazy
tree's RefResultID with the captured resolved ref's ResultID; the extra
result is main's pinned ref, not `__fullCheckout` (see below).

#### Rulings applied (coordinator): probe helper, pinned ref, query context

Three commits on `pkg/a3` above the previous tip:

- `ca84e738e0` test: probe read-only bind mount privileges before native
  snapshot reads. New exported `testutil.RequireNativeMount` in
  engine/snapshots/testutil/privilege.go, ported from the store's old
  `requireNativeMount` (`21f73b7a33:engine/snapshots/testutil/store.go`),
  called first in the four mount-bound tests. Run alone here (60 s core,
  120 s core/schema): core `ok` (both skip), core/schema
  `--- SKIP: TestBuiltinMetadataSelectors`,
  `--- SKIP: TestLazyStoredResultsWithoutBacking`.
- `11462cc28a` test: expect main's pinned ref input for git trees without
  .git. Main's `pinnedGitTree` (main `19d5eee488`) selects
  `ref(name: <sha>)` on the repository before `tree` for trees without
  .git, so the lazy input is that pinned ref, one result after the
  resolved one. The ruling's literal 4 would have broken the `fixed` case
  (name == SHA, no pinning); the commit expects `record.ResultID+1` when
  the ref name differs from the SHA, `record.ResultID` otherwise. 10/10
  subtests PASS. Correction reported to the coordinator.
- `c8f61d7c76` test: give the workspace checkout reuse test a query
  context (root query on `currentTypeDefsTestServer` with a platform, as
  the other schema tests). "no query in context" is gone; the test still
  fails, see next item.

#### Presented, not adapted: TestWorkspaceGitCheckoutReuse under lazy trees

Main's test (`ab34fdabac`, `61e4ab7ecf`) counts the fixture backend's
Tree calls: retained checkout once (still holds, `__fullCheckout` is
eager), then a discard=true request for the public tree with
keepGitDir=false, a depth-1 request for the default tree, an includeTags
request for the tagged tree. A3's lazy `GitRef.tree` calls the backend
only on evaluation (core/git.go:927 applies the repository flag there),
so none of the three is recorded at selection. Passes at A2's tip
`c5338475d2`. Failures: discard=true cases at "public tree must still
honor keepGitDir=false" (discarded 0); discard=false cases at the depth-1
check. Two test-only shapes trialed and reverted:
`/tmp/pkg-a3-workspace-reuse-option2.diff` (6 added lines: evaluate each
lazy tree with `cache.Evaluate` before main's assertion, assertions
verbatim; 6/6 PASS; recommended) and
`/tmp/pkg-a3-workspace-reuse-option1.diff` (assert the lazy's args
instead; fails for discard=true since the repository flag is applied at
evaluation). Awaiting the coordinator's ruling; A3 tip `c8f61d7c76`.

Lint pre-run on `c8f61d7c76` started (/tmp/pkg-a3-lint-pre.log) to
prepare the lint commit; the final lint-all runs on the final tip.
Already known: gofmt flags core/schema/foreign_module_context_test.go
(A3's own `10e821d6d5`).

#### #14224 cache-persistence repros (dev engine, one invocation each)

`dagger --engine=container://remote-cache-engine api call engine-dev test
--pkg ./core/integration --run
TestCachePersistence/TestDiskPersistenceAcrossRestart/module_core_metadata_returns_survive_restart`

| head | content | result |
|---|---|---|
| `b63ea739dc` | #14224's head with the moved backport `b561d4e4c0` | FAIL `--- FAIL: ...module_core_metadata_returns_survive_restart (73.85s)`, expected 8080 actual 9090 (engine_persistence_test.go:571); /tmp/pkg-14224-cachepersist-repro-b63.log |
| `9db965f4e2` | the head right below the backport | PASS `✔ github.com/dagger/dagger/core/integration 1 passed`, trace `6eba55cfc228dfb8defdde1fb30fefda`; /tmp/pkg-14224-cachepersist-repro-9db.log |
| `594ab859d9` | A2's old tip on `9db965f4e2`, `b561d4e4c0` inside A2 | running; /tmp/pkg-14224-cachepersist-repro-594.log |

So far the failure appears with `b561d4e4c0` and not without it.

Engine rule change (Erik, via the coordinator): the dev engine is shared,
no holds or hand-offs; runs may overlap, timeouts sized 25–30 m.

Stack check state at this point: #14050, #14051, #14093, #14220 all 86
checks pass; #14224 (`0cd8b591af`) 85 pass, 1 pending, none failed;
review decision REVIEW_REQUIRED (no human approval yet on any).

#### Cache-persistence subtest: cause found, not persistence

Repro 3 (`594ab859d9`, A2's old tip with `b561d4e4c0` inside A2): PASS
`✔ github.com/dagger/dagger/core/integration 1 passed`
(/tmp/pkg-14224-cachepersist-repro-594.log).

Repro 1's log (/tmp/pkg-14224-cachepersist-repro-b63.log:416-420) shows
the assertion at engine_persistence_test.go:571 failing from :611,
`first := request(a)`: the first request, before any restart. Cause:
`Container.exposedPorts` (core/schema/container.go:4590) iterates the
container config's ExposedPorts Go map with no sort; main's resolver is
identical (main observation, not a defect this stack fixes). Go map
iteration order is random: 200 iterations over a two-entry map here put
8080 first 175 times, 9090 first 25 (about 1 in 8). The subtest asserts a
fixed order, so it fails about one time in eight on first evaluation
regardless of `b561d4e4c0`. The subtest is the stack's (from
`153fef89e6`, present only in #14224's head), so by Erik's rule the fix
is a test-only follow-up in #14224.

Coordinator's ruling: sort the returned ports by number before the
assertions, with a comment; one dev-engine run of the subtest (deterministic
by construction); reviewer; push. Follow-up commit on `pkg/a1`:
`test: compare persisted core ports in sorted order` (hash in
/tmp/pkg-14224-followup.head), 4 added lines in
core/integration/engine_persistence_test.go (`sort` import, comment,
`sort.Slice` by port). Dev-engine run: /tmp/pkg-14224-cachepersist-followup.log
(in progress at the time of writing).

#### A2 published: #14228 `sipsma/remote-cache-value-transfer`

Persistence hold lifted by the coordinator. `pkg/a2` moved from
`b63ea739dc` onto #14224's reviewed head `0cd8b591af`
(`git rebase --onto`), tip `c7dc73fbf7`; `git range-diff` of the 21
patches: all `=` (equal patch series); 21/21 DCO signoffs, no attribution
trailers, author Erik. Pushed to upstream `sipsma/remote-cache-value-transfer`
(new branch), `gh stack link 13937 sipsma/remote-cache-value-transfer`
created #14228 (base `sipsma/remote-cache-transfer-foundations`) as a
draft; title and approved body set via
`gh api -X PATCH repos/dagger/dagger/pulls/14228 --input /tmp/pkg-a2-patch.json`
(title "remote cache: value transfer between engines"; body
/tmp/pkg-a2-pr-body.md with one sentence updated for the probe ruling:
SelectedChain "lives on behind a mount-privilege probe"); marked ready
with `gh pr ready`. Stack API `repos/dagger/dagger/stacks/13937`: id
487371, #14228 at position 14 of 14, above #14224. Read back: head
`c7dc73fbf7`, base `sipsma/remote-cache-transfer-foundations`, head repo
dagger/dagger, draft false, body verbatim (saved
/tmp/pkg-a2-pr-body.live.md, differs from the file by a trailing newline
only). Pending: once the #14224 follow-up is pushed, move #14228 onto the
new #14224 head (mechanical, range-diff) and push with
`--force-with-lease`; that costs a second CI run on #14228.

#### A3 moved onto A2's current tip

`git rebase --onto c5338475d2 ff54e7c06c pkg/a3` (50 commits; pre-move tip
tagged `pkg/a3-before-move` = `db5d4fd1f3`). One conflict, in
dagql/cache_persistence_codec.go: A2's lint commit had extracted the
object-envelope case into `visitPersistedObjectEnvelope`, and A3's
`9aa3fd459a` rewrote that case (snapshot-link scopes: `scopes.project`,
`ValidateSnapshotScope`, `scopes.rewrite`). Resolved by keeping the
extracted helper and applying A3's body inside it, signature
`(env, ownerCall, scopes, path, visit)`; `go build ./dagql/` ok. Tip in
/tmp/pkg-a3-moved.head; build, vet and lint-all on it running
(/tmp/pkg-a3-lint2.log).

#### #14224 follow-up pushed; persistence entry closed

Reviewer approved `c3f7dc33f6` (no findings). Pushed with
`--force-with-lease=sipsma/remote-cache-transfer-foundations:0cd8b591af`
(fast-forward `0cd8b591af..c3f7dc33f6`); #14224 head `c3f7dc33f6`.
Persistence entry closed: test-order flake in the stack's own subtest,
fixed in #14224; repro 3 PASS recorded above. The resolver's map
iteration stays as on main (main observation).

Rule (coordinator, Erik): a PR above is moved only if the commit below
changes code it builds on or tests that run in its CI; a test-only
follow-up in #14224 changes neither for #14228, and GitHub's stack
rebase moves it at merge time anyway. #14228 stays on `0cd8b591af`.

#### A3 rulings applied after the move; tip check; lint commit

Coordinator's ruling on TestWorkspaceGitCheckoutReuse: option (1), evaluate
each lazy tree with `cache.Evaluate` right before main's assertion and
compute `discarded` after; main's assertions verbatim. Committed as
`db5d4fd1f3` (pre-move) → after the move onto `c5338475d2` the four ruling
commits are `b4010ffd2b` (probe), `482bf2e4ba` (pinned ref), `c730965638`
(query context), `b6738941a0` (evaluate lazy trees). The coordinator
accepted the pinned-ref correction (its `__fullCheckout` premise was wrong).

Tip check on the moved tip plus lint working tree (one invocation per
package, 60 s, core/schema 120 s; /tmp/pkg-a3-tests4.log,
/tmp/pkg-a3-tests4-schema.log): `ok core 19.222s`, `ok dagql 4.229s`,
`ok engine/snapshots 5.742s`, `ok core/schema 13.701s`; dagql/persistdb and
engine/snapshots/testutil have no test files. 1182 top-level PASS (1016 +
166), 0 FAIL, 4 SKIP (the four probed tests).

Lint-all on the moved tip before fixes (/tmp/pkg-a3-lint2.log): terminal
line `golangci-lint:lint-all ERROR [1m37s]`, 52 findings
(/tmp/pkg-a3-lint2.findings; the pre-move run /tmp/pkg-a3-lint-pre.log
had 40, most of them A2's already-fixed ones). Lint commit `9958d1caba`
"lint: meet main's golangci-lint configuration" (35 files, +692/−568),
content per its message: mechanical fixes; three dupl pairs extracted into
filesystemOutput helpers (lazyEvalFunc, evaluateLazy, openSnapshotPart);
four SA2001 sites → `LazyState.awaitUnlocked` / `lazyGroupOnce.awaitUnlocked`
(Lock + deferred Unlock, documented barrier) and a TryLock assertion in the
test; gocyclo: real extractions for NewCache, NthValue,
Container.DecodePersistedObject, PreparePartRecord, PrepareReadyPart,
prepareEvaluatedParts, assertColdPartDelegation, TestPartDelegationRealStore;
directives (main's form, one-line reason) for runLazyTask (Erik's line),
CommitReadyPart, demandPart, scanPartSources, selectDemandPartSource.
Local gocyclo tool after the commit lists none of the flagged functions
under 30 except the five with directives. Lint-all on `9958d1caba` running
(/tmp/pkg-a3-lint3.log).

#### A3 lint result, tip check, and an intermittent failure in the series

Lint-all on `9958d1caba` (/tmp/pkg-a3-lint3.log): `golangci-lint:lint-all
ERROR [1m32s]`, 3 findings, all from the lint commit itself (receiver name
of the new `LazyState.awaitUnlocked`, two ineffectual `persistDB`
assignments in NewCache). Fixed and the unreviewed, unpushed lint commit
amended → `d67442f45a`. Lint-all on `d67442f45a`
(/tmp/pkg-a3-lint4.log): `golangci-lint:lint-all DONE [1m32s]`, 0 findings.

Tip check on `d67442f45a` (one invocation per package, 60 s, core/schema
120 s; /tmp/pkg-a3-tests5.log, /tmp/pkg-a3-tests5-schema.log): `ok dagql
4.781s`, `ok engine/snapshots 5.546s`, `ok core/schema 13.743s`;
`FAIL core 18.633s`: `--- FAIL: TestPartInlineAddress/concurrent (0.25s)`,
"snapshot path does not name a declared inline envelope"
(part_inline_test.go:115, from dagql/cache_snapshot_scope.go:72). 1181
top-level PASS, 1 FAIL, 4 SKIP.

This failure is intermittent and predates every adaptation here: it was in
A3's first run at the original content (/tmp/pkg-a3-tests.log) and absent
from runs 2–4; a focused `go test -count=20 -run 'TestPartInlineAddress$'
./core/` on `d67442f45a` failed 1 of 20. Batch 7's own record
(implementation/b7/REPORT-AUTHOR-B.md:14, manifest/BATCH-7.md:33) names it:
a race in batch 4's source scan when two parts of one imported row are
demanded concurrently, about one run in twenty, "on the unchanged batch 6
tree as well"; its fix is `959054a2e0` "dagql: do not select a part source
from a row that could not be captured" (dagql/cache_part_source.go +15,
cache_part_source_test.go +59, cache_value_transfer_test.go +6), authored on
`b7-integration-author-b`, and it is not in the packaged `b4-acquisition`
branch nor in A3 (subject absent; see below for the code check). Reported to
the coordinator for a ruling (Erik's rule points at A3, the PR whose commit
introduced the defect).

Hash map for the series: /tmp/pkg-map-a3.txt (44 originals mapped, 7 new:
two demanded-bytes commits, four ruling commits, the lint commit).

### Merge of #14227 (main PR): from_baseline fixed on main

#14227 `sipsma/changeset-export-declared-paths` (`9020dae33c`, the
investigator's Changeset.Export fix): vito APPROVED 2026-09-18T22:37Z
(human maintainer), 86/86 checks pass, merge state CLEAN. Merged with
`gh pr merge 14227 --merge` at 22:40:53Z; merge commit `4056f4a8b2`
(`upstream/main` head). The from_baseline entry
(TestWorkspace/TestWorkspaceExportLocalWorkdirAndFrom/from_baseline) is
now: fixed on main at `4056f4a8b2`. Stack PRs still base on the older
main until they move; a from_baseline failure on them stays off the
stack's books and free to rerun. #14227 was not a stack member: no
range-diff, no automatic rebase.

CI watch re-armed with approvals: each cycle polls check state and the
approving reviewers on #14050, #14051, #14093, #14220, #14224, #14228.

### Corrections table: the predecessor's ten batch-7 corrections and their destinations

Source: implementation/b7/manifest/BATCH-7.md "Corrections to earlier
batches' code, kept as their own commits in batch 7" and batch-7.json.
The predecessor kept all ten as their own commits in the packaged
`b7-verification` branch and left the fold-back to the Human; none was
in A2 (#14228) or A3. Ruling (coordinator, Erik's step-4 rule: the fix
goes to the PR whose commit introduced the defect; hunks that need a
later series' machinery go with that series):

| Packaged | Original | Corrects | Destination |
|---|---|---|---|
| `51d24c1f09` | `e094252906` name every reselect refusal, watch all seven loops | batch 4 | split: naming part (new dagql/cache_part_refusal.go, every bare `ErrPartReselect` return named by site) → A3 as `565728162c`, directly above `c14313a18e`; the reselect-watch wiring (partReselectWatch is batch 6's) → A5 above its watch commit |
| `777e484881` | `f9db98a420` progress rule (PartNoProgressError) | batch 4 | A5 above the watch commit (the rule is wired through the watch); Erik's earlier note had it in A6, coordinator is telling him |
| `af0a3b200f` | `af2ddb4e36` one refusal is one progress record | batch 4 | A5, after f9db98a420 |
| `dcd36f6c96` | `959054a2e0` scan does not select an uncaptured row | batch 4 | A3 as `279425d3d2`, above `565728162c` (needs partRefusedBy); carries the two `revisionHook` test-hook lines its test uses (from the offers series' test coverage commit, to be found present there) |
| `6c6cdfd8ac` | `1bfece3b77` clone a part-acquired File or Directory | batch 4 + lazy values | code files → A3 as `49087746ad`; its test core/part_restored_clone_test.go (snapshot sharing + gated fixture) → A6 |
| `3e0a06bca5` | `e844245c8b` key-only offer's first renewal writes its addresses | batch 5 | A4 when cut |
| `f2c36a337f` | `87c099a615` WithExportedValues never waits (docs) | batch 2 | #14228 follow-up, last of four |
| `fbd8ca8f17` | `16786b5fe3` own a backing snapshot created after import | batch 2 | #14228 follow-up 1 |
| `2b34e9e3e7` | `5d3ee071c7` drop a backing snapshot whose owner lease failed | batch 2 | #14228 follow-up 2 |
| `3238e5c94c` | `cfa148371c` creation, sync and discard one step per value | batch 2 | #14228 follow-up 3 |

Placement in A3: every file the batch-4 corrections touch was last
changed by the original `c14313a18e` "cache: name and persist Lazy
operation acquisition" (#37 of 51), so all three sit directly above it,
in dependency order. Naming part built mechanically: e094252906's
old→new line pairs applied with up to four lines of the diff's preceding
context to pin each site (42 sites applied; 12 name sharing-series code
A3 lacks; 3 commit sites A3 has in a different shape carry the same
names, one combined, "commit: donor unregistered or facts changed"; no
bare `ErrPartReselect` return remains). Built and vetted at each point;
core at the bare `c14313a18e` fails five tests (two mount-bound ones the
probe commit later covers, TestPartAcquisitionRootRoutes's mount reads
the demanded-bytes commits later cover, and two container-persistence
tests later originals fix), and the same five, no others, fail at each of
the three correction commits (/tmp/pkg-a3-cp-core.log,
/tmp/pkg-a3-cp-core-chain.txt); dagql `ok` at each point. Messages kept
with a provenance paragraph; author and signoff Erik.

#### A3 rebased onto the correction chain

`git rebase --onto 49087746ad c14313a18e pkg/a3` (pre-corrections tip
tagged `pkg/a3-before-corrections` = `d67442f45a`): 14 commits replayed,
no conflicts. One compile fix folded into the unreviewed lint commit
(amended → `c14909d8a4`): the scan fix's new test called
`partTestEquivalent` with the context parameter the lint commit removes.
Series now 54 commits on `c5338475d2`; map /tmp/pkg-map-a3.txt: 44
originals mapped, 0 unmapped, 10 new (three corrections `565728162c`,
`279425d3d2`, `49087746ad`; two demanded-bytes commits; four ruling
commits; the lint commit). TLA audit on the tip: 0 findings (30
constants, 10 variables, 43 cfgs). Scratch worktrees /tmp/pkg-a3-cp and
/tmp/pkg-a3-base removed. Running on `c14909d8a4`: focused
`go test -count=20 -run 'TestPartInlineAddress$' ./core/`
(/tmp/pkg-a3-inline20.log), the full tip check (/tmp/pkg-a3-tests6.log,
/tmp/pkg-a3-tests6-schema.log), lint-all (/tmp/pkg-a3-lint5.log).

#### A3 candidate `c14909d8a4`: results

Focused `go test -count=20 -run 'TestPartInlineAddress$' ./core/`:
20/20 `--- PASS: TestPartInlineAddress`, `ok core 39.753s`
(/tmp/pkg-a3-inline20.log). Tip check, one invocation per package, 60 s,
core/schema 120 s: `ok core 20.250s`, `ok dagql 4.636s`, `ok
engine/snapshots 9.265s`, `ok core/schema 14.844s`; 1184 top-level PASS
(1018 + 166), 0 FAIL, 4 SKIP (the four probed tests)
(/tmp/pkg-a3-tests6.log, /tmp/pkg-a3-tests6-schema.log). Lint-all:
`golangci-lint:lint-all DONE [1m40s]`, 0 findings (/tmp/pkg-a3-lint5.log).
54/54 DCO signoffs, no attribution trailers, author Erik on all. Sent to
the reviewer (/tmp/pkg-a3-review-request.md); description draft
/tmp/pkg-a3-pr-body.md to the coordinator.

#### Record corrections (reviewer) and the missing helper fix

Corrections to the `c14909d8a4` record above: the focused command was
`go test -count=20 -timeout 120s -run 'TestPartInlineAddress$' -v ./core/`;
SKIP accounting is 5 SKIP lines, 4 top-level (the probed four) plus the
inherited nested TestCacheContextCancel/last_waiter_canceled_fn_returns_value_still_releases,
all unexecuted; head files /tmp/pkg-a3-tests6.head, -inline20.head,
-lint5.head (= `c14909d8a4`) written after the fact from
/tmp/pkg-a3-final.head, which was written right after the amend, with
`git status --short` empty before the runs.

Reviewer's preliminary blocking finding: the helper fix `ed7a4a47f9`
(packaged `46ee7035b8`), placed by the coordinator's earlier ruling (c)
into A3 above `4e2515e704`, was recorded but never applied: store.go
still handed inPlaceApplier and inPlaceDiffer raw `s.Content`. Applied
now per the coordinator: cherry-picked onto `8cc37d8e53` (candidate of
`4e2515e704`) cleanly, regression `--- PASS: TestStoreObservesDifferWrites`
at that point, message kept plus provenance line → `1b6d8f7e61`; the
remaining 49 commits rebased on it with no conflicts
(`pkg/a3-before-helperfix` = `c14909d8a4`). Tip `efaa347626`, 55
commits; map refreshed (/tmp/pkg-map-a3.txt: 44 mapped, 0 unmapped, 11
new).

Corrections-table check against the series, every A3 entry: `0513487968`
(demanded-read, ruling (b)) present as `e0eae359e6`; the batch-7
privilege commit's http_lazy_test.go and part_acquisition_test.go hunks
present as `273cacdfbe`; its lazy_operation_execution_test.go hunk
present minus the two helpers, which A0's core/demanded_read_test.go
carries (verified: all 43 added lines of that hunk are in A3 except the
13 helper lines, found in core/demanded_read_test.go:18,32); the three
batch-4 corrections present (`565728162c`, `279425d3d2`, `49087746ad`);
`ed7a4a47f9` was the one gap, now `1b6d8f7e61`. Running on `efaa347626`:
tip check (/tmp/pkg-a3-tests7.log, -schema.log) and lint-all
(/tmp/pkg-a3-lint6.log).

#### A3 candidate `efaa347626`: results, follow-up review

Reviewer's full verdict on `c14909d8a4`: changes required, B1 only (the
helper fix); everything else clear; follow-up limited to B1, the map and
range-diff, and validation. Rule from the reviewer's hygiene note: the
head file (commit and dirty count) is written inside each run command,
immediately before the run.

Range-diff `c5338475d2..c14909d8a4` vs `c5338475d2..efaa347626`
(/tmp/pkg-a3-rangediff-helperfix.txt): 54 `=`, 1 new (`1b6d8f7e61` at
position 6). Tip check on `efaa347626` (/tmp/pkg-a3-tests7.head written
in the run command; tree clean): `ok core 21.037s`, `ok dagql 5.009s`,
`ok engine/snapshots 7.147s`, `ok core/schema 13.741s`; 1185 top-level
PASS (1019 + 166), 0 FAIL, 5 SKIP lines (4 probed top-level + 1 inherited
nested), `--- PASS: TestStoreObservesDifferWrites (0.15s)`
(/tmp/pkg-a3-tests7.log, -schema.log). Lint-all (/tmp/pkg-a3-lint6.head):
`golangci-lint:lint-all DONE [58.0s]`, 0 findings. Sent to the reviewer;
description draft's PASS count updated to 1185 (coordinator told).
Description approved by the coordinator as drafted; title "remote cache:
lazy part acquisition"; publish at position 15 on the reviewer's approval.

### A3 published: #14229 `sipsma/remote-cache-lazy-part-acquisition`

Reviewer approved `efaa347626` (B1 closed; range-diff, map, validation
checked independently). Move onto #14228's head `c7dc73fbf7`
(`git rebase --onto c7dc73fbf7 c5338475d2`, trial worktree
/tmp/pkg-a3-move): 55 commits, no conflicts; range-diff reviewed vs
moved (/tmp/pkg-a3-rangediff-move.txt): 54 `=`, 1 `!` by context only
("cache: name and persist Lazy operation acquisition" edits the file
A1's strace skip `0cd8b591af` also edits; the skip's six lines verified
present in the moved tip's core/lazy_operation_execution_test.go). Build
and vet ok; core once at the moved tip (/tmp/pkg-a3-move-core.head:
`e8846990e9 dirty=0`, written before the run): `ok core 19.549s`, 559
PASS, 0 FAIL, 2 SKIP lines (/tmp/pkg-a3-move-core.log). `pkg/a3` reset to
`e8846990e9`; `pkg/a3-reviewed` = `efaa347626`.

Pushed to upstream `sipsma/remote-cache-lazy-part-acquisition` (new
branch); `gh stack link 13937 sipsma/remote-cache-lazy-part-acquisition`
created #14229 (base `sipsma/remote-cache-value-transfer`) as a draft;
title and approved body (1185 count) set by REST PATCH
(/tmp/pkg-a3-patch.json); `gh pr ready`. Stack API: id 487371, 15
members, #14229 at position 15 above #14228 (14). Read back: head
`e8846990e9`, base `sipsma/remote-cache-value-transfer`, head repo
dagger/dagger, draft false, body verbatim (trailing newline only;
/tmp/pkg-a3-pr-body.live.md). CI watch re-armed with #14229.

### #14228 follow-ups: the three A2 code corrections and the docs line

Coordinator's ruling: the three code corrections as follow-up commits at
#14228's tip in manifest order, then the docs-only one; of
core/backing_snapshot_test.go, cases needing the gated fixture go to A6
with the fixture, cases that run without it stay. Split (checked on a
scratch copy with all three fixes): TestImportedBackingSnapshotIsOwnedByItsRow
(16786b5fe3) needs no fixture, stays; TestImportedBackingSnapshotIsDroppedWhenItsOwnerAttachFails
(5d3ee071c7) and TestImportedBackingSnapshotConcurrentFirstUses
(cfa148371c) arm `ArmTransferFixtureBarrier` after
`EnableTransferFixtureParts`, go to A6; the file's shared helpers stay.

Commits on `pkg/a2` above the published `c7dc73fbf7` (tag
`pkg/a2-published`): `2266f481a1` (16786b5fe3 whole), `95a320301e`
(5d3ee071c7 minus its fixture case), `a4ea34dcec` (cfa148371c minus its
fixture case; test file taken as cfa148371c's version minus the two
fixture functions after a context conflict), `dca16de409` (87c099a615,
the four doc lines placed above the lint commit's gocyclo directive with
gofmt's `//` separator, hence 5 insertions vs the original's 4). Code
files identical to the originals by diffstat (94/12, 83/23, 26/3). Each
message kept plus a provenance paragraph; author and signoff Erik; no
attribution. Running on `dca16de409`: core, dagql (60 s) and core/schema
(120 s) once (/tmp/pkg-a2-followups-tests.head written first;
/tmp/pkg-a2-followups-tests.log, -schema.log) and lint-all
(/tmp/pkg-a2-followups-lint.head, .log).

Results on `dca16de409` (head files `dca16de409 dirty=0` written before
the runs): `ok core 11.536s`, `ok dagql 2.535s`, `ok core/schema
51.913s`; 1070 top-level PASS (917 + 153), 0 FAIL, 1 SKIP line (the
inherited nested last-waiter case); `--- PASS:
TestImportedBackingSnapshotIsOwnedByItsRow (0.00s)`. Lint-all:
`golangci-lint:lint-all DONE [1m34s]`, 0 findings. Sent to the reviewer;
coordinator copied with the proposed description sentence.

#### #14228 follow-ups pushed; A4 cut started

Reviewer approved `dca16de409` (no findings; the three production
patches match their originals; the test file is cfa148371c's minus the
two fixture tests and their unused imports). Move-rule check before the
push: trial merge of `dca16de409` into #14229's head `e8846990e9`
(scratch worktree): clean, 10 files, `go build ./...` and `go vet
./core/` ok, so #14229 stays. Pushed with
`--force-with-lease=sipsma/remote-cache-value-transfer:c7dc73fbf7`
(fast-forward `c7dc73fbf7..dca16de409`); #14228 head `dca16de409`. The
approved sentence appended to the "How it fits main" paragraph by REST
PATCH (/tmp/pkg-a2-patch2.json); read back verbatim
(/tmp/pkg-a2-pr-body.live.md).

A4 (`pkg/a4`, worktree /tmp/pkg-a4): packaged `b5-offers`
`a760512501..abb3ce9750` (18 commits) rebased onto A3's published tip
`e8846990e9`. One conflict, in dagql/cache_value_transfer_test.go at
"test: cover offer admission, settlement, resources and shutdown"
(`eb2031eb09`): the revisionHook test hook the scan fix carried into A3
(`279425d3d2`'s provenance line) is already present; kept once (one
`revisionHook func()` declaration, the `unready` lines alongside), as
foreseen. Reconciliations with A3's lint commit, folded into the A4
commits that introduce the references: `lifetimeSyncFailure` →
`errLifetimeSync` in dagql/cache_offer_matrix_test.go
("test: cover offer admission…"), and a `partTestEquivalent` call without
the removed context parameter (its introducing commit being located).
Main-owned vet finding recorded, not fixed:
engine/server/session_attachables.go:211 (main's `74c2889afc`, #13108)
discards a WithTimeoutCause cancel. LLM files: none touched.
Batch-5 correction `e844245c8b` corrects `cc0f894a11` ("dagql: fetch
offered chains through one content source with renewal", which clones
the offer's address map) and goes directly above its candidate.

#### CI triage after the #14228 push and the #14229 publication

#14228 `dca16de409`: golang:test-all fail (trace
`8fb241c41033f957a1ffa6b660ec51de`; /tmp/pkg-ci-14228-golang_test-all.log):
e2e/helm `--- FAIL: TestPackageDryRun`, `--- FAIL: TestCustomProbes`, k3s
etcd-client retry warnings throughout, the helm e2e infrastructure pattern
seen on #14043/#14049/#14219; helm:assert-template fail (trace
`10338d971d179895aac2b71734a6fd7b`): "list tags for
cgr.dev/chainguard/wolfi-base: unexpected status 500 … Rate" (registry
rate limit). Both at about 23:23Z; one rerun each after an hour (no
approval on #14228, so not sooner). 9 checks pending, test-interface
among them.

#14229 `e8846990e9`: test-split:test-interface fail (trace
`9c5d4f45cf6166c38b944d254de3a76f`; TestIface/returnCustomObj in the
three SDKs, "could not find object or interface type for Impl"): with
the investigator at Erik's request, under investigation, not rerun; the
investigator's working hypothesis is main's `7e2bb23d32` (interface-field
handles → attached objects) combined with the stack. test-split:test-base
fail (trace `ce27389098a65b16a3f09da4f602b11b`;
/tmp/pkg-ci-14229-test-split_test-base.log): core/integration
`--- FAIL: TestGit/TestCrossSessionGitRepositoryIdentity`
(cross_session_test.go:572, `Should not be: "EhAI8d8CEgoKBkdpdFJlZhgB"`)
and `--- FAIL:
TestSecret/TestCrossSessionGitAuthScoping/git_module_source/ssh_key`
(cross_session_test.go:507, unexpected error). Both concern git ref
identity across sessions, which A3's lazy git trees change; test-base is
green on #14224. One dev-engine run of exactly those two tests on
`e8846990e9` started (/tmp/pkg-14229-testbase-repro.head written first;
/tmp/pkg-14229-testbase-repro.log); evidence to the coordinator before
any change.

#### A4 lint step

Lint-all on the A4 tip (tree of `7ec3c85c87`; /tmp/pkg-a4-lint1.head,
/tmp/pkg-a4-lint1.log): `golangci-lint:lint-all ERROR [1m43s]`, 18
findings (/tmp/pkg-a4-lint1.findings), all in A4's own files: 14
nakedret in `(*Cache).offerPart` (dagql/cache_offer.go), gocyclo 46 for
the same function, 2 bodyclose in dagql/cache_part_content.go
(`partHTTPReader.open` :458 and `request` :502), 1 unparam
(`RemoteCacheBridge.finish` result unused). Applied in the working tree:
explicit `return out` at the 14 sites; `finish` returns nothing
(`finishLocked` keeps its bool, used at :251); `//nolint:gocyclo` on
`offerPart` with the reason (offer admission validating and publishing
under the graph and gate locks in one sequence). bodyclose: the reader
hands `resp.Body` to its stream wrapper, closed by `stream.close` and a
context AfterFunc; a same-function close would defeat the streaming
reader; main carries the same situation with a directive
(internal/cloud/otlp.go:454). Ruling asked.

Rulings (coordinator): bodyclose directive in main's form with the reason
(as internal/cloud/otlp.go:454); allowed-directive list is now: gocyclo
(classifiers/validators/state machines, one-line reason, where main
carries the same) and bodyclose for streaming readers that hand the body
to a closer. A4 lint commit `7f52999a3e` (20 commits on `e8846990e9`):
14 explicit returns in `offerPart`, its gocyclo directive, `finish`
without the unused bool, two bodyclose directives. Lint-all
(/tmp/pkg-a4-lint2.head) and tip check (/tmp/pkg-a4-tests1.head; core,
dagql, engine/server 60 s, core/schema 120 s, core/integration vet)
running on it.

#14229 test-interface: cause localized by the investigator to A3's
`7e2bb23d32` (interface fields retained as attached object results
instead of ID strings), which activates a wrong-schema branch in
`InterfaceType.ConvertFromSDKResult` so an implementation from the
caller's module is looked up in the interface module's dependencies.
Recorded as an A3 regression; fix pending as a follow-up commit on
#14229 (investigator's branch off `e8846990e9`, unit regression, review
first). A4 continues on `e8846990e9` and moves onto the follow-up when it
is pushed (its code builds on A3's).

#14229 test-base repro (dev engine, one invocation of exactly the two
tests on `e8846990e9`; /tmp/pkg-14229-testbase-repro.head
`e8846990e9 dirty=0`, log /tmp/pkg-14229-testbase-repro.log, trace
`b0279bffe7ac574d64132ba12e6f4a22`): both reproduce, `✘ 2 failed`, `✔ 3
passed` (the other subtests of the two parents), exit=1;
`--- FAIL: TestGit/TestCrossSessionGitRepositoryIdentity` at :572
(`Should not be: "Eg8I0iISCgoGR2l0UmVmGAE="`) and `--- FAIL:
TestSecret/TestCrossSessionGitAuthScoping/git_module_source/ssh_key` at
:507 ("git authentication failed: SSH URLs are not supported without an
SSH socket" while c2, the client with the socket, loads the ssh module
dependency: `GitRef.tree` → `Directory.exists(path: "top-level")` ERROR).
Analysis sent to the coordinator (below in this record's next entry).

#### #14229 test-base: analysis and rulings

Analysis sent to the coordinator (going to Erik as a design question):
both failures trace to A3's "git: construct lazy outputs from resolved
calls" (original `4d77b28df7`, candidate `0b9a8dbe0f` on #14229).
`GitRepository.ref(name)` still resolves the name through the calling
client's workspace lock but then selects the persistable
`__resolvedRef(name, commit)`, identified by repository plus the resolved
pair, so two sessions resolving "main" to the same SHA receive one
handle; main's identity test (`57addd3ade`) asserts per-client handles
for named refs. The SSH case fails inside the lazy `GitRef.tree` body
(`Directory.exists` demand) at core/git_remote.go:237 because the
GitRef's backend carries no SSH socket; the hypothesis consistent with
the trace is that c1's socket-less `__resolvedRef` result is shared with
c2 (Query.git results are shared across clients since `57addd3ade`), not
yet established which client built it. Recorded in the #14229 entry as
A3 regressions from `0b9a8dbe0f`, design decision pending with Erik; the
investigator takes the evidence task (which client created the shared
result). Erik's escalation rule: an A3 regression whose fix needs a
design decision stops at evidence and analysis, sent to the coordinator
for Erik; no design picked here or by the investigator.

#14228 `dca16de409` test-interface control (investigator):
TestInterface/TestIfaceBasic/go PASS including returnCustomObj
(/tmp/iface-14228-dca-control-full.log:6225,6472, trace
`581824780e3938277c46ee954aa9de31`); #14229 `e8846990e9` same case FAIL
(trace `34049a5fdfbf70e812cece5e4245705d`). The test-interface failure
starts at A3, cause `7e2bb23d32`, follow-up in preparation.

#### A4: native admission probe, final tip

Tip check on `7f52999a3e` (/tmp/pkg-a4-tests1.head; core, dagql,
engine/server 60 s, core/schema 120 s; core/integration vet only):
`ok dagql 14.343s`, `ok engine/server 4.958s`, `ok core/schema 13.948s`,
`FAIL core 28.084s`: `--- FAIL: TestOfferPartsNativeAdmission` at
part_offer_admission_test.go:95 and :116 ("failed to mount …
Options:[rbind ro ro]"), a read-only bind-mount test introduced by A4's
"test: cover offer admission, settlement, resources and shutdown"; 1226
top-level PASS otherwise, `--- PASS: TestRenewalChainControls (4.11s)`.
Coordinator's ruling: probe shape (surviving-design test kept behind
`testutil.RequireNativeMount`). Adaptation commit `4bacf344d0` "test:
probe read-only bind mount privileges before native offer admission"
placed below the lint commit (re-applied as `86f23052e8`; tree identical
to probe-on-lint). A4 tip `86f23052e8`, 21 commits on `e8846990e9`.
Running: core once (/tmp/pkg-a4-tests2.head) and lint-all
(/tmp/pkg-a4-lint3.head) on it.

#### A4 candidate `86f23052e8`: results, review, description draft

Core once on `86f23052e8` (/tmp/pkg-a4-tests2.head `86f23052e8 dirty=0`):
`ok core 21.305s`, 559 top-level PASS, 0 FAIL, 3 SKIP lines
(TestLazyEvaluatedFilesystemClones, TestOfferPartsNativeAdmission,
TestValueTransferPartsSelectedChain, all probed). Lint-all
(/tmp/pkg-a4-lint3.head): `golangci-lint:lint-all DONE [48.4s]`, 0
findings. Per-package counts from the `7f52999a3e` run (identical trees
for those packages): dagql + engine/server 618 PASS, 0 FAIL, 1 inherited
nested SKIP; core/schema 166 PASS, 0 FAIL, 2 SKIP. Candidate total: 1343
top-level PASS, 0 FAIL, 5 probed top-level SKIP + 1 nested. Map
/tmp/pkg-map-a4.txt: 18 mapped, 0 unmapped, 3 new (`d3f4571fbc`
correction, `4bacf344d0` probe, `86f23052e8` lint). 21/21 signoffs, no
attribution, no LLM files. Sent to the reviewer; description draft
/tmp/pkg-a4-pr-body.md to the coordinator with title proposal "remote
cache: live part offers and renewal". Publication waits for the reviewer
and for #14229's test-interface follow-up (A4 moves onto it first).

Correction (reviewer): the "1226 top-level PASS otherwise" line for the
`7f52999a3e` tip check is wrong; the raw tests1 log has 1177 (core 559,
dagql 463, engine/server 155) and the schema log 166: 1343 across those
logs. The final applicable evidence for the candidate is 1343 PASS, 0
FAIL, 6 SKIP lines, all unexecuted (core's initial mount failure recorded
separately above).

Reviewer's A4 verdict on `86f23052e8`: changes required, two placement
findings under Erik's main-defect rule: B1, candidate `327c80123b`
(source 7893a8022a "server: return shutdown errors from GracefulStop"):
returning the pre-existing error accumulator fixes main independently
(both `0d031c08ef` and upstream/main `4056f4a8b2` discard it; the source
message calls it pre-existing) → the accumulator return, its
earlier-shutdown-errors regression and the general documentation to a
small main PR; CloseWithShutdownError and the adapter stop/error
propagation stay in A4. B2, candidate `bcd95269fd` (source e81a03ae61
"server: let the shutdown closing goroutine finish after a timeout"):
buffering doneClosingCh fixes a goroutine leak present on both main refs
→ a standalone main shutdown fix, possibly the same main PR as B1.
Everything else clear (18 mapped, 3 additions, revisionHook once, lint
behavior-preserving, 21 signoffs). Ruling asked of the coordinator.

#### #14229 follow-up pushed by the coordinator; #14228 test-modules; main shutdown PR; A4 rework

#14229: the reviewer approved the interface fix `7b5d35903a`
(fix/module-interface-field-dependencies, two files, 19 lines, unit
regression FAIL-before/PASS-after, Go engine case PASS, trace
`4403b7d49a094ecba9412ac0ee6d4168`); the coordinator pushed it
(fast-forward from `e8846990e9`, lease held), head `7b5d35903a`, 56
commits. Recorded as the A3 regression fix (cause `7e2bb23d32`). One
sentence appended to #14229's "How it fits main" paragraph by REST PATCH
(/tmp/pkg-a3-patch2.json), read back verbatim.

#14228 `dca16de409` test-split:test-modules fail (trace
`dfd7a9056c4c27b23ab3912ec7d3564e`; /tmp/pkg-ci-14228-test-modules.log):
`--- FAIL: TestModuleConfig/TestDaggerGitRefs/SSH_Private_GitLab/root_module`
(module_config_test.go:849, "exit code: 1" from the nested
`dagger core module-source --ref-string ssh://gitlab.com/… as-string`;
the CLI's own stderr is not in the engine log). Statuses by commit:
test-modules success on `c3f7dc33f6` (22:24Z) and on #14228's previous
head `c7dc73fbf7` (22:22Z); error on `dca16de409` (23:29Z), the first
run with the follow-ups, which add `EnsureBackingSnapshot(ctx,
repo.Mirror)` at core/git_remote.go:599 inside `initRemote` (used by
`mount` for every scheme) and the mirror lock field. The SSH GitLab key
is a CI secret (base64 private key), so no local repro. Coordinator's
ruling: (c) read the follow-ups for an SSH-specific difference (in
progress) and (b) one rerun of test-modules on `dca16de409` as a second
data point, not a flake claim: issued
(`dagger cloud -W github.com/dagger/dagger@dca16de409 rerun --check
test-split:test-modules`).

Main shutdown PR (coordinator's ruling on the reviewer's A4 findings B1
and B2): branch `sipsma/engine-server-graceful-stop-errors` off
upstream/main `4056f4a8b2`, worktree /tmp/pkg-main-gs, two commits:
`a6d1d3637a` "server: return shutdown errors from GracefulStop" (main
side of 7893a8022a: the two final returns join the accumulator, the
FIXME and its nolint go; regression
TestGracefulStopReturnsEarlierShutdownErrors with its helper in the new
engine/server/graceful_stop_test.go; one doc sentence in
internal-docs/cache_persistence.md) and `e2bda9adfa` "server: let the
shutdown closing goroutine finish after a timeout" (e81a03ae61 whole);
messages kept plus provenance. engine/server once and lint-all running
(/tmp/pkg-main-gs-tests.head, -lint.head). The coordinator publishes it.

A4 rework: `pkg/a4-before-mainsplit` = `86f23052e8`. Interactive rebase:
`327c80123b` edited to keep the integration's stop error, its
propagation into the final returns (`errors.Join(adapterStopErr,
dbCloseErr)` / `(adapterStopErr, ctx.Err())`) and
CloseWithShutdownError, restoring main's FIXME/nolint block; message
notes the split. `bcd95269fd` dropped. Tip `731858e798` on `e8846990e9`
(`pkg/a4-on-e884`), 20 commits; then moved onto #14229's new head
`7b5d35903a`. Corrections table: `7893a8022a` → split (main PR +
A4); `e81a03ae61` → main PR.

Main shutdown PR results (head files `e2bda9adfa dirty=0` written before
the runs): `go test -count=1 -timeout 60s -v ./engine/server/`
(/tmp/pkg-main-gs-tests.log): `ok engine/server 4.094s`, 151 top-level
PASS, 0 FAIL, 0 SKIP, `--- PASS: TestGracefulStopReturnsEarlierShutdownErrors
(2.81s)`; lint-all (/tmp/pkg-main-gs-lint.log): `golangci-lint:lint-all
DONE [1m39s]`, 0 findings; two commits, Erik's signoff, no attribution.
Sent to the reviewer; the coordinator publishes. A4 moved onto
`7b5d35903a`: tip `e121ed5aa7`, 20 commits; range-diff vs the reworked
series on `e8846990e9` (`731858e798`): 20 `=`; map: 17 originals mapped,
`e81a03ae61` (none) = moved to the main PR, 3 new. Touched packages and
lint-all running (/tmp/pkg-a4-tests3.head, /tmp/pkg-a4-lint4.head).
(c) finding for #14228's SSH GitLab failure reported: no SSH-specific
difference in the follow-ups' diffs; the only new git path is the shared
mirror's backing-snapshot ensure and owner-lease sync before auth setup,
for every scheme (core/git_remote.go:599, core/backing_snapshot.go:45-67).

### Main PR #14231 `sipsma/engine-server-graceful-stop-errors`

Reviewer approved `e2bda9adfa` (approve; nonblocking provenance
correction: the regression and its helper come from `eb2031eb09`, not
`7893a8022a`); I applied it as a message-only amend (`859755962e`,
`ddd065070d`, trees identical, tag `pkg/main-gs-reviewed` =
`e2bda9adfa`). The coordinator re-messaged both commits for main's
readers and published: #14231 at `dce5557471` on main `4056f4a8b2`
(`04cce2201e` accumulator return + regression + doc, `dce5557471`
buffered channel; trees identical to the approved pair,
reviewer-confirmed). #14231 is under the CI watch and merge rule (plain
merge, not a stack member). Rule for every future main PR prepared here:
commit messages for main carry no workstream vocabulary (batch,
predecessor, round, rule item numbers, decision IDs); say what changed
and why, and name the originating remote-cache commit hash in one
sentence at the end.

#### A4: regression case moved with the accumulator

On `e121ed5aa7` (moved onto `7b5d35903a`): lint-all `DONE [1m31s]`, 0
findings; core, dagql, core/schema ok (1176 + 166 top-level PASS, 6 SKIP
lines); engine/server FAIL: exactly
`TestRemoteCacheGracefulStop/earlier_shutdown_errors_are_returned`, the
case that moved to #14231. The coverage commit
("test: cover offer admission, settlement, resources and shutdown")
edited to drop that case and its failing network provider (message notes
it; `pkg/a4-before-testtrim` = `e121ed5aa7`); tip `13dd5e3adf`, 20
commits on `7b5d35903a`. engine/server once and lint-all running on it
(/tmp/pkg-a4-tests4.head, /tmp/pkg-a4-lint5.head). Draft's "How it fits
main" sentence names #14231.

#### A4 candidate `15dfcb81d7`

`ddd065070d` dropped; /tmp/pkg-main-gs reset to #14231's `dce5557471`.
A4: engine/server once on `13dd5e3adf` (/tmp/pkg-a4-tests4.head
`13dd5e3adf dirty=0`): `ok engine/server 3.795s`, 155 top-level PASS, 0
FAIL, `--- PASS: TestRemoteCacheGracefulStop (2.49s)`; lint-all on it
(/tmp/pkg-a4-lint5.head): `golangci-lint:lint-all DONE [24.3s]`, 0
findings. Coverage commit's note reworded per the coordinator (the case
lives in #14231 and passes on the stack once #14231 is in its base):
message-only, tip `15dfcb81d7`, `git diff 13dd5e3adf 15dfcb81d7` empty,
range-diff 19 `=` + 1 message-only `!`. Map: 17 mapped, `e81a03ae61` →
#14231, 3 new. 20/20 signoffs, no attribution. Sent to the reviewer.
Draft names #14231; its bullets no longer claim the two main-side fixes.

Reviewer approved `15dfcb81d7` (B1/B2 resolved) with two nonblocking
wording corrections, no rerun needed: (1) A4's doc sentence in
internal-docs/cache_persistence.md claimed all collected shutdown errors
are returned, which holds only once #14231 is in the base; the sentence
in the docs commit now reads "`GracefulStop` joins the integration's stop
error into what it returns, together with the final database close
result." (`pkg/a4-approved-15df` = `15dfcb81d7`; the docs commit amended
in place, doc file only). (2) The count in the `e121ed5aa7` entry above
is corrected: tests3 has core 559 + dagql 463 = 1022 (its engine/server
run, 154 passes, is superseded by the failing case's removal); the
applicable evidence for the candidate is 1022 + core/schema 166 +
engine/server 155 (tests4) = 1343 top-level PASS, 0 FAIL, 6 SKIP lines
(unexecuted); the earlier engine/server failure stays recorded above.

Correction to the entry above: the docs commit amend is undone (`pkg/a4`
reset to the approved `15dfcb81d7`); the doc wording is a doc-only
follow-up commit `82f2e8487e` "docs: claim only the integration's stop
error for GracefulStop" at the tip (21 commits on `7b5d35903a`), sent to
the reviewer for acknowledgement per the coordinator; no rerun.

CI: #14228 test-modules rerun on `dca16de409`: pass (single occurrence,
watched). #14229 `7b5d35903a`: test-interface pass (the A3 regression
fix holds in CI), test-modules, golang:test-all, helm pass; test-base
pending. #14228 test-base: "Cancelled - max execution time exceeded"
(trace `c3131529a42b7fe96b102342a92d8f02`;
/tmp/pkg-ci-14228-test-base.log): nested engine containers exited 137
from 23:27:30Z / 23:30:52Z, then only metric-export lines until the
30-minute cutoff (23:52Z); no test result lines; green on `c7dc73fbf7`
and `c3f7dc33f6`. Rerun proposed as a second data point.

### A4 published: #14233 `sipsma/remote-cache-live-part-offers`

Reviewer acknowledged the doc-only follow-up `82f2e8487e` (approval
stands). Pushed to upstream `sipsma/remote-cache-live-part-offers` (new
branch); `gh stack link 13937 sipsma/remote-cache-live-part-offers`
created #14233 (base `sipsma/remote-cache-lazy-part-acquisition`) as a
draft; title and approved body (with the #14231 sentence) set by REST
PATCH (/tmp/pkg-a4-patch.json); `gh pr ready`. Stack API: id 487371, 16
members, #14233 at position 16 above #14229 (15). Read back: head
`82f2e8487e`, base `sipsma/remote-cache-lazy-part-acquisition`, head
repo dagger/dagger, draft false, body verbatim (trailing newline only;
/tmp/pkg-a4-pr-body.live.md). The agreed test-base rerun on #14228's
`dca16de409` issued (second data point; the nested engine's SIGKILL with
no test output is runner behavior until shown otherwise). CI watch
re-armed with #14233. Investigator's git evidence report for Erik:
/tmp/git-scoping-evidence.md (named refs share `__resolvedRef` rows on
`e8846990e9`; the SSH failing pair does not share one: c2 evaluates c1's
socket-less lazy Directory published under the same content digest
xxh3:ab061a4655b0a931; a per-client resolved ref alone cannot fix SSH;
options without recommendation; trace `88d5735a0f6ce0c780ccfdddc9b901cc`).

#### #14229 naming-site correction; A5 cut started

A5's first rebase conflict (CommitReadyPart) exposed a mislabel in
#14229's naming commit `1139dead6f`: dagql/cache_part_install.go:420, the
refusal when a source's recorded dependency is not among the prepared
ones, got e094252906's sessionless-share name "commit: donated facts
changed" through the naming script's last fallback (bare-line match, no
context); the original names it "commit: dependency not held". Site
check: the other 41 applied sites match their conditions; this was the
only fallback match. Coordinator: go. Follow-up `26fc6aa5fe` "dagql: name
the missing-dependency refusal as the original does" on pkg/a3 above
`7b5d35903a`; dagql once (head file `26fc6aa5fe dirty=0`): `ok dagql
5.679s`, 438 top-level PASS, 0 FAIL, 1 inherited nested SKIP. With the
reviewer; push with lease on approval; A5 then moves onto it.

A5 (`pkg/a5`, worktree /tmp/pkg-a5): packaged `b6-sharing`
`abb3ce9750..38583498cf` (25 commits) rebasing onto A4's published tip
`82f2e8487e`. Conflicts so far, all in the sites A3's naming commit
renamed: (1) "dagql: validate a sessionless share per donated address"
splits the combined donor check into three; resolved with the
original's names ("commit: donor unregistered", "commit: donated facts
changed", "commit: donor facts changed"); (2) "dagql: add the snapshot
sharing queue, triggers and close admission" inserts
`beginShareNotificationsLocked` after `egraphMu.Lock()` where main's TTL
merge block now sits; both kept, notification after the block; (3)
"dagql: install a share through ordered preparation and a release
barrier" rewrites the receiver-representation check; resolved with the
original's name ("commit: receiver representation").

#### A5: rebase finished; corrections above the watch commit

`pkg/a5` rebased onto `82f2e8487e`: 25 commits, four conflicts (above).
Reconciliations with A3's lint commit folded into the sharing commits
that introduce the references, at rebase edit stops with `go vet
./dagql/` at each: two `walkTransferPayloads` calls without the removed
path parameter ("dagql, core, engine: probe, select and bound a sharing
preparation"); `partTestEquivalent` calls without the removed context
parameter in cache_snapshot_sharing_test.go (five commits: "verify
snapshot sharing in process", "docs: record the batch 6 implementation",
"core/integration: bound the restarted read", "give each sharing slot
outcome one reporter", "keep the donor held between a decoded receiver's
passes"). A first attempt's sed also stripped the cache argument from
already-fixed calls; redone from `pkg/a5-rebased-raw` with the exact
five-argument pattern; every stop vets clean. Tip before corrections
`48cad9eab7` (`pkg/a5-before-corrections`).

Corrections above the watch commit `a8143f01b1` (candidate of
`ccdb016ed6` "dagql: warn when a part reselect loop stops making
progress"), built in a scratch worktree: `d3731af842` = e094252906's
watch part (watch wired into the loops, the watch's own changes and
test, and the names of the sites this series adds:
"sessionless source: row expired", "sessionless source: donor does not
own the snapshot", "share: slot ended without installing"; the one
conflict was the mislabeled site, kept as A3 has it since #14229's
follow-up owns the rename); `bf8cee1b2b` = f9db98a420 whole;
`e8d997530b` = af2ddb4e36 whole (its progress test's helper call folded
to the current signature). Messages kept plus provenance; Erik's
signoff. dagql once at the chain tip (head file `e8d997530b dirty=0`):
`ok dagql 11.601s`, 509 top-level PASS, 0 FAIL, 1 inherited nested SKIP;
`--- PASS: TestPartProgressRule`, `TestPartVersionRefusal`,
`TestPartReselectWatch`, `TestPartRefusalNamesItsSite`.

#### A5 lint step; CI after the rename push

Lint-all on `39c9916ac2` (/tmp/pkg-a5-lint1.head, .log):
`golangci-lint:lint-all ERROR [1m45s]`, 5 findings
(/tmp/pkg-a5-lint1.findings), all in A5's files: gocyclo 34
`prepareReadyPartFromBase`, ST1016 receiver in
`PartDemandState.refused` (from the progress-rule correction), dogsled ×2
in the sharing test, QF1011 in engine/server's sharing test. Early test
pass on `39c9916ac2` (/tmp/pkg-a5-tests0.head): core, dagql, engine,
engine/server, core/schema all `ok`; 1403 top-level PASS, 0 FAIL, 6
SKIP lines. Lint commit `b8bc41cfa0` (29 commits on `82f2e8487e`):
`readyPartPreparationBase.recordFor` extracted (real fix), receiver
rename, a `shareTestEnv` fixture struct for the two cache-only sites,
the redundant view type dropped. Lint-all and tip check running on it
(/tmp/pkg-a5-lint2.head, /tmp/pkg-a5-tests1.head).

CI: #14228 `dca16de409`: test-base rerun pass (second data point;
single occurrence stays recorded), test-modules rerun pass; golang:test-all
and helm:assert-template reruns issued after the hour (00:23Z rule).
#14229 `26fc6aa5fe`: test-split:test-call-and-shell fail (trace
`18efa5ff6d5b601011e84a8df483485d`): `--- FAIL: TestCall/TestErrNoModule`
(module_call_test.go:1600, "persist state not ready: typed output
changed during capture"); the check passed on `7b5d35903a` and on
#14233's `82f2e8487e`; intermittent; the capture guard escaping to a CLI
call; reported, no rerun. #14233 `82f2e8487e` test-base fail (trace
`443c74e245d70a05daa20fca69a5bbd5`): the two A3 git regressions
inherited on A4's line (Erik ruled: behavioral assertions for the
identity test, credential-scoped pending recipes for SSH lazy trees;
investigator implements both as #14229 follow-ups; nothing for me), plus
dagql `TestPartReadyPreparationBoundaries/missing-local-descriptor`
(cache_part_boundary_test.go:134, receiver incomingOwnershipCount
expected 2 actual 3), from A3's "cache: cover acquisition decision and
publication boundaries" (introducing PR #14229); locally on A4's tip
20/20 PASS without -race (/tmp/pkg-a4-boundary20.log); -count=20 -race
run in progress (/tmp/pkg-a4-boundary-race.head). #14231 86/86 pass, no
approval.

#### Boundary count (#14233 test-base, dagql) reading

`TestPartReadyPreparationBoundaries/missing-local-descriptor`
(introducing PR #14229): locally on `82f2e8487e` (head files first, tree
clean) 20/20 PASS plain (`ok dagql 2.736s`) and 20/20 PASS under `-race`
with zero race reports (`ok dagql 7.433s`). The count is the receiver's
incomingOwnershipCount read right after RunLazyTask returns; the
preparation's own hold is released synchronously in PrepareReadyPart's
deferred Release (cache_part_install.go:80-111), but the attempt runs in
runLazyTask's goroutine (dagql/cache.go:185) whose deferred
`releasePartRow` (:191-192) drops the attempt's row hold after the body
completes, and RunLazyTask returns on the body's completion, so the read
can precede that release by one hold under load. Timing-dependent test
determinism problem, not a leak; follow-up on #14229; shape asked of the
coordinator (wait for retirement in the test, or RunLazyTask returning
after the holds drop). Answered the investigator's API question (no
after-evaluation content-digest hook on PartHost; the teach entry points
take the result: WithContentDigest/WithContentDigestAny, TeachContentDigest,
TeachCallEquivalentToResult). The #14229 capture-guard escape is the
analyst's commission (tc-17201bd113f0e6698af2b36ffa07dd2d); no rerun.

### A5 published: #14235 `sipsma/remote-cache-snapshot-sharing`

Reviewer approved `b8bc41cfa0` (no blocking findings; should-fix: the map
lacked the lint commit line, appended; note: on the eventual stack move
dagql/cache_part_install.go:585 must carry #14229's `26fc6aa5fe` name
"commit: dependency not held", not the inherited label). Coordinator
approved the description as drafted, title "remote cache: snapshot
sharing between rows". Pushed to upstream
`sipsma/remote-cache-snapshot-sharing` (new branch); `gh stack link
13937` created #14235 (base `sipsma/remote-cache-live-part-offers`) as a
draft; title and body by REST PATCH (/tmp/pkg-a5-patch.json); `gh pr
ready`. Stack API: id 487371, 17 members, #14235 at position 17 above
#14233. Read back: head `b8bc41cfa0`, base
`sipsma/remote-cache-live-part-offers`, head repo dagger/dagger, draft
false, body verbatim (trailing newline only; /tmp/pkg-a5-pr-body.live.md).

#14229 boundary-test follow-up (coordinator's ruling: the test, not
RunLazyTask; deterministic wait, no polling): cache_part_boundary_test.go
now sets the cache's existing `testBeforeSessionOperationExit` hook
(dagql/cache.go:286, called in `cacheOperation.finish`, which the attempt
goroutine reaches after its deferred `releasePartRow`) to signal a
channel, and reads the counts only once both are back at their pre-task
values, consuming one exit event per re-check, with a 10 s failure
bound. `-count=20 -race` run in progress on `26fc6aa5fe` + the change
(/tmp/pkg-14229-boundary-race.head).

#### #14229 boundary follow-up: the predecessor's two commits

The verification series carries this fix: `404936e21c` "dagql: count
ownership after the attempt's release, and never hold the lock across an
assertion" (three tests read incomingOwnershipCount straight after
RunLazyTask; counts copied under egraphMu, released before asserting) and
`2147ea5c09` "dagql: wait for the attempt's row release through a hook,
not by polling" (nil-checked `testAfterLazyAttemptReleased` hook on the
attempt's goroutine after releasePartRow; arm/wait helpers, ten-second
bound; the boundary Body returns errors). Coordinator: go; my
hand-written wait dropped. On pkg/a3 above `26fc6aa5fe`: `c41f511906`,
`77ebab112d` (messages kept plus provenance; applied cleanly). Run (head
file `77ebab112d dirty=0`): `go test -count=20 -race -timeout 120s -run
'TestPartReadyPreparationBoundaries$|TestPartReadyRevalidationAndCanceledFinish$|TestPartSessionlessOwnSubset$'
-v ./dagql/`: `ok dagql 13.678s`, 60/60 PASS, 0 FAIL, 0 races
(/tmp/pkg-14229-boundary-race.log). With the reviewer; the coordinator
sequences the push above the investigator's git pair. Corrections table:
`404936e21c`, `2147ea5c09` → #14229 follow-ups (test determinism).

Scan of b7-verification for corrections the rule sends to A0–A5 (method:
each unplaced commit by the pre-b7 files it modifies; non-test code left
is fixture, transport, reports, TLA registrations, plumbing → A6). Test
files of earlier series, sent to the coordinator for rulings: A3
`452ea00673`, `3636d8acb6`, `10279e7758`, `110a3db4e8` (the four probed
tests rewritten to run unprivileged), `e8277963a5` mark / `76311056c7`
delete of the four git tests (delete needs A6's TestGitTrees),
`4927844037` (edits a file A3 keeps deleted); A1 `73a7917e05`,
`c591ea43ac` (umask controls in a child process, lazy_operation_execution_test.go);
A5 `4767b31208`, `cbea2f664f`; `c68022468c`/`2bf9621abf` cancel;
`1af01b8dc7`, `bc905aed16`, `4c4a988ede`, `4a4ccd9f37` fixture-side → A6.

#### Rulings on the verification scan; #14229 bundle

Coordinator's rulings: the four unprivileged rewrites (`452ea00673`,
`3636d8acb6`, `10279e7758`, `110a3db4e8`) → #14229 follow-ups replacing
the probes in those four tests (probe helper stays for A4's admission
test), done when each prints PASS unprivileged; git-test mark
`e8277963a5` and delete `76311056c7` → A6 as one move with native
TestGitTrees; `4927844037` nothing to apply; A1 umask pair `73a7917e05`,
`c591ea43ac` → #14224 follow-ups; A5 pair `4767b31208`, `cbea2f664f` →
#14235 follow-ups; `c68022468c`/`2bf9621abf` cancel; `1af01b8dc7`, the
cleanup bounds `bc905aed16` and the engine_test.go debug commits → A6.
Order: #14229 bundle (reviewer, coordinator pushes after the
investigator's SSH commit), #14224 pair, #14235 pair, then A6.

Reviewer approved the boundary pair `c41f511906` / `77ebab112d`. Bundle on
pkg/a3 above them: the four rewrites cherry-picked cleanly, each amended
to drop its `testutil.RequireNativeMount(t)` line (provenance paragraph
says so). First run: three PASS unprivileged, TestValueTransferPartsSelectedChain
FAIL at value_transfer_chain_test.go:81 (File.Contents mounts). Cause: the
predecessor's `bbbe792279` hunk for this file (demandedFileContents in
place of Contents) was never carried, the test having been dropped in A2
and revived in A3 behind the probe; folded into the selected-chain
rewrite commit with a provenance note.

Bundle results (head file `c077d7dfdc dirty=0`): the four unprivileged:
`--- PASS: TestLazyEvaluatedFilesystemClones (0.07s)`, `--- PASS:
TestValueTransferPartsSelectedChain (0.24s)`, `--- PASS:
TestBuiltinMetadataSelectors (0.47s)`, `--- PASS:
TestLazyStoredResultsWithoutBacking (0.06s)`; core once `ok core
23.304s`, 561 PASS, 0 FAIL, 0 SKIP; core/schema once (tree identical
before the fold) `ok core/schema 15.370s`, 168 PASS, 0 SKIP. Bundle
commits: `95e3360987`, `aacb2d2510` (with bbbe792279's hunk),
`ff3126387c`, `c077d7dfdc`, above `c41f511906`, `77ebab112d`. With the
reviewer; the coordinator pushes after the investigator's SSH commit.
Corrections table: `452ea00673`, `3636d8acb6` (+ bbbe792279's
value_transfer_chain_test.go hunk), `10279e7758`, `110a3db4e8` → #14229
follow-ups replacing the probes.

#### #14224 umask pair; #14235 pair; CI reads

#14224: `c15cec7aa6` = 73a7917e05 (applied to A1's pre-rename
core/eager_producer_execution_test.go by hand: fixture helpers and
freshProducerFile/producer names kept, the umask child helper added,
both umask sites converted; the trailing in-process umask call remains
only inside the helper), `e3e75aedb1` = c591ea43ac; messages kept plus
provenance. Core once (head file `e3e75aedb1 dirty=0`): `ok core 9.847s`,
510 PASS, 0 FAIL, 0 SKIP; `--- PASS: TestHTTPProducerWriter/restrictive_umask
(0.13s)`, `--- PASS: TestHTTPProducerWriter/public_eager_layout (0.22s)`.
With the reviewer. #14235: `d491858b5b` = 4767b31208, `e787998fc4` =
cbea2f664f on `b8bc41cfa0`, applied cleanly with provenance; dagql and
engine/server once running (/tmp/pkg-14235-pair-tests.head).
Corrections table: 73a7917e05, c591ea43ac → #14224 follow-ups;
4767b31208, cbea2f664f → #14235 follow-ups.

CI: #14228 golang:test-all second red on `dca16de409` (trace
`bbfd359402c2a83d268766e1732ff06f`): e2e/helm
`--- FAIL: TestInstallK3S/default_daemonset (302.55s)`, engine pod
ErrImagePull, `registry.dagger.io/engine:main` HEAD 500 at 00:25:30Z; the
first red (23:23Z) was the k3s etcd pattern (TestPackageDryRun,
TestCustomProbes); golang:test-all green on 7b5d35903a, dce5557471,
82f2e8487e, 26fc6aa5fe in the window: infrastructure, two windows on one
head; its one rerun spent. #14235 test-split:test-provision (trace
`6cc8c71cd78b8b6689a0565bec26d7cb`): TestImageDriverGarbageCollectEngines
nerdctl/podman, `registry.dagger.io/engine:v0.16.1` HEAD 500, same
registry window; one rerun when an hour old (about 01:25Z).

Reviewer approved the #14224 umask pair; pushed with
`--force-with-lease=sipsma/remote-cache-transfer-foundations:c3f7dc33f6`
(fast-forward `c3f7dc33f6..e3e75aedb1`); #14224 head `e3e75aedb1`, 35
commits; PRs above stay (test-only). Reviewer approved #14229's four-test
bundle `c077d7dfdc` (no blocking findings; the builtin-selector run is
not counted as selected-byte proof). #14229's line at push time
(coordinator pushes): investigator's `746fd6deb8`, `b1841e71fa` →
analyst's capture-guard owner-sync fix `4fe1ca0624` → my boundary pair
and four rewrites rebased on top. Rule refinement (coordinator): a rerun
is spent per documented infrastructure cause, not per check; the two
registry-window reruns (#14228 golang:test-all, #14235 test-provision)
are scheduled at 01:25Z behind a registry probe returning 200
(/tmp/pkg-reruns-0125.sh, log /tmp/pkg-reruns-0125.log).

## #14229 push line, pre-built (coordinator's instruction)

Scratch branch pkg/a3-line (worktree /tmp/pkg-a3-line) from 26fc6aa5fe:
cherry-picks of the investigator's 746fd6deb8, b1841e71fa, 0aaff55cd1
(all approved), the analyst's 4fe1ca0624, c68886903b, 32eb3e17ec (approved;
the evidence commits 93761358c5 and fb64697a8c omitted), then my six
(c41f511906..c077d7dfdc). No conflicts. Tip d41463dfd4, 12 commits, 12 Erik
signoffs, 0 attribution trailers. Range-diff per commit against its source
(/tmp/pkg-a3-line-rangediff.txt): 12 of 12 `=`.

Runs on d41463dfd4, head files written before each run:
- /tmp/pkg-a3-line-tests.{head,log}: `go test -v -count=1 -timeout 60s
  ./core/ ./dagql/` → `ok core 20.396s`, `ok dagql 4.997s`; 1003 PASS, 0
  FAIL, 0 top-level SKIP, one inherited nested SKIP. PASS lines:
  TestPartTaskContentIdentityWaitsForOperationLeaseRelease,
  TestSnapshotOwnerPublicationUsesCoherentRead,
  TestPartHostInlineAllPartsRetriesCapture, TestPartReadyPreparationBoundaries,
  TestPartReadyRevalidationAndCanceledFinish, TestPartSessionlessOwnSubset,
  TestLazyEvaluatedFilesystemClones, TestValueTransferPartsSelectedChain.
- /tmp/pkg-a3-line-schema.{head,log}: `go test -v -count=1 -timeout 120s
  ./core/schema/` → `ok core/schema 13.874s`; 170 PASS, 0 FAIL, 0 SKIP.
  PASS lines: TestGitTreeContentIdentityAfterMaterialization,
  TestBuiltinMetadataSelectors, TestLazyStoredResultsWithoutBacking.
- /tmp/pkg-a3-line-lint.{head,log}: `golangci-lint:lint-all ERROR [1m34s]`,
  findings: 4. core/integration/cross_session_test.go:575:18 and :578:18
  staticcheck SA1019 (GitRef.Commit deprecated; from 746fd6deb8);
  dagql/cache_part_task.go:48:28 staticcheck ST1016 (receiver `task` where
  the type's other method uses `t`; from b1841e71fa);
  dagql/cache_part_boundary_test.go:128:97 errorlint (%v for an error; from
  my 77ebab112d).

Miss recorded: the six-commit bundle (c41f511906..c077d7dfdc) went to the
reviewer without a lint-all run. Rule restated by the coordinator: no
candidate reaches the reviewer without a lint-all terminal line.

Lint commit on top, per the coordinator: 380ab472de "lint: meet main's
golangci-lint configuration on the identity and owner fixes" (three files,
6 insertions, 6 deletions: CommitSHA at the two sites, the receiver renamed
inside SetContentDigestAfterEvaluation only, %w). Erik signoff. Runs on
380ab472de, head files first: /tmp/pkg-a3-line-lintfix-tests.{head,log}
`go test -v -count=1 -timeout 60s ./dagql/` → `ok dagql 4.711s`, 442 PASS,
0 FAIL, 0 top-level SKIP plus one inherited nested SKIP (log line 726,
TestCacheContextCancel/last_waiter_canceled_fn_returns_value_still_releases;
reviewer's correction); `go vet ./core/integration/` clean;
/tmp/pkg-a3-line-lintfix-lint.{head,log} `golangci-lint:lint-all DONE
[1m32s]`, findings: 0. Sent to the reviewer; the coordinator pushes the
13-commit tip.

Reviewer approved 380ab472de (12/12 range-diff equal checked independently;
lint commit scope confirmed).

Pushed by the coordinator: #14229 at 380ab472de (fast-forward from
26fc6aa5fe with lease, 70 commits, no evidence files in the tree). #14229
now carries: the investigator's three (workspace-pin assertions in the
cross-session test; git tree content identity published after authorized
materialization; private identity teaching deferred to lease cleanup), the
analyst's three (coherent snapshot-owner reads across publication; bounded
handoffs in the owner regression test; managed inline discovery capture
retries), my six (boundary pair, four unprivileged rewrites) and the lint
commit. Record for the dagql run on 380ab472de: zero top-level skips, one
unexecuted nested case.

Coordinator's move ruling after this push: A4 (#14233) and A5 (#14235)
build on the changed code (cache.go, cache_part_host.go, cache_part_task.go,
git.go), so both move: A4 onto 380ab472de, A5 onto A4's new head, range-diff
all equal, one push each with lease; the A5 label at
dagql/cache_part_install.go:585 takes the corrected name (26fc6aa5fe's
"commit: dependency not held") during the move. A6 then goes onto A5's new
head.

#14229 description: one paragraph appended at the end of "How it fits main"
(the four follow-up groups), REST PATCH, read back identical apart from
GitHub's trailing newline (/tmp/pkg-14229-body-new.md vs
/tmp/pkg-14229-body-readback.md). Not yet updated there, flagged to the
coordinator: the "Tests kept behind a privilege probe" section and the
validation line "4 SKIP (the four probed tests)" describe the pre-rewrite
state.

A4 move: `git rebase --onto 380ab472de 7b5d35903a pkg/a4` (A4's base was
7b5d35903a, #14229's head before 26fc6aa5fe), no conflicts, 21 commits,
range-diff 7b5d35903a..82f2e8487e vs 380ab472de..39bbd96e7c: 21 of 21 `=`
(/tmp/pkg-a4-move-rangediff.txt). `go build ./...` and vet of core, dagql,
engine/server, core/schema, core/integration clean apart from main's
session_attachables.go:211 lostcancel. First push attempt went to origin
(the fork) and was rejected on the lease; the PR branches live on upstream
(dagger/dagger). Pushed:
`git push --force-with-lease=sipsma/remote-cache-live-part-offers:82f2e8487e
upstream pkg/a4:sipsma/remote-cache-live-part-offers` → forced update
82f2e8487e...39bbd96e7c. #14233 head 39bbd96e7c.

A5 move: `git rebase --onto 39bbd96e7c 82f2e8487e pkg/a5`, one conflict in
ae474ab9a7's import block of dagql/cache_part_host.go (the line below adds
go-digest, the commit adds engine): both kept, goimports grouping. 31
commits, tip 4dad71ad4c. Range-diff 82f2e8487e..e787998fc4 vs
39bbd96e7c..4dad71ad4c (/tmp/pkg-a5-move-rangediff.txt): 29 `=`, 2 `!`:
commit 3 (the import placement) and commit 24, the reselect-watch commit,
whose diff context now shows the analyst's managed-inline retry below it.
The label at dagql/cache_part_install.go:585 reads "commit: dependency not
held" in the moved tree: the rename commit 26fc6aa5fe is below A5 now and
no A5 commit rewrote the line, so the rebase carried it; :545, the
sessionless-share check, keeps "commit: donated facts changed" as intended.

A5 push: `git push --force-with-lease=sipsma/remote-cache-snapshot-sharing:e787998fc4
upstream pkg/a5:sipsma/remote-cache-snapshot-sharing` → forced update
e787998fc4...4dad71ad4c; #14235 head 4dad71ad4c, 31 commits (gh). A6 moved:
`git rebase --onto 4dad71ad4c e787998fc4 pkg/a6`, no conflicts, 72 commits,
tip a9b7935b8f, range-diff 72/72 `=` (/tmp/pkg-a6-move-rangediff.txt).

## CI premise change (top-PR job)

Finding: the transfer suite is not gated in the test process. The only
reads of _DAGGER_TEST_REMOTE_CACHE_FIXTURE_ROOT are in engine/server
(remote_cache_fixture_controller.go:85,95; snapshot_sharing.go:27;
remote_cache_fixture.go:16); core/integration's harness sets the variable
on the nested dev engines it starts (remote_cache_harness_test.go:73) and
nothing in the suite or testctx skips without it. test-base runs ./... with
an explicit skip list that does not name the suite; #14228's passing
test-base log (/tmp/pkg-ci-14228-test-base-pass.log, trace
e517e4a42995894dd9ff1eebac30ac00) shows `ok core/integration 1014.573s`
and carries no per-test lines (the shard runs without -v). So by the code
the suite has been running inside test-base on every published PR. The
sentence "skips in CI unless the root is set, and stays skipped until the
top PR's job sets the root" in six descriptions was my error, inherited
from a misreading of the engine-side gate.

Coordinator's rulings: (1) the six descriptions corrected now; the shard
stays in the top PR with no env parameter, run ^TestRemoteCacheTransferSuite$
on ./core/integration, and the suite added to test-base's exact-name skip
list so it runs once; the benefit is test-base's time budget. (2) tla-check
registered in the default environment, matrix entry runs Quick only, with
comments in checks.yml and dagger.toml; Erik is told, since a local
`dagger check '**'` now includes the multi-hour runs; if he objects it comes
out before merge. (3) the two backing-snapshot fixture cases become an A6
follow-up once the stack sits on #14228's follow-up head; patch held at
/tmp/pkg-backing-two-cases.go (+ the six comment lines at
/tmp/pkg-backing-full.go:171-176), verified on a scratch tree
(/tmp/pkg-a6-backing-scratch.{head,log}: A6's tip plus 2266f481a1,
95a320301e, a4ea34dcec cherry-picked cleanly; the three
TestImportedBackingSnapshot* tests PASS at 60 s). (4) #14229's
"Tests kept behind a privilege probe" section and validation line rewritten.

Descriptions patched by REST (bodies /tmp/pkg-body-<n>-new.md, readbacks
/tmp/pkg-body-<n>-readback.md, all identical): #14220 and #14224 ("One
standing CI gap applies to the whole stack: ... `TestRemoteCacheTransferSuite`
runs inside CI's test-base shard, which starts its nested engines with the
fixture root; the top PR gives it a shard of its own (this PR does not
touch that suite)."), #14228, #14229, #14233, #14235 ("One CI gap applies
to this stack." and the same sentence with each PR's own clause). #14229
additionally: section renamed "Tests that read a native snapshot" and
rewritten to the post-rewrite state (in-place reads through the
unprivileged test store, run unprivileged and in CI; the probe helper stays
for the offers PR's admission test), validation line now "1195 top-level
PASS, 0 FAIL, zero top-level skips and one inherited nested skip" (core 561
and core/schema 170 from the d41463dfd4 runs, packages the lint commit does
not touch; dagql 442 and engine/snapshots 22 run at 380ab472de,
/tmp/pkg-a3-line-snapshots.{head,log}: `ok engine/snapshots 5.444s`, 22
PASS).

## A6 candidate

Base 4dad71ad4c (#14235 head). 75 commits: 71 from b7-verification (98
above 38583498cf minus the 27 already placed), plus four new:
- a9b7935b8f core: test the clone of a part-acquired File or Directory for
  a Container — 1bfece3b77's test file, provenance paragraph; one
  reconciliation, producedFileContents called with the parameter list A3's
  lint commit left (the original passed a context the helper no longer
  takes). PASS on A6 before the commit (/tmp/pkg-a6-clone-test.{head,log}).
- d766655267 lint: meet main's golangci-lint configuration — lint-all on
  a9b7935b8f (/tmp/pkg-a6-lint1.{head,log}): ERROR, findings: 10.
  unparam: git trees test reader's unused client parameter (dropped);
  seal's unread result (returns only the error; two callers). gocyclo:
  runLazyOperationDecision 32 → 26 by moving the seal of the original with
  its three fixture points into beginLazyOriginal; prepareReadyPartFromBase
  32 → 29 by moving the locked dependency hold into holdSourceDependencies;
  runFixtureControl 39, the control dispatcher (12 cases), keeps
  `//nolint:gocyclo // one case per fixture control; splitting the
  dispatcher would hurt clarity` (gofmt puts a `//` line between the doc
  comment and the directive). bodyclose ×5 in engine/fixturetransport's
  test: every response closed through closeResponse (tolerates a failed
  round trip's nil response and http.NoBody); transport test PASS.
- 8f702d4791 ci: run the remote cache transfer suite in its own shard —
  test-split: `testRemoteCache` shard (testSpecific
  ["^TestRemoteCacheTransferSuite$"], default pkg ./core/integration),
  "^TestRemoteCacheTransferSuite$" added to test-base's exact-name skip list;
  checks.yml matrix entry "test-split/test-remote-cache".
- 6203a9a807 ci: run the bounded TLA+ configurations — dagger.toml:
  [env.dev.modules.tla-check] → [modules.tla-check] with the comment;
  checks.yml entry "tla-check/quick" with a two-line comment; tla-check
  doc line "(CI runs Quick only)".
Validation of the CI edits: checks.yml parses (28 matrix entries, the two
new ones present); `dagger check --list` in the default environment lists
test-split:test-remote-cache, tla-check:quick, tla-check:cache-lifecycle,
tla-check:client-lifecycle. The shard itself was not run locally (the
coordinator's ruling: the first CI run measures it).

Map (/tmp/pkg-map-a6.txt, 38583498cf..b7-verification vs 4dad71ad4c..HEAD):
71 mapped + 4 new (the above) = 75 commits; 27 unmapped, the drops, which
split as: 25 whose content is already present lower in the stack or
cancelled (the eight manifest corrections placed in A3, A4, A5 and #14228,
the #14228 follow-ups 16786b5fe3 and 87c099a615, the boundary pair, the
four rewrites, the umask pair, the A5 pair, 4927844037 which changed
nothing, and the park/revert pair that cancels itself), plus 2 whose code
is present lower (#14228's follow-ups 95a320301e and a4ea34dcec) and whose
fixture test hunks, TestImportedBackingSnapshotIsDroppedWhenItsOwnerAttachFails
and TestImportedBackingSnapshotConcurrentFirstUses, are deferred by the
coordinator's ruling to an A6 follow-up (5d3ee071c7 and cfa148371c).
Correction per the reviewer: an earlier line here said "75 mapped".

Tip 6203a9a807 runs, head files first:
- /tmp/pkg-a6-lint2.{head,log}: `golangci-lint:lint-all DONE [1m34s]`,
  findings: 0.
- /tmp/pkg-a6-tests1.{head,log}: `go test -v -count=1 -timeout 60s ./core/
  ./dagql/ ./engine/server/ ./engine/snapshots/ ./engine/fixturetransport/`
  → `ok core 24.084s`, `ok dagql 17.250s`, `ok engine/server 4.771s`,
  `ok engine/snapshots 10.785s`, `ok engine/fixturetransport 0.008s`; 1283
  PASS, 0 FAIL, 1 top-level SKIP (TestOfferPartsNativeAdmission, the probe)
  plus one inherited nested SKIP. PASS lines:
  TestPartAcquiredValuesCloneForContainers, TestFixtureTransport.
- /tmp/pkg-a6-tests1-schema.log: `go test -v -count=1 -timeout 120s
  ./core/schema/` → `ok core/schema 13.051s`, 173 PASS, 0 FAIL, 0 SKIP.
- `go vet ./core/integration/` clean.
- TLA audit at the tip (/tmp/pkg-a6-tla-audit.{head,log}): CacheLifecycle
  0 findings, 30 constants, 10 variables, 43 cfgs; the four models A6 adds
  and SnapshotChain (/tmp/pkg-a6-tla-audit-others.log): RemoteParts 0
  findings (6/8/8 cfgs), RemoteOwners 0 (3/12/7), RemoteSharing 0
  (4/15/12), RemoteCheckpoint 0 (2/18/9), SnapshotChain 0 (4/10/2).
Recorded, not fixed (main's own code): go vet lostcancel at
engine/engineutil/executor.go:565,649,716 and
engine/server/session_attachables.go:211.

## Deferrals ruled by the coordinator (A6 entry and corrections table addendum)

Ruled after the reviewer's A6 completeness point: "the A1 umask pair and
the A2 backing-snapshot follow-ups reach the PRs above at the next base
move; until then the copies of those files above A2 predate them."
Corrections table rows affected: 73a7917e05 and c591ea43ac (destination
#14224, published as c15cec7aa6 and e3e75aedb1; A3–A6's base 4dad71ad4c
does not contain them, so core/lazy_operation_execution_test.go above A2
still calls syscall.Umask in the shared test process); 16786b5fe3,
5d3ee071c7, cfa148371c, 87c099a615 (destination #14228, published as
2266f481a1, 95a320301e, a4ea34dcec and dca16de409's line; absent from
A3–A6's base c7dc73fbf7); and the two fixture test hunks of 5d3ee071c7 and
cfa148371c (destination A6, deferred until that base move). Planned by the
coordinator: one coordinated move sweep after A6 is published: A2 onto
#14224's head e3e75aedb1, then A3, A4, A5, A6 each onto the one below,
range-diff all equal, the umask change's propagation into A3's renamed file
resolved deliberately (inUmaskChild in lazy_operation_execution_test.go),
the two deferred backing-snapshot fixture cases appended to A6 in the same
sweep; one push per PR, one CI run each.

Gap found by the reviewer (A6 B3): bbbe792279's core/part_offer_admission_test.go
hunk (two File.Contents reads → demandedFileContents) is assigned to A4 in
the split table but was never applied; TestOfferPartsNativeAdmission still
probed and skipped. Coordinator: apply it as a follow-up on #14233, the
probe removed if the test then runs unprivileged, core once at 60 s, lint
once, reviewer, push with lease on 39bbd96e7c; A5/A6 pick it up in the
coordinated move. Split table rows for bbbe792279/397168d119 to be
re-checked against the actual series.

CI reruns (coordinator: "Run"): scheduler /tmp/pkg-reruns-0150.sh started
at 01:42Z (log /tmp/pkg-reruns-0150.log): waits for 01:50Z, probes
registry.dagger.io/v2/engine/tags/list for 200, then reruns #14224
golang:test-all, #14224 test-split:test-provision, #14228 golang:test-all
(registry cause, ImagePullBackOff on registry.dagger.io/engine and 500s at
00:24Z and 00:47–00:54Z) and #14224 test-split:test-workspaces
(TestWorkspace/TestWorkspaceExportLocalWorkdirAndFrom/from_baseline: main
defect, fixed on main by #14227 (4056f4a8b2), not in the stack's base
601d12f424; rerun freely on that assertion until the base includes it).
#14229 "load" failure ("PR has merge conflicts"): local merges of
380ab472de onto the base branch head dca16de409 are clean (with and
without rename detection, one merge base c7dc73fbf7); GitHub REST reports
mergeable=false, mergeable_state=dirty, base.sha c7dc73fbf7. The
authorized recompute nudge, PATCH base to the same branch, was refused:
HTTP 422 "Cannot change the base branch because the pull request is part
of a stack." Sent to the coordinator; stopped there.

## A6 B4 and B1, then Erik's stack-wide rebase (phase 1, local)

Native check selection (core/schema/workspace.go:3935 `checks`; modules
from the active configuration :3966; every +check per module :3984;
include/skip :3947, :3993, :3997-4002; config skips
`[modules.<name>.check] skip` :3971-3973/:4005-4010, core/workspace/config.go:112,136):
the native PR runner passes no include, so every check of every
default-environment module is scheduled; checks.yml's matrix decides only
pushes to releases/** (pull_request commented at :7-13). Fix on A6:
f5d75bfd2d "tla-check: make the hours-long runs plain functions, keep
Quick the check" (CacheLifecycle and ClientLifecycle lose +check, docs,
dagger.toml comment, README invocations without --env dev, checks.yml
comment removed, plain entry kept); `dagger check --list` then shows only
tla-check:quick for the module (and test-split:test-remote-cache). B1:
b5cb5faa85 "chore: regenerate tla-check module bindings" (`dagger generate
-y go-sdk:generate`; dagger.gen.go 36 lines: source maps +41/+40, the two
WithCheck() registrations gone, descriptions; tla-check.gen.go 19 lines of
doc text). A6 tip before the rebase: b5cb5faa85.

A4 follow-up 5e0ddaaccb "core: read the offered bytes in place in the
native admission test" (bbbe792279's two reads → demandedFileContents,
probe removed; focused PASS both subtests; core once 562 PASS 0 FAIL 0 SKIP,
/tmp/pkg-a4-admission-core.{head,log}; lint DONE 0,
/tmp/pkg-a4-admission-lint.{head,log}): reviewer approved; coordinator:
reviewed, pending push, rides in A4's series in the stack-wide rebase.

Reruns issued 01:50Z after the tag-list probe returned 200
(/tmp/pkg-reruns-0150.log): #14224 golang:test-all, test-provision,
test-workspaces; #14228 golang:test-all.

Stack recorded bases (gh api repos/dagger/dagger/stacks/13937): #14229
base sha c7dc73fbf7 (branch at dca16de409), #14233 base 380ab472de, #14235
base 39bbd96e7c: the stack records each member's base as the PR below's
head at the member's last push, so #14229's test merge runs against a
stale base. PATCH base refused (422, part of a stack). Superseded by
Erik's call: stack-wide rebase onto upstream/main.

Phase 1 (local, nothing pushed). upstream/main 4056f4a8b2 (four commits
past the old base 601d12f424: #14222, #14227). Branches pkg/r-<n> in
/tmp/pkg-r-<n>, chain script /tmp/pkg-r-chain.sh and -chain2.sh, tips in
/tmp/pkg-r-tips.txt:
  14050 40b7df8381 → 60dbab39fb (15), 14051 a4f7b28366 → 612f0fcd6b (17),
  14093 23f71a77d4 → 84db69369f (9), 14220 e9372bccdb → 274a3b935c (3),
  14224 e3e75aedb1 → 9fafecb977 (35), 14228 dca16de409 → 33d2c1e5d5 (25),
  14229 380ab472de → 7121f77f66 (70), 14233 5e0ddaaccb → ae2e3c7302 (22,
  includes the admission follow-up), 14235 4dad71ad4c → 0178ca416d (31),
  A6 b5cb5faa85 → fb80d86b7a (77) + 25f778bcac (the two deferred
  backing-snapshot fixture cases appended: TestImportedBackingSnapshotIsDroppedWhenItsOwnerAttachFails,
  TestImportedBackingSnapshotConcurrentFirstUses; focused run 3/3 PASS,
  /tmp/pkg-r-a6-backing-focused.{head,log}) = 78.
One conflict: #14229's 997e953f8c (the rename of eager_producer_execution_test.go
to lazy_operation_execution_test.go) against the umask pair now below:
resolved keeping A3's renamed form with inUmaskChild (helper kept, both
umask sites guarded, syscall.Umask only inside the helper). #14229's tip
has the backing fixture in its base and the refusal label at :420 reads
"commit: dependency not held".
Range-diff table (/tmp/pkg-r-rangediff-table.txt, per-PR files
/tmp/pkg-r-rangediff-<n>.txt): every pair `=` except #14229's 997e953f8c →
a2d93ae2f5 (context only, the umask resolution) and A6's one new commit;
no missing patches.

Reviewer: B1/B4 (f5d75bfd2d, b5cb5faa85) approved, their phase-1 copies
range-diff `=`. S2 (README: `dagger` prefix dropped with `--env dev`;
introduction still said dev-env scoped and not in CI; the measured
command at :254 to stay as measured) applied as dc07a67bf2 "dagql/tla:
restore the dagger prefix in the README's examples" on A6's phase-1 tip;
reviewer confirmed. A6 phase-1 tip dc07a67bf2, 79 commits (77 rebased + the
two backing-snapshot cases + the README fix).

Reruns of 01:50Z: #14224 test-workspaces pass (trace 0424bbecc6c4);
#14224 golang:test-all (ed5f045bb7e4), #14228 golang:test-all
(3b2420cd9327) and #14224 test-provision (9dfdb1df0de2) failed again at
01:52Z on the same registry 500s (engine:main pull, engine:v0.16.1
resolve); the tag-list probe was too weak (manifest HEAD with the OCI
Accept headers is the request CI makes; it answered 200 at 02:08Z). No
further rerun under the rule; the rebased heads' first CI run is next.

Phase-1 per-tip runs: /tmp/pkg-r-runs.sh (head files /tmp/pkg-r-<n>.head
and -lint.head written inside the script before each run; logs
/tmp/pkg-r-<n>-{build,vet,tests,schema,lint}.log; summary
/tmp/pkg-r-runs-summary.txt). Phase-2 push script prepared, not run:
/tmp/pkg-r-push.sh (one PR at a time, lease on the exact current head,
waits for GitHub to show the new head).

## Phase 2: pushed; A6 published as #14241

Erik: per-tip local runs stopped (done for #14050, #14051, #14093 only);
range-diff equality suffices, CI runs every head. Pushes 02:24–02:26Z by
/tmp/pkg-r-push.sh (log /tmp/pkg-r-push.log), lease on each previous head,
GitHub confirmed before the next: #14050 60dbab39fb, #14051 612f0fcd6b,
#14093 84db69369f, #14220 274a3b935c, #14224 9fafecb977, #14228 33d2c1e5d5,
#14229 7121f77f66, #14233 ae2e3c7302 (with the admission follow-up), #14235
0178ca416d. A6: branch sipsma/remote-cache-verification pushed at
dc07a67bf2; `gh stack link 13937 sipsma/remote-cache-verification` created
#14241 (base sipsma/remote-cache-snapshot-sharing); REST PATCH title
"remote cache: verification fixture, integration suite and CI" and body
from /tmp/pkg-a6-pr-body.md (/tmp/pkg-a6-patch.json; readback
/tmp/pkg-a6-body-readback.md identical apart from the trailing newline);
`gh pr ready`. Stack API: 18 members, #14241 at position 18, every
member's recorded base is the PR below's new head; #14228–#14241
mergeable=true (blocked on checks). The from_baseline main defect is in
the base now (4056f4a8b2): record entry closed. Pending for the record:
the shard's and tla-check:quick's measured runtimes from #14241's first
check run.

Reviewer's post-rebase confirmation: all nine pushed heads and A6's
dc07a67bf2 approved from the table (306 commits, every patch equal apart
from #14229's one context-only rename adaptation; inUmaskChild byte-identical
to the approved A1 helper; the two in-place reads and the removed probe in
#14233; backing_snapshot_test.go matches cfa148371c apart from import
grouping). Reviewer's note kept: the focused backing-snapshot run
(fb80d86b7a plus one dirty file) is not a clean-tip or full-package run;
CI is the execution gate under Erik's ruling. Watch re-armed on the ten
heads (monitor b1h0ycdbk).

Reviewer: #14241 publication confirmed by GitHub read-back
(sipsma/remote-cache-verification at dc07a67bf2, 79 commits, non-draft,
base sipsma/remote-cache-snapshot-sharing@0178ca416d, body matching
/tmp/pkg-a6-pr-body.md apart from trailing newlines); all ten publication
heads confirmed. Awaiting CI on the ten heads; #14241's first run supplies
the shard's and tla-check:quick's runtimes for the record.

## First CI run on the rebased heads (02:26–02:45Z): registry window

Monitor defect: `gh pr checks` exits 8 when any check is pending or failed;
the loop skipped a PR on non-zero exit, so only the three PRs with nothing
but DCO and netlify reported. Fixed and re-armed (bwbgzjls3).

#14050 60dbab39fb, six failures: golang:test-all (7e56a5bbd959,
TestInstallK3S/default_daemonset, engine:main pull, 500s 02:36–02:43Z);
test-split:test-provision (60239031f3ce, five TestImageDriver cases,
engine:v0.16.1 resolve 500 at 02:28Z); dang-sdk:generate:up-to-date
(1e3a451c1050) and go-sdk:generate:up-to-date (b16fec5551b7): zero log
messages in the traces, local generators on 60dbab39fb change nothing
except the dang generator's engine-version stamp in
.dagger/modules/go-cli/dagger-module.toml (local engine artifact,
reverted); the unrelated PR #14240 shows the same two checks errored the
same way in the same window, with golang:test-all, test-provision and
test-base; release:publish-with-mock-endpoints (be1a32073b74) and
test-split:test-base (819c691d91f2): errored 02:41:35Z, zero log messages,
same pattern. Conclusion: no stale bindings, nothing to regenerate or
propagate; all six are the registry window. Every other head shows the
same generate pair, golang:test-all and test-provision (plus test-base on
#14228/#14235, release and test-cache-persistence on #14224, test-modules /
test-module-runtimes on several), all first runs inside the window.
Reruns: /tmp/pkg-reruns-14050.sh (log /tmp/pkg-reruns-14050.log) waits
for ten consecutive 200 manifest probes a minute apart, then reruns
#14050's six checks once; the other heads follow the same rule after
#14050's rerun shows the registry holds.
The four empty traces (both generate:up-to-date, test-base, release) have
no check span at all (`dagger cloud logs <trace> --check <name>` answers
"no check named ... in trace"): the check errored before its body started,
at module and SDK loading, which pulls through the registry.

#14241 first check run, the two numbers the CI section owed: 
test-split:test-remote-cache passed, Succeeded in 13m14s (trace
15277b262119ec0c9a7e47342dc71a53); tla-check:quick passed, Succeeded in
5m32s (trace 8b59becc33a592320cd37419a5bfa7da). The shard sits well inside
the thirty-minute job limit; no split by subtest.

#14050 rerun of 03:04Z: dang-sdk:generate:up-to-date errored again in
1m51s (trace 8d73449e00bf74ecd1d42a8d67ffe32c) with no check span while
the registry was healthy. Not the stack: the unrelated main-based PR
#14242 (regen-and-bump, run 02:52Z) errors the same two synthetic checks
(dang-sdk:generate:up-to-date 2m48s, go-sdk:generate:up-to-date 3m5s) and
passes changie's and go-client's; locally the name does not even exist
(`dagger check dang-sdk:generate:up-to-date` → no checks matched), since
the synthetic SDK generate checks are produced by the CI runner's engine.
A CI-side defect of those two checks on every PR; nothing to regenerate.

#14050 rerun round (03:04Z): test-split:test-base and
release:publish-with-mock-endpoints pass; golang:test-all (e72ca9dd955b)
and test-split:test-provision (05818abaa74c) fail again on registry 500s
from 03:06:56Z to 03:15:23Z as seen from CI, while manifest probes from
this host answered 200 throughout (probe not representative of CI's
path); the two synthetic generate checks fail again (CI-side). Rerun
budget for the registry cause spent on #14050; no further reruns without
a ruling. Other heads' first-run failures: registry pattern or "no check
span" (#14228 test-base bdd519b17edc, #14235 test-base 0fad60577521,
#14241 test-base 883bbe663825 "Errored in 0.1s" at 03:05:48Z with no
pending entry before it, #14241 test-module-runtimes b94d737beb4a,
#14224 release/test-cache-persistence/test-modules, several
test-modules/test-module-runtimes). Proposed gate: an unrelated PR's
golang:test-all and test-provision passing, then one stack-wide rerun on
a ruling.

Gate change (coordinator's ruling on my proposal): host probes of the
registry do not reflect CI's path, so the rerun gate is evidence from CI
itself: an unrelated open PR whose golang:test-all and
test-split:test-provision both passed after 03:15Z. Then one stack-wide
rerun of every registry-pattern and no-span check (all failed checks
except dang-sdk:generate:up-to-date and go-sdk:generate:up-to-date, which
stay untouched as a CI-side defect); #14050's second rerun for the
registry cause is authorized under that gate. Script
/tmp/pkg-gate-reruns.sh (polls every five minutes; log
/tmp/pkg-gate-reruns.log).

## Second stack-wide rebase (Erik's fast path), onto main cd3b79e66a

Erik: a fix merged to main that may resolve the synthetic generate checks
(main cd3b79e66a, #14226 post-release beta.14 bump, six commits past
4056f4a8b2); fast path: rebase, range-diff, build at the top tip only, no
per-tip test or lint runs, push bottom-up with lease; the gated rerun
script cancelled (task stopped, nothing fired: gate=none at 03:24Z and
03:29Z). Chain /tmp/pkg-r2-chain.sh on the pkg/r-<n> branches, old heads
/tmp/pkg-r2-oldheads.txt, tips /tmp/pkg-r2-tips.txt, range-diffs
/tmp/pkg-r2-rangediff-<n>.txt, table /tmp/pkg-r2-table.txt: no conflicts,
every pair `=`:
  14050 60dbab39fb → ade30bcc1a (15), 14051 612f0fcd6b → ce8895c9e5 (17),
  14093 84db69369f → a3cafc3314 (9), 14220 274a3b935c → 2d1369d172 (3),
  14224 9fafecb977 → ad1afd8701 (35), 14228 33d2c1e5d5 → 6f7a49c5a3 (25),
  14229 7121f77f66 → 6dc366ab82 (70), 14233 ae2e3c7302 → 72dd21f7f3 (22),
  14235 0178ca416d → 70efe651b0 (31), 14241 dc07a67bf2 → 49f36c61f2 (79).
`go build ./...` at 49f36c61f2: exit 0. Pushes by /tmp/pkg-r2-push.sh
(log /tmp/pkg-r2-push.log), lease on each current head, GitHub confirmed
before the next.
Pushed 03:42–03:44Z, all ten, GitHub-confirmed: #14050 ade30bcc1a, #14051
ce8895c9e5, #14093 a3cafc3314, #14220 2d1369d172, #14224 ad1afd8701,
#14228 6f7a49c5a3, #14229 6dc366ab82, #14233 72dd21f7f3, #14235
70efe651b0, #14241 49f36c61f2. Stack API: bases follow, #14050 on
main@cd3b79e66a. First CI run on these heads is the next evidence.
Reviewer's confirmation of the second rebase: all 306 patches equal,
every parent link correct from cd3b79e66a, all ten GitHub heads and base
branches match; each of the ten heads approved as published (no tests or
builds by the reviewer).

First CI run on the second-rebase heads (from 03:42Z): the synthetic
generate pair errors pre-body (no check span) again on #14050, #14093,
#14220, #14224, #14228, #14229, #14233, #14235, #14241 (the merged main
fix did not change it); test-provision on #14050 (787ae21558f1) and
#14051 on registry 500s at 03:45–03:48Z; #14220 test-container
(9637de8a83731) TestContainer/TestPublishAndFromWithRegistryServiceBinding/plain_http_engine_config
117 s, nested call exit 1, no registry lines, main's own test untouched by
the stack, passed on #14050, #14051 and unrelated #14242: single-run
flake, one rerun available under the rule, awaiting the ruling.
Ruling: one rerun of #14220 test-split:test-container (own documented
cause). Issued 03:53Z on 2d1369d172 (a first attempt with a mistyped
commit was refused by the tool and did nothing). Registry-pattern and
generate checks untouched.
03:53Z: vito approved #14050 (ade30bcc1a) on GitHub (human maintainer
approval; checks not green: golang:test-all 4dabe5376cd7 on registry 500s
03:44–03:53Z, test-provision, the generate pair). test-provision now
failed on all ten heads (registry). #14235 python-client:python-311:slow
(063c7129f071): tests/provisioning/test_integration_connection.py::test_execute_timeout,
ClientConnectionError after a one-second read timeout, 1 failed / 36
passed; sdk/python untouched by #14235; passes on #14050, #14051, #14233,
#14241: single-run flake of main's test, one rerun available on ruling.
Ruling: one rerun of #14235 python-client:python-311:slow, issued on
70efe651b0. Standing delegation from the coordinator: a single failure of a
main-owned test the stack does not touch, passing on neighbouring heads in
the same window, gets its one rerun on my own call, recorded here;
anything else goes to the coordinator. #14050 approved by vito; merges
when its checks are green, which waits on the registry and the generate
pair (CI-side).
#14093 (a3cafc3314) golangci-lint:lint-all errored (trace
0261f21939e50ec0ad020176f5c8f003, 13m56s): seven nolintlint findings, all
"directive //nolint:staticcheck is unused" in core/schema/modulesource.go
:1101-1103, core/workspace/migrate.go:377, engine/clientdb/span.go:195,
engine/contenthash/tarsum.go:40,43: main-owned files untouched by #14093
and by the stack through it; the same check passed on #14050, #14051,
#14220, #14224 in the same window, so the run's staticcheck did not report
the deprecations it reports elsewhere (a non-deterministic lint run, not a
finding). Under the standing delegation (single failure, main-owned code
the stack does not touch, neighbours pass) one rerun issued 03:58Z;
noted to the coordinator that the delegation was read to cover a lint
check of main-owned code, not only a test.

Burst 03:56–04:00Z. (1) Stack code, escalated, not rerun: #14241
test-split:test-remote-cache (be50f19793457ac4):
TestRemoteCacheTransferSuite/TestSharedHostDirectoryLifetime (141.56s),
remote_cache_sharing_test.go:167 expected event kind "owner-sync", actual
"share-skipped", Detail "persist state not ready: row 4429 representation
changed" (Sequence 8, Field directory); the shard passed in 13m14s on
dc07a67bf2 and the rebase changed no patch: timing-dependent outcome in
A5/A6 code. (2) #14051 test-base (07b546ec4257e744):
TestWorkspaceGitCheckoutReuse/discard=false/concurrent,
core/schema/workspace_test.go:148 "materialize the retained checkout only
once: expected 1, actual 2"; core/schema untouched through #14051,
test-base passed on #14050; one rerun under the delegation, 04:02Z,
flagged because A3 adapts this test. (3) No-span "Errored in 15m25s"
at ~04:00Z: test-base #14093 (ec019c51386a) #14228 (661f6d73d456) #14229
#14235; test-modules #14093 (17fe055d488c) #14220 #14235; release #14233
(1ff16d32bb50) #14241; test-module-runtimes #14233 #14241;
test-cache-persistence #14220 #14229; K3S on #14220 #14229; test-provision
everywhere. CI-side, untouched.

CI state of the second-rebase heads at 04:14Z (dang/go = the synthetic
generate pair, k3s = golang:test-all's registry pull, test-provision =
registry; the rest are no-span errors of the 04:00Z burst; #14241
test-remote-cache = the escalated TestSharedHostDirectoryLifetime):
  14050 pending=0 failed=[dang go k3s test-provision ]
  14051 pending=1 failed=[dang go k3s test-provision ]
  14093 pending=0 failed=[dang go k3s test-base test-modules test-provision ]
  14220 pending=1 failed=[dang go k3s test-cache-persistence test-modules test-provision ]
  14224 pending=0 failed=[dang go k3s test-provision ]
  14228 pending=0 failed=[dang go k3s test-base test-provision ]
  14229 pending=0 failed=[dang go k3s test-base test-cache-persistence test-provision ]
  14233 pending=0 failed=[dang go k3s release test-base test-module-runtimes test-provision ]
  14235 pending=0 failed=[dang go k3s test-base test-modules test-provision ]
  14241 pending=1 failed=[dang go k3s release test-module-runtimes test-provision test-remote-cache ]
Delegated reruns passed: #14220 test-container, #14235
python-client:python-311:slow, #14093 golangci-lint:lint-all. #14051
test-base rerun pending.
Ruling "arm": gate timestamp 04:00Z; one rerun of every registry and
no-span check across the ten heads when an unrelated PR passes
golang:test-all and test-provision after that time; the generate pair and
#14241 test-remote-cache (with the analyst) untouched. Script
/tmp/pkg-gate-reruns.sh re-armed (log /tmp/pkg-gate-reruns-2.log), polling
every five minutes; it reruns only checks failed at the moment it fires.
#14220 test-split:test-workspaces (2ac73ac72723d92b): "Cancelled - max
execution"; five TestWorkspace cases at the 300 s bound and a compat case
at 167 s, nested sessions' shutdown POST hitting its deadline, no registry
lines; passes on #14093, #14224, #14228. Runner overload of the 04:00Z
burst; covered by the armed stack-wide rerun (it reruns whatever is failed
when the gate fires), nothing separate issued.
#14051 test-base rerun passed (TestWorkspaceGitCheckoutReuse/discard=false/concurrent
did not recur); #14051 now carries only the CI-side four. All four
delegated reruns passed (#14220 container, #14235 python, #14093 lint,
#14051 test-base).

## Third stack-wide rebase (fast path), onto main 316147206d

Erik: the generate-check fix merged (#14246 "ci: temporarily skip SDK
generation freshness checks", dagger.toml +2). Gate script disarmed (never
fired, gate=none through 04:46Z). Chain /tmp/pkg-r3-chain.sh, old heads
/tmp/pkg-r3-oldheads.txt (= the second-rebase tips), tips
/tmp/pkg-r3-tips.txt, range-diffs /tmp/pkg-r3-rangediff-<n>.txt, table
/tmp/pkg-r3-table.txt: no conflicts; every pair `=` except A6's
5f9bba85a7 → 822c14f946 "ci: run the bounded TLA+ configurations",
context only (main's two dagger.toml lines beside A6's tla-check
registration). Tips: 14050 b6b328b4ad, 14051 e982b148bf, 14093
5075c989a9, 14220 de872846a3, 14224 6a9bdbceff, 14228 76c0e46853, 14229
9c1823e8e0, 14233 f7856e2a61, 14235 e0ce431522, 14241 7cba6796a7.
`go build ./...` at 7cba6796a7: exit 0. Pushes by /tmp/pkg-r3-push.sh
(log /tmp/pkg-r3-push.log). Registry checks expected red on the new
heads; left alone per Erik.
Pushed 04:52–04:54Z, all ten, GitHub-confirmed: #14050 b6b328b4ad, #14051
e982b148bf, #14093 5075c989a9, #14220 de872846a3, #14224 6a9bdbceff,
#14228 76c0e46853, #14229 9c1823e8e0, #14233 f7856e2a61, #14235
e0ce431522, #14241 7cba6796a7; stack chain verified (bottom base
main@316147206d). #14050 now lists 86 checks (the generate pair skipped).
Reviewer's confirmation of the third rebase against 316147206d: every
parent link and all ten GitHub head/base read-backs match; the only
range-diff change is A6's dagger.toml context (main's two check.skip lines
under dang-sdk and go-sdk, the TLA registration intact); all ten heads
approved (no tests or builds by the reviewer).
Third-rebase heads, first run: #14241 (7cba6796a7)
test-split:test-remote-cache passed, Succeeded in 13m6s (trace
fe484bd392a35a5f…), so TestSharedHostDirectoryLifetime did not recur on
this run (the escalation stays with the analyst: one failure in two runs
of the shard); tla-check:quick passed in 2m28s (719c4a907e6a76d4…). The
generate pair is no longer scheduled. Every head fails test-provision on
the registry, #14050 and #14051 also golang:test-all (K3S pull), all left
alone per Erik; no other failure so far.

## #14050 merge (Erik's call)

Rule update from the coordinator: for the rest of the stack,
registry-caused failures (test-split:test-provision, golang:test-all's K3S
engine pull) do not block a merge while the registry fault persists;
every other check must be green and a maintainer approval present.
#14050 at b6b328b4ad, vito's approval, only those two failures:
`PUT repos/dagger/dagger/pulls/14050/merge-async -f sha=b6b328b4ad… -f
merge_method=merge` at 05:11Z → status pending, "Merge request
enqueued", uuid a2a711b9-b7c4-4506-b17a-fe5c72823c65, expected head
b6b328b4ad. Next: main hash, GitHub's automatic rebase of the nine above
(heads and range-diffs against the third-rebase tips), watch re-armed.
#14050 merged: main 92c4619047dd6649be08be7510e0d0f356ae7a14 "Merge pull
request #14050 from dagger/sipsma/remote-cache-container-part-persistence"
(merge commit, head b6b328b4ad). GitHub's automatic rebase of the nine
above confirmed: #14051's base main@92c4619047, every base above equal to
the head below (/tmp/pkg-m1-stack.txt); range-diffs against the
third-rebase tips all equal (/tmp/pkg-m1-table.txt,
/tmp/pkg-m1-rangediff-<n>.txt):
  14051  e982b148bf 87a8043da4  17    17    0 0
  14093  5075c989a9 115a6e911f   9     9    0 0
  14220  de872846a3 67e9caf377   3     3    0 0
  14224  6a9bdbceff 6f9b926390  35    35    0 0
  14228  76c0e46853 dbd528117a  25    25    0 0
  14229  9c1823e8e0 0bce94ee73  70    70    0 0
  14233  f7856e2a61 3b65335dfb  22    22    0 0
  14235  e0ce431522 ab27485983  31    31    0 0
  14241  7cba6796a7 757d6e1fec  79    79    0 0
Watch re-armed on the nine open PRs.

First run after #14050's merge (heads 87a8043da4…757d6e1fec): registry
class (test-provision everywhere; golang:test-all K3S on #14224, #14229,
#14233, #14235, #14241) left alone under the merge rule. No-span "Errored
in 15m3x–15m4xs" (zero log messages): test-cache-persistence on #14051
(96f4d50983f2), #14093 (224add051886), #14241 (b0bf225ddb21);
test-module-runtimes on #14233 (d57784d5f9f9), #14241 (2ae0436d133c):
CI-side, untouched. #14229 (0bce94ee73) test-split:test-local-cache
(2ca34ef77a99): TestLocalCache/TestDagqlMetadataGCProtectsActiveZeroDiskResults
(75.02s); core/integration/localcache_test.go untouched by the stack
through #14229 (main's #14222 last touched it), the check passes on the
other eight heads: delegated class, one rerun issued.
Cause of the #14229 local-cache failure: "active metadata workload: timed
out waiting for metrics; last=map[dagger_connected_clients:0 ...]"
(testctx.go:193), the metrics wait that main's #14222 (7e3c431108, in the
base) had just loosened; a timing flake of main's test under CI load.
Rerun issued 05:30Z.
#14229 local-cache rerun passed. Second no-span burst at 05:29Z ("Errored
in 15m39–15m42s", zero log messages): test-base #14051 (ffb230b43b6e);
test-modules and test-module-runtimes #14093 (d3d65b67fa74,
cead80d43806) and #14220 (613110656316, ff0801fdbf4c); test-cache-persistence
#14051, #14093, #14241; test-module-runtimes #14233, #14241. CI-side,
untouched; two identical bursts now (04:00Z, 05:29Z). Stack code,
escalated, not rerun: #14235 (ab27485983) test-split:test-base
(73176f3018529bd2), TestPartHostInlineAllPartsRetriesCapture/before-metadata,
dagql/cache_part_host_test.go:137 "each discovery attempt releases its
row hold: expected 2, actual 3": the analyst's test from #14229's line
(32eb3e17ec); test-base passed on #14093, #14224, #14229 this run.
Post-merge run complete on #14229 (only the registry pair) and #14241
(registry pair plus two no-span; test-split:test-remote-cache and
tla-check:quick passed again, so the shard is two passes in three runs).
The reviewer's note on be2ae11fac (sipsma/localcache-active-wait, someone
else's main-placed fix for the local-cache flake) answered: not my commit
or run; coordinator copied.

## Main PR #14248 (localcache active wait) under the CI watch

#14248, sipsma/localcache-active-wait at be2ae11fac on main 92c4619047:
the 90 s active-observation wait for
TestDagqlMetadataGCProtectsActiveZeroDiskResults; plain merge, not a stack
member; merge rule: all checks green (registry pair excepted while the
fault persists) and a maintainer approval. Its record cites the triage
above (trace 2ca34ef77a9987ce313b3e5a37946ca7, passes on the other eight
heads, delegated rerun passed). On merge the local-cache flake entry
closes as "fixed on main at <hash>". Watch re-armed with #14248 included.

#14229 pushed by the coordinator at 5510ddada6: the analyst's approved
fix of the inline hold-count test (TestPartHostInlineAllPartsRetriesCapture/before-metadata,
the A3 flake, trace 73176f3018529bd2fd4d7b7d39f84a2f) cherry-picked onto
0bce94ee73; 71 commits; lease held. Test-only, so #14233 and above stay
per the move rule (they pick it up at the next base move or GitHub's
merge-time rebase). The A6 sharing-test fix (TestSharedHostDirectoryLifetime)
follows once its t.Parallel follow-up is approved. #14051's checks going
pending again without us: Dagger Cloud's own rerun.

#14241 pushed by the coordinator at 48476414db: the analyst's approved
sharing-test pair cherry-picked onto 757d6e1fec (23a0b18c59 tolerance of
a skipped pass plus its regression case; 48476414db t.Parallel); 81
commits; lease held; no evidence files. The A6 shard flake fix
(TestSharedHostDirectoryLifetime, trace be50f19793457ac431dc2cc43d99d7c6).
Both stack flakes are now fixed in their PRs (#14229 5510ddada6, #14241
48476414db). CI watch continues.
#14248 first run: registry pair (golang:test-all 2309482b7036, K3S;
test-provision 7805c317923710, both on 500s), exempt; plus
test-split:test-base (1c5c02b2958b7a37, 14m12s):
TestGenerators/TestSDKModuleClientUpdateRefreshesLockAndRegenerates,
generators_test.go:659, a lockfile-contents assertion after a git-ref
update (network-dependent), main's own test (#14248 changes only
core/integration/localcache_test.go), passes on #14093 and #14224 in the
same period: delegated class, one rerun issued.
#14241 at 48476414db, first run: test-split:test-remote-cache passed (the
sharing fix held); test-split:test-base (a63d407184e09612, 16m1s) fails
two stack tests not seen failing before: TestPartDecodeLosesToInstalledRevision
(dagql/cache_part_decode_test.go:212, expected 1 actual 0) and
TestScratchDirectoryAcquisition/cold (core/schema/directory_scratch_test.go:395
"final row release must release its own accessor", expected 1 actual 0).
The analyst's 48476414db adds t.Parallel in dagql/cache_snapshot_sharing_test.go
(same package as the decode test). Escalated; not rerun.
#14248 test-base rerun passed; #14248 now has only the exempt registry
pair failed and awaits a maintainer approval for its plain merge.
#14229 at 5510ddada6: all 86 checks green, including golang:test-all and
test-provision this time (the registry answered), and test-base with the
A3 flake fix in it. Awaits a maintainer approval.

## Staggered registry reruns (Erik: the registry error is clearing)

One PR at a time, bottom-up (#14051, #14093, #14220, #14224, #14228,
#14233, #14235, #14241, then #14231, #14248), rerunning only that PR's
failed registry-class checks (test-split:test-provision, golang:test-all),
next PR only after the previous PR's reruns finish; stop and report if a
rerun fails on the registry again; no other checks touched. Script
/tmp/pkg-staggered-reruns.sh, timestamped log /tmp/pkg-staggered-reruns.log.
#14231 (dce5557471) currently has no failed check and is skipped by the
script unless one appears.
#14051's Cloud-issued test-base rerun: "Cancelled - max execution time"
(f751b8db838e2410, no FAIL line, no registry lines); the only non-green
check on #14051; not registry-class, not a single failing test; proposed
to the coordinator as an infrastructure cause with one rerun.

Staggered sequence: #14093's golang:test-all and test-provision both
passed on rerun (06:25Z; the registry answered), sequence moved to
#14220. #14093 now carries only the no-span trio (test-cache-persistence,
test-modules, test-module-runtimes).

#14229 pushed by the coordinator at d42472a17e (73 commits; lease held):
the analyst's two approved test fixes on 5510ddada6: 3e2be0973e (scratch
acquisition waits for session cleanup before Prune; TestScratchDirectoryAcquisition/cold)
and d42472a17e (part-decode waits for session cleanup before removing the
persisted edge; TestPartDecodeLosesToInstalledRevision), both against
trace a63d407184e096123baa17b93aaef237; the reviewer placed the decode fix
in A3 since A3 introduced that test. Test-only: PRs above stay. #14241's
test-base failure at 48476414db is addressed at its source and clears when
A6 next moves onto A3's line (merge-time rebase or the next sweep).
Staggered sequence: #14220's two registry reruns passed (06:29Z; only its
no-span pair remains); #14224's passed too, and #14224 (6f9b926390) is
now fully green, awaiting a maintainer approval. Sequence on #14228.
Ruling: max-execution cancellation with no failing test is an
infrastructure cause with one rerun, added to the delegation; #14051's
test-base re-run 06:34Z. Appended to the staggered sequence, after the
registry pass: one rerun each of the no-span errored (or cancelled,
no-failing-test) checks per head, same one-PR-at-a-time pacing; checks
whose log has a failing test are left alone. Script
/tmp/pkg-staggered-nospan.sh (waits for the registry pass to end), log
/tmp/pkg-staggered-nospan.log.
#14228 (dbd528117a): both registry reruns passed; fully green, awaiting a
maintainer approval. Sequence on #14233.
#14229 at d42472a17e: all 86 checks green (the two test fixes included);
awaits a maintainer approval.
#14233's registry pair passed (only its no-span module-runtimes left);
#14235's registry pair passed (only the escalated test-base left, fixed
at its source in #14229 at d42472a17e). #14248 (be2ae11fac): both
registry checks now pass (Cloud's own rerun of provision; K3S earlier),
fully green, awaiting a maintainer approval for its plain merge.
Registry pass complete 06:56Z (/tmp/pkg-staggered-reruns.log): every
rerun passed, none failed on the registry: #14093 06:25Z, #14220 06:29Z,
#14224 06:34Z, #14228 06:39Z, #14233 06:44Z, #14235 06:49Z, #14248 06:56Z;
#14051, #14241, #14231 had no failed registry-class check and were
skipped. No-span pass started 06:56Z (/tmp/pkg-staggered-nospan.log);
#14051 had nothing left to rerun (its test-base rerun passed).
No-span pass: #14093's test-cache-persistence, test-modules and
test-module-runtimes all passed on rerun; #14093 (115a6e911f) fully
green, awaiting a maintainer approval.
CORRECTION: #14051's test-base did not pass. The 06:35Z rerun was
"Cancelled - max execution time exceeded" at 07:05Z (trace
4d04c9f6c86202cf1392f34f1177fb4d), like the Cloud rerun of 06:18Z
(f751b8db838e2410); the no-span script's "nothing to rerun" at 06:56Z
reflected a pending check, which I recorded as a pass without checking.
History on 87a8043da4: 05:29Z errored no-span, 06:18Z cancelled, 07:05Z
cancelled. In both cancelled runs 64 packages (core, dagql, core/schema,
engine/server among them) finished; only core/integration had no result
at the 30-minute limit; no FAIL, no registry lines. Same content passed
test-base on the three previous heads (612f0fcd6b 22m5s, ce8895c9e5
14m35s, e982b148bf 19m10s). Delegated rerun spent; ruling requested.
#14051 is NOT merge-ready; the earlier "fully green" lines above for
#14051 are withdrawn.
Ruling: one more rerun of #14051's test-base after the no-span pass
completes (both cancellations fell in the loaded 06:00–07:00Z window; the
same content finished in 14–22 minutes on three earlier heads and #14224's
post-merge head passed test-base); if it cancels a third time, stop; the
coordinator takes the -v question (core/integration's runtime near the
30-minute limit is a CI-shape problem) to Erik. Script
/tmp/pkg-14051-testbase-rerun.sh, log /tmp/pkg-14051-testbase-rerun.log.
Report-writing rule from the correction: a check is reported by its
terminal state read from the API at the moment of the report, never
inferred from a script's "nothing to rerun".
No-span pass: #14220's test-modules and test-module-runtimes passed on
rerun; #14220 (67e9caf377) has no non-pass check (API read at the time of
this line); awaits a maintainer approval.
No-span pass complete 07:21Z (/tmp/pkg-staggered-nospan.log): #14093's
three, #14220's two and #14233's module-runtimes all passed on rerun;
#14235's and #14241's test-base left alone (failing tests, both fixed at
their source in #14229); nothing to rerun elsewhere. API terminal states
at 07:22Z: #14233 (3b65335dfb) no non-pass check; #14235 and #14241
test-base=fail only. #14051's third test-base run starting next
(/tmp/pkg-14051-testbase-rerun.log).

## E-series (Erik): E9, E15, E12, E13

Sources (outstanding-work.md §3.2 at 4504a3a4aa; branch engine-main): E9
3d2cbd783c + 88304eb722; E15 3587242fe7; E12 535165c6a7, ab9db370a0,
5193483904, 3c448d99d9; E13 fcf5245821, 54cf1fbd51, b347418163;
c96012aad7 superseded by ed7a4a47f9 (#14228's line). Plan approved.
Placement facts found at the first cherry-picks: E15's code needs
partCanReselect and its test partRefused/partRefusedBy, A3's reselect
class, absent on main (no ErrPartReselect there) → ruled: follow-up on
#14229 (branch sipsma/part-reselect-span-status unused). E9's code fix
compiles on main; its test used A1's persistedListTestCache/Result and
A2's export API → ruled: main PR with the test rewritten against main's
dagql helpers keeping the three dependency assertions; the export-carrying
assertion reaches the stack when main carries the fix (to confirm on A2's
line at the next stack move). E12/E13: hand port onto #14051's snapshot
code, tests against its testutil; the A1/A3-only hunks of ab9db370a0
(core/part_scope_boot_test.go, core/schema/lazy_resolver_cleanup_test.go)
recorded for their PRs at the next move; the builtin-image persistent
lease stays on #14051 (flagged to Erik). Worktrees: /tmp/pkg-e9
(sipsma/dagql-null-row-dependency-edges off f283737ff7), /tmp/pkg-e15-a3
(pkg/e15 on d42472a17e), /tmp/pkg-r-14051 (87a8043da4).

E15: pkg/e15 fce10013ff on d42472a17e (#14229), 3587242fe7 cherry-picked
-x, message rewritten (no workstream vocabulary, originating commit named,
no attribution trailer, Erik signoff). Runs, head files first:
/tmp/pkg-e15-tests.{head,log} `go test -v -count=1 -timeout 60s ./dagql/`
→ `ok dagql 4.727s`, 443 PASS, 0 FAIL, 0 top-level SKIP plus one
inherited nested SKIP (log line 729, reviewer's correction);
/tmp/pkg-e15-lint.{head,log} `golangci-lint:lint-all DONE [1m39s]`, 0
findings. Reviewer approved; coordinator pushes.

E9: sipsma/dagql-null-row-dependency-edges 62e596517f on f283737ff7
(main): 3d2cbd783c's dagql/cache.go fix (11 lines) with the test rewritten
against main's helpers (NewCache, cacheTestIntCall, cacheTestIntResult,
noopTypeResolver, the row's deps map), keeping the three dependency
assertions, dropping the export-carrying assertion (needs A2). Mishap
recorded: the first squash (9d97a810d8) took the original test from the
index while the rewrite sat unstaged, and a cleanup discarded the rewrite;
recreated and amended into 62e596517f (my own unreviewed commit). Runs on
62e596517f, head files first, dirty=0: /tmp/pkg-e9-tests.{head,log}
`go test -v -count=1 -timeout 60s ./dagql/` → `ok dagql 2.036s`, 350
PASS, 0 FAIL, 0 top-level SKIP plus one inherited nested SKIP;
/tmp/pkg-e9-lint.{head,log} `golangci-lint:lint-all DONE [40.3s]`, 0
findings; /tmp/pkg-e9-without-fix.{head,log}: with the cache.go hunk
reverted in the working tree the test FAILs (the null row depends on none
of the three), proving the defect on main.
#14229 pushed by the coordinator at fce10013ff (E15 on d42472a17e, 74
commits, lease held). Move rule: no move; E15 changes only
dagql/otelprof_lazy.go's span-ending branch and its test, with no caller
outside the otelprof files above A3 and no A4–A6 test asserting a lazy op
span's status. E9 candidate 62e596517f sent to the reviewer.

## Stack merged; main watch; #14241 rebase; E12/E13 to main

Erik merged the stack (21 September): main first-parent 9282dfa127
"Merge pull request #14229" (carrying #14051–#14228 below it) then
b831de5b6a "Merge pull request #14235" (carrying #14233). Open: #14241
(A6, CONFLICTING), #14231 (CONFLICTING now), #14248 (MERGEABLE), and E9
published by the coordinator as #14263 (sipsma/dagql-null-row-dependency-edges
at 62e596517f, base main); all four under the CI watch and the merge rule.
Reviewer's provenance correction on E9 recorded: the negative-control run
(/tmp/pkg-e9-without-fix.{head,log}) was the edited tree with
dagql/cache.go's hunk reverted in the working tree, run as `go test -v
-count=1 -timeout 60s -run TestNullResultRecordsFrameDependencies
./dagql/`, not a clean 62e596517f run; its head file's dirty=0 was
written before the revert.

Main watch (monitor): 9282dfa127 and b831de5b6a push-triggered checks all
pending or success so far, none failed; b831de5b6a's release row: the
target-version up-to-date checks success, python-client release-dry-run
success, the other release-dry-runs pending; release:publish not yet
reported. Terminal states are read from the API at each report.

#14241 rebase onto b831de5b6a (branch pkg/a6-main in /tmp/pkg-r-a6 from
48476414db; 81 commits): one conflict, dagger.toml in "ci: run the
bounded TLA+ configurations" (930fff806e): main's 8a9c4ac3e8 renamed
`[env.dev.modules.engine-lab]` to `[modules.engine-lab]` at the line the
commit used as context for removing the dev-only tla-check block;
resolved by removing the tla-check dev block (this side's intent) and
keeping main's `[modules.engine-lab]` header (dagger.toml:155); the
commit's `[modules.tla-check]` registration lands at :140 as before.
Tip 40a0eee986. Not pushed (coordinator's hold).

E12/E13: target #14051 merged, so like E15 they follow the placement rule
again: one PR to main, branch sipsma/builtin-image-blob-lifetime
(/tmp/pkg-e12) off b831de5b6a; main now has A1–A5, so the A1/A3-only
hunks apply directly; the remaining differences from engine-main's base
are engine/server/server.go (24 lines), engine/snapshots/lease.go (6) and
testutil/store.go (10, the c96012aad7 vs ed7a4a47f9 helper).
#14241 pushed at 40a0eee986 (19:27Z, lease on 48476414db; MERGEABLE
against main). Runs on 40a0eee986, head files first
(/tmp/pkg-a6-main-{tests,schema,lint}.*): six packages 60 s and
core/schema 120 s → core 24.258s, dagql 15.809s, engine/server 3.609s,
engine/snapshots 9.219s, engine/fixturetransport 0.005s, core/schema
14.645s; 1340 + 173 top-level PASS, 0 FAIL, 0 top-level SKIP (the
admission test runs unprivileged now) plus one inherited nested SKIP;
lint-all DONE [5m45s], 0 findings.

Main watch, terminal: 9282dfa127 failed python-client:python-312:slow
(885bd8a3b10b8f51, test_container_build) and test-split:test-container
(35d7f0c5f82aa630, TestSaveHostContainerd, TestLoadHostContainerd);
b831de5b6a failed golang:test-all (417fd0fdb90fdeb5, TestInstallK3S) and
test-split:test-provision (82b62a7ab190abb9, nerdctl cases). All four:
`read "https://proxy.golang.org/.../@v/....zip": stream error: ...
INTERNAL_ERROR; received from peer` in Go module downloads (dev-engine
image dockerBuild on golang:1.24-bookworm, or the test binary's own
downloads); no registry.dagger.io lines; provision_test.go,
container_test.go, helm_test.go untouched by the stack: a Go module proxy
outage, not ours. release:publish not yet reported on either head; no
release-row failure.

#14231 rebased onto b831de5b6a (pkg/main-gs-rebased in /tmp/pkg-main-gs):
conflict in GracefulStop's tail (engine/server/server.go), 04cce2201e's
join of the accumulator against A4's join of adapterStopErr, resolved to
`errors.Join(err, adapterStopErr, dbCloseErr)` / `errors.Join(err,
adapterStopErr, ctx.Err())`; two test-helper collisions with A4's
engine/server/remote_cache_test.go now on main (boundedContext and
newGracefulStopServer, both byte-identical): dropped from
graceful_stop_test.go, folded into the first commit as rebase adaptations.

E12/E13 → sipsma/builtin-image-blob-lifetime (/tmp/pkg-e12) on
b831de5b6a: seven cherry-picks (535165c6a7, ab9db370a0, 5193483904,
3c448d99d9, fcf5245821, 54cf1fbd51, b347418163), two resolutions both in
engine/snapshots/testutil/store.go (main's ed7a4a47f9 helper keeps the
shared `observed` content wrapper for the in-place applier and differ;
the source's `observedSnapshotter` wrapper and `BuiltinContent: s.Builtin`
added beside it); messages rewritten under the main-PR rules (no
workstream vocabulary, originating commit named at the end, no attribution
trailers, Erik signoff), trees unchanged by the rewrite (tree 0ee94185e35c
before and after). Runs on the pre-rewrite tip 037dde8663 (same tree),
head file first: `go test -v -count=1 -timeout 60s ./engine/snapshots/
./engine/server/ ./core/ ./dagql/` → all ok, 1324 PASS, 0 FAIL, 0 SKIP
(/tmp/pkg-e12-tests.{head,log}); build and vet clean; lint-all on the
final tip cdb0aca2ec running (/tmp/pkg-e12-lint.{head,log}).
#14231 pushed at aac0c40269 (19:38Z, lease on dce5557471; MERGEABLE):
engine/server once → ok 3.250s, 161 PASS, 0 FAIL, 0 SKIP
(/tmp/pkg-14231-main-tests.{head,log}); lint-all DONE [3m28s], 0
findings (/tmp/pkg-14231-main-lint.{head,log}); range-diff
/tmp/pkg-14231-main-rangediff.txt: 1 context-only, 1 equal.
E12/E13: five lint findings (goimports ×4, duplicate image-spec import)
folded into 435bbeb994; tip 381e345d02; runs
/tmp/pkg-e12-tests2.{head,log} (1324 PASS, 0 FAIL, 0 top-level SKIP,
one inherited nested), /tmp/pkg-e12-tests2-schema.log (170 PASS),
/tmp/pkg-e12-lint2.{head,log} DONE [2m10s] 0 findings; sent to the
reviewer.
Main reruns (/tmp/pkg-main-reruns.log): 9282dfa127 python-312:slow
success; test-container errored again on
TestContainer/TestSystemProxies/git/GitLab_public (110 s, trace
532e2dbec5d5914a, no proxy/registry lines; main's network-dependent test,
untouched by the stack); its one rerun is spent. b831de5b6a's two reruns
in flight. Slop-pass review sent to the coordinator; items 2 and 4 ruled
as follow-ups on 40a0eee986 (items 1 and 3 to Erik).
E12/E13 381e345d02 approved by the reviewer (all seven sources accounted
for; per-file lines match apart from the declared store-wrapper and
import adaptations; 1,324 + 170 PASS, one inherited nested skip; lint
DONE 2m10s). Record correction: the alias reconciliation in
core/schema/lazy_resolver_cleanup_test.go also changes three `ocispec.`
references to `ocispecs.`, not only the import block. Coordinator
publishes. 9282dfa127 superseded by b831de5b6a; its test-container
(TestSystemProxies/git/GitLab_public) stays red as a main-owned network
flake, no further rerun (ruling).
E12/E13 published by the coordinator as #14264
(sipsma/builtin-image-blob-lifetime at 381e345d02, seven commits, base
main); under the watch and the merge rule with #14231, #14248, #14263.
The E-series is fully placed: E9 #14263, E12/E13 #14264, E15 in #14229
(merged with the stack). outstanding-work §3 updated below.

Cloud-runner cross-PR source fault (ruling, coordinator to Erik): the
19:39–19:42Z check runs on #14231 (aac0c40269) and #14264 (381e345d02)
failed the engine build with `util/fsxutil/gitignore_matcher.go:13:2: no
required module provides package github.com/go-git/go-git/v6/...`;
neither tree, nor main b831de5b6a, has go-git v6 (all import v5 at that
line, go.mod v5.19.0); the only source in the repo with that import is
the open PR #14256 "chore: migrate go-git to v6" (fix/go-git-worktreeconfig),
so those runs built another PR's source. Traces: #14231 test-modules
a51b7f8adb8ed6e945ef0e86d60103db, test-call-and-shell
bd1b19ab2a150cac49be948183151d9e, test-telemetry
d8c9dfeb24743a8b0299d6ec41f2f077, java-client:test
2a6efe6c363a7f856864b5c4a766b2a8, python-310:slow
b839822729150c3ef3b5384e1fb0f6fa, java-client:release-dry-run
7bd9cdad29f1511dd396a64a044b701c; #14264 test-base
714ab6d7e778d952d07cab4c006ee143. Reruns once each, staggered
(/tmp/pkg-crosspr-reruns.sh, log /tmp/pkg-crosspr-reruns.log).
b831de5b6a's delegated reruns of golang:test-all and test-provision
passed (registry answered).

## A6 slop-pass follow-ups (items 2 and 4), branch pkg/a6-slop on 40a0eee986

8f7f715f9d "core/integration: arm, await and release fixture barriers
through the harness": armedBarrier with armBarrier/await/release/
releaseAtCleanup on the harness; the three `await` copies and the inline
waits replaced; the two "walk every arrival" loops return the holding
barrier; the controls test keeps its raw calls where the protocol itself is
under test; 9 files, +118/−135; no barrier request, point, selector or
occurrence changed; two one-minute wait bounds (the fault cases) now share
the two-minute bound. A first application regrouped imports in ten
untouched files (goimports -local); reverted and re-applied with plain
goimports so only the touched files change.

bfa1452ab0 "dagql: name part observations with a closed kind set and one
observe entry point": TransferFixturePartKind with fourteen constants
beside the event; observePart(row, address, partObservation{kind,
snapshotID, source, detail}) replaces the six recording helpers; the
selected/installed sites build their observation with one switch each;
harness filters and two kind-keyed maps typed; every literal in the suite
and in the dagql, core and core/schema unit tests replaced by the
constant (the capture test's "settled" value is not a kind and stays);
29 files, +236/−212; JSON unchanged.

Runs on bfa1452ab0, head files first (dirty=0): `go test -v -count=1
-timeout 60s ./core/ ./dagql/ ./engine/server/ ./engine/snapshots/
./engine/fixturetransport/` → all ok (25.370s, 16.789s, 3.642s, 9.475s,
0.005s), 1340 PASS, 0 FAIL, 0 top-level SKIP plus one inherited nested;
core/schema at 120 s ok 13.569s, 173 PASS; `go vet ./core/integration/`
clean (/tmp/pkg-a6-slop-{tests,schema}.*). lint-all and the
shared-engine run of the whole suite (`dagger call engine-dev test --pkg
./core/integration --run '^TestRemoteCacheTransferSuite$' --timeout 45m
--test-verbose`, /tmp/pkg-a6-slop-suite.{head,log}) in progress.

Other: #14241 at 40a0eee986 test-base failed TestPartImportChainRefCleanupHandoff/collect
(cache_part_chain_lifetime_test.go:281, trace 9ca0a7ba23b7eed4068117682bce68bc),
A3's test, escalated to the coordinator for the analyst, not rerun.
#14263 (62e596517f) test-base "Cancelled - max execution time"
(19b1e6d79e62f6a1, no FAIL line): one delegated rerun issued 19:56Z.
main b831de5b6a: all 90 statuses success after the two delegated reruns.
Slop-pass lint on bfa1452ab0: one finding, unparam on the failTreeChain
closure's unused `t` (git_trees_test.go:230, unused since armBarrier
requires on the engine's t); parameter dropped, folded into the barrier
commit; tips now 2376a78c3d (barriers) and dbf28bec46 (kinds); lint-all
on dbf28bec46 DONE [38.8s], 0 findings (/tmp/pkg-a6-slop-lint2.{head,log}).
The suite run in progress started at bfa1452ab0 (/tmp/pkg-a6-slop-suite.head);
the only difference to dbf28bec46 is that closure's signature, so
TestGitTrees is re-run at dbf28bec46 once the full run ends.
Cross-PR reruns: #14231's six all passed (19:58Z); #14264's test-base
rerun issued 19:58Z.
#14231 test-base (033057b5a4a9f6e2): TestSnapshotSharingDonorReceivesSibling,
dagql/cache_snapshot_sharing_test.go:382 expected 2 actual 1; A5's test,
now main's, untouched by #14231; first failure logged here; one delegated
rerun 20:00Z; flagged to the coordinator for the analyst as the third
timing-dependent outcome in the merged sharing/lifetime family.
Shared-engine suite run at bfa1452ab0 (/tmp/pkg-a6-slop-suite.{head,log},
19:56:37Z–20:07:52Z, trace 387e24d57142e4997a47b426580e3b4b, full logs
/tmp/pkg-a6-slop-suite-full.log): `ok core/integration 424.107s`;
TestRemoteCacheTransferSuite PASS with 16 top-level subtests PASS
(EncodedRestart, FixtureControls, GitTrees, HTTPRestore, HostInputs,
Offers, PartMixedExecOutputs 95.43s, PendingOffersRestart 167.28s,
Pipeline, Renewal, SchemaRecovery 110.12s, SchemaRecoveryCold 110.10s,
SharedHostDirectoryLifetime 95.36s, SharingDonorRestart, SharingFinish,
WorkspaceCapture 215.74s), 80 nested PASS, 0 FAIL, 1 SKIP
(TestDefaultGCPruneDiagnostic, opt-in). TestGitTrees at the final tip
dbf28bec46 running (/tmp/pkg-a6-slop-gittrees.{head,log}).
TestGitTrees at dbf28bec46 through the shared engine
(/tmp/pkg-a6-slop-gittrees.{head,log}, trace efadf358419f72ad02770e6c10204170,
full logs /tmp/pkg-a6-slop-gittrees-full.log): PASS with Local 135.44s,
LocalBundle 96.28s, LocalCleaned 87.64s, RemoteDownload 105.29s,
RemoteFallback 107.46s; `ok core/integration 135.555s`. Both follow-ups
(2376a78c3d, dbf28bec46) sent to the reviewer.
#14231 test-base rerun (82d214a08a8b3bfe): TestSnapshotSharingDonorReceivesSibling
failed again identically (two of two on aac0c40269); delegated rerun
spent; escalated as reproducible on main's merged sharing code.
#14264 test-base cross-PR rerun ended "Cancelled - max execution time
exceeded" at 20:29Z (no failing test; core/integration had no result at
the limit, as on #14051 earlier); one rerun under the max-execution
delegation. #14263's test-base rerun still pending.
Reviewer on the slop pair: 2376a78c3d correct in every checked respect;
B1 on dbf28bec46: the delegation source (allocation, path clone) and the
skip's cause.Error() were built before observePart's gate
(cache_part_demand.go:238, cache_part_install.go:477,
cache_snapshot_sharing.go:809), regressing the disabled path from one
atomic load. Fix as a follow-up: partObservation carries the proof and
the cause; observePart builds source/detail after the gate; regression
TestObservePartOffGateDoesNoWork (AllocsPerRun 0 and no Error() call while
disabled; source and detail present when enabled). Record corrections:
the suite counts are 1 parent + 16 direct + 64 deeper (80 nested), and
the controls test's discarded-generation release uses never.release()
(the old generation, asserting "not armed"), the raw calls remaining for
the malformed-record and illegal-action probes.
#14266 (sipsma/sharing-and-import-cleanup-test-waits at 2204b4069a, two
test commits on main) published by the coordinator: the sibling-cohort
and chain-cleanup test fixes; under the watch and the merge rule. The two
flakes are recorded as fixed on main pending merge:
TestSnapshotSharingDonorReceivesSibling (traces 033057b5a4a9f6e2ba23825a55ad404e,
82d214a08a8b3bfe76797f8e02c6f918 on #14231) and
TestPartImportChainRefCleanupHandoff/collect (9ca0a7ba23b7eed4068117682bce68bc
on #14241). #14231's spent test-base rerun stays red until #14266 merges
and #14231 rebases.
B1 follow-up 02ce73c6f5 "dagql: build a part observation's source and
detail only past the gate": partObservation carries the proof and the
cause; observePart builds source/detail after the gate; regression
TestObservePartOffGateDoesNoWork (sequential: AllocsPerRun forbids
parallel). Control on dbf28bec46's fields fails with "Should be zero, but
was 2" (/tmp/pkg-gate-control.log). Runs on 02ce73c6f5, head files
first: dagql once at 60 s ok 11.809s, 531 PASS, 0 FAIL, one inherited
nested SKIP, the regression PASS (/tmp/pkg-a6-slop-b1-tests.{head,log});
lint-all DONE [3m21s], 0 findings (/tmp/pkg-a6-slop-b1-lint.{head,log}).
Candidate 2376a78c3d, dbf28bec46, 02ce73c6f5 to the reviewer. Watch
re-armed with #14266.
Reviewer approved the slop-pass line 2376a78c3d → dbf28bec46 →
02ce73c6f5 for #14241 (B1 closed; serial regression justified; prior
engine evidence keeps its bfa1452ab0 / final-tip GitTrees provenance).
Coordinator pushes with lease on 40a0eee986.
#14241 pushed by the coordinator at 02ce73c6f5 (84 commits, lease on
40a0eee986). Description updated by REST PATCH (read back identical apart from
GitHub's trailing newline): a
"Follow-ups" section naming the two cleanups (barrier helpers; closed
observation kinds with the gate-first entry point) with their runs, and
the validation numbers refreshed to the post-rebase head (1513 top-level
PASS, no top-level skip, one inherited nested). CI section unchanged (the
shard's measured runs 13m14s, 13m6s, 9m26s are within its statement).
Its test-base stays at risk until #14266 merges and #14241 rebases.
First CI run of #14241 at 02ce73c6f5: test-split:test-cache-persistence
(trace 50470f8d7df035b3d663b028cd0a896b, errored at 1m3s) and
test-split:test-module-runtimes (e5e58959ade0f7071c3f04aee66df893, 47 s)
both carry the Cloud-runner cross-PR source fault (the go-git v6 import
error from #14256's source, four "go-git/v6" lines in each log:
/tmp/pkg-ci-14241-test-split-test-{cache-persistence,module-runtimes}.log).
Per the coordinator's ruling on that fault, a script
(/tmp/pkg-14241-crosspr.sh, log /tmp/pkg-14241-crosspr.log) waits for
the run to settle, then reruns once each non-passing check whose log
shows the v6 import and leaves any other failure alone. Reported.
#14266 at 2204b4069a: test-split:test-container failed on
TestContainer/TestSaveHostDocker/tcp_driver (trace
5701a4067859b30e4bff3258c8e31a9f; container_test.go:6084 "Received
unexpected error: exit code: 1"; no registry, proxy or go-git lines,
/tmp/pkg-ci-14266-test-container.log). container_test.go is untouched by
#14266's two commits (no diff against upstream/main); test-container
passes on the neighbouring heads #14263 and #14264 in the same window.
Standing delegation applied: one rerun issued at 20:44Z
(dagger cloud -W github.com/dagger/dagger@2204b4069a rerun --check
test-split:test-container). Reported to the coordinator.
#14241 at 02ce73c6f5, same run: test-split:test-container failed on
TestContainer/TestSystemGoProxy (trace 456101143124aa0a47a843cca8855480,
container_test.go "Received unexpected error: exit code: 1";
/tmp/pkg-ci-14241-test-container-02ce.log has no go-git v6 line, and its
only registry.dagger.io lines are the engine:dev docker-tag steps, not
errors). The test is main's Go module proxy test; #14241 does not touch
container_test.go (no diff b831de5b6a..02ce73c6f5), and test-container
passes on #14263 and #14264 in the same window (#14266's delegated rerun
of the same check is pending). Standing delegation applied: one rerun
issued (dagger cloud -W github.com/dagger/dagger@02ce73c6f5 rerun --check
test-split:test-container). The cross-PR script leaves this check alone
by design (no v6 line) and reruns the two cross-PR errors once the run
settles.
#14241 at 02ce73c6f5, first run settled 21:01:16Z. Outcomes read from the
API and the logs:
- test-split:test-container: the first failure (trace
  456101143124aa0a47a843cca8855480) and the delegated rerun
  (b5e18d0b1d3496ad19a03a341489e85d) both fail in
  TestContainer/TestSystemGoProxy at its `go test -c -o ./test
  ./core/integration` step with a Go module proxy read error
  ("read https://proxy.golang.org/golang.org/x/text/@v/v0.40.0.zip:
  stream error: stream ID 247; INTERNAL_ERROR; received from peer", then
  x/sys v0.47.0 in the rerun; whole-trace logs
  /tmp/pkg-ci-14241-test-container-{4561,b5e1}-trace.log). The Go module
  proxy fault seen on main earlier; the delegated rerun is spent, so a
  further rerun is the coordinator's call.
- test-split:test-call-and-shell (60894cd4da4691d63f69e062984cbd89):
  TestDaggerCMD/TestShellAutocomplete fails loading ./wolfi's dependency
  "alpine": its runtime build `go build -ldflags -s -w -o /runtime .`
  exited 1 while `go: downloading` lines were still being emitted; the
  compiler's output is in neither the check log nor the whole trace.
  modules/, sdk/go/ and internal/cmd/dagger/ are untouched by #14241;
  the check passed on main's last five first-parent heads and on #14263,
  #14264 and #14266. Delegated rerun issued 20:56Z.
- test-split:test-base (2b4f5359bcad00b452da3c061f1f16b0): one failure,
  TestSnapshotSharingDonorReceivesSibling, the flake #14266 fixes; left
  red as expected until #14266 merges and #14241 rebases.
- cross-PR script: reran test-cache-persistence (21:01:49Z) and
  test-module-runtimes (21:02:27Z); left test-base alone.
Test-base cancellations on the main PRs (all "Cancelled - max execution
time exceeded"; ok counts are per-package result lines; #14248's passing
run ecea4012b521687106651b4bef236860 has 64):
- #14263 (62e596517f): 19b1e6d79e62f6a1c70867eaa6e93449 (19:51Z) and the
  rerun 06a534110692958585264cb98383ec4a (20:54Z): 66 ok, no FAIL,
  core/integration ok in 754 s and 949 s; the dagql package has no
  result line in either. Rerun spent.
- #14264 (381e345d02): 714ab6d7e778d952d07cab4c006ee143 was the cross-PR
  fault (40.9 s); bf3ae1cef470acfd7d748dcc283cf12e cancelled with 66 ok,
  no FAIL, dagql missing; ee23f81f00e5c96d9c266e7ce5b04e5b cancelled with
  66 ok, dagql ok, core/integration unfinished and one FAIL,
  TestDirectory/TestSearch/binary_files_are_skipped. Reruns spent.
- #14266 (2204b4069a): f7af35d5ab303806ef0e7a0421ec207a cancelled with
  65 ok, dagql and core/integration unfinished, and one FAIL,
  TestRemoteCacheTransferSuite/TestSharedHostDirectoryLifetime (61 s).
  Not rerun by me: a failing test, and the suite is stack code.
dagql never finishing in four of the five cancelled runs: the package's
waits on main are bounded (10 s selects with t.Fatal), so a hang would
be inside the cache under test; locally on 62e596517f the package is ok
in 2.736 s at -timeout 60s (/tmp/pkg-e9-dagql-hangcheck.{head,log},
dirty=0). A ten-iteration loop on main b831de5b6a (six plain, four at
GOMAXPROCS=2, -timeout 60s each, detached worktree /tmp/pkg-main-b831,
head file /tmp/pkg-main-dagql-loop.head) is running; log
/tmp/pkg-main-dagql-loop.log.
Three rulings from the coordinator (21:05Z): (1) the dagql no-result in
test-base is the priority: keep the local loop, look for dagql's test
spans in the cancelled traces, hand the analyst the dump or the
last-running test; (2) cherry-pick 23a0b18c59 + 48476414db onto
upstream/main as branch sipsma/sharing-probe-diagnostics-on-restored-reads
(adapt if the files differ, dagql once at 60 s, reviewer, then the
coordinator); (3) one more TestSystemGoProxy rerun on #14241, gated on an
unrelated PR passing test-container after the proxy fault.
Ruling (3), a mistake of mine: the gate script
(/tmp/pkg-14241-goproxy-gate.sh) fired at 21:10:01Z and issued the
test-container rerun on 02ce73c6f5 without the gate being met: its jq
printed "#14266 null" for an empty match and the null check compared the
wrong string. No unrelated test-container success had been created after
the 20:54:23Z failure (the latest was #14266's at 20:50:05Z). Reported to
the coordinator. The rerun passed; so did the delegated
test-call-and-shell rerun and the two cross-PR reruns. #14241 at
02ce73c6f5 now has only test-base red (the sibling flake, expected).
Ruling (1): the cancelled traces carry no per-test record for dagql
(test-base runs otelgotest without -v and dagql's tests do not use
testctx, so only per-package result lines exist; the engine-dev runner's
testVerbose option would add -v for the whole shard). The loop on main
b831de5b6a (/tmp/pkg-main-dagql-loop.{head,log}, dirty=0): six plain
iterations ok in 11.8–13.7 s; GOMAXPROCS=2 iteration 1 "panic: test
timed out after 1m0s", iterations 2–4 ok. Dump extracted to
/tmp/pkg-main-dagql-hang-dump.txt: running tests
TestPartAdmittedChainLifetime (56s) / output-backref-rejected (55s); the
subtest failed require.Nil(c.resultsByID[row.id]) at
cache_part_chain_lifetime_test.go:169 under c.egraphMu.RLock() (:168),
FailNow ran the cleanups on that goroutine, and CloseDiscardingPersistence
→ closeSnapshotSharing (cache_snapshot_sharing.go:456) blocks on
c.egraphMu.Lock() behind that read lock; the lazy task's worker
(releasePartRow, cache_part_task.go:188, from runLazyTask cache.go:4471)
also waits for the lock, so the row was still owned when the test
looked, the worker-release race #14266 fixes for the chain-cleanup test,
here in the admitted-chain test. Handed to the coordinator; the analyst's
turn was busy on four attempts, delivery pending.
Ruling (2): worktree /tmp/pkg-lifetime on branch
sipsma/sharing-probe-diagnostics-on-restored-reads from b831de5b6a; both
commits apply without conflict (cherry-pick -x, then messages rewritten
in the main-PR form with "Originates from remote-cache commit <hash>.",
no cherry-pick line). Adaptation: main's TransferFixturePartEvent has no
Detail field and partFixtureEvents returns one value; the regression's
two-value call and its Detail assertion were dropped (the cause is
asserted through the barrier's skip causes). Head ebea2c4724, dirty=0:
lint-all DONE [2m13s], 0 findings (/tmp/pkg-lifetime-lint.{head,log});
go vet ./core/integration ok; dagql once at 60 s
(/tmp/pkg-lifetime-dagql.{head,log}): 514 PASS, 1 FAIL, one inherited
nested SKIP. The FAIL is the cherry-picked regression
TestSnapshotSharingCompletedRowCaptureRefusal: it expects two part
events (share-skipped, owner-sync) and main records one (owner-sync),
because the share-skipped part event is A6's 37ddaee52c ("record a
sharing pass's skipped slots in the fixture report"); main has only the
testShareSkipped hook. Not sent to the reviewer.
Also: the failure the ruling is based on is not the one these commits
change. #14266's test-base (f7af35d5ab303806ef0e7a0421ec207a) fails
TestSharedHostDirectoryLifetime at remote_cache_sharing_test.go:129,
"the imported row takes its snapshot exactly once: map[]" (expected 1,
actual 0: the report has no part event for the imported row's snapshot
part at all), while 23a0b18c59 changes the restored-read assertion at
:157–172 (passing over share-skipped). A6 does not change the :129
assertion beyond the kind constants. Escalated for a design call.
Coordinator's ruling on (2): option (a), the branch is dropped; the
tolerance reaches main with #14241. Worktree /tmp/pkg-lifetime removed and
the local branch deleted (never pushed; upstream has no such head). The
:129 failure is the analyst's next item after the seventh: sent with the
trace (f7af35d5ab303806ef0e7a0421ec207a), the check log and the excerpt
from the test's RUN line to its FAIL line with engine log lines removed
(/tmp/pkg-14266-lifetime-129-excerpt.txt, 312 lines); the log has no
fixture report dump, since the test logs nothing before that assertion.
The earlier failure of the same test (be50f19793457ac431dc2cc43d99d7c6,
:167 share-skipped) was a different assertion. The dagql hang dump went
to the analyst in the same message.
Correction: the message to the analyst (hang dump plus the :129 item) has
not been delivered; six attempts since 21:14Z were refused with
"target_busy" (the analyst's turn is running). It is queued on my side;
the coordinator has the same content and the file paths.
The coordinator relayed both artifacts to the analyst (steer delivery
works while a turn is busy); my retries stopped at eight. Watch state at
21:21Z from the API: #14248 fully green awaiting approval; #14241,
#14231, #14263, #14264 red only on test-base (sibling flake / spent
reruns after max-execution cancellations); #14266 now APPROVED with its
test-base red (cancelled at max execution with the :129 failure), so the
merge rule does not admit it yet.
#14263 (62e596517f) APPROVED at about 21:25Z; test-base red (two
max-execution cancellations, no failing test, dagql without a result
line both times: the hang reproduced on main). Reruns spent; the merge
rule does not admit it as written, since the red check is not
registry-caused. Reported for the coordinator's call.
#14264 (381e345d02) APPROVED as well; test-base red (two max-execution
cancellations after the cross-PR error; the second carries the
main-owned TestDirectory/TestSearch/binary_files_are_skipped failure).
Same standing as #14263: reruns spent, awaiting the coordinator's call.
Ruling: the dagql hang joins the merge exemption, narrowly: a test-base
cancelled at max execution with no failing test and dagql without a
result line, on a head whose diff does not touch the hanging test's
path, does not block a merge; each use recorded with the trace ids.
#14263 merged under it at 21:31Z: plain merge, head pinned
62e596517fd3ce7cd070261352862ac4d51a02e2, main first-parent 47413897b2
("Merge pull request #14263 from dagger/sipsma/dagql-null-row-dependency-edges").
Exemption use: test-base traces 19b1e6d79e62f6a1c70867eaa6e93449 and
06a534110692958585264cb98383ec4a (66 ok, no FAIL, dagql without a result
line in both); the PR's files per the GitHub API are dagql/cache.go and
dagql/cache_null_dependencies_test.go, not the admitted-chain test. (A
local diff I ran against /tmp/pkg-e9 before the merge listed hundreds of
files and was wrong; the API file list is the check that counts.)
#14264: the coordinator ruled its second cancelled run's failing main test
(TestDirectory/TestSearch/binary_files_are_skipped, untouched by #14264)
keeps the exemption from applying as is; one more test-base rerun
authorized and issued at 21:33Z on 381e345d02. If it cancels with no
failing test, the exemption applies and it merges after #14263, head
pinned. #14266 waits for the hang commits and the :129 analysis. Merges
one at a time.
#14248 (localcache active wait, be2ae11fac) approved by grouville (MEMBER)
at 21:35:31Z with every check passing (85 pass, one skipping, none
registry-exempt), so the merge rule is met as written: merged at 21:37Z,
plain merge, head pinned be2ae11fac13e5bc5eab09eec4fae6bd2cd604cc, main
first-parent 206eaa57c2 ("Merge pull request #14248 from
dagger/sipsma/localcache-active-wait"), after #14263's merge completed
(one at a time). Main is now b831de5b6a → 47413897b2 → 206eaa57c2; the
monitor covers both new heads. #14264's test-base rerun still pending.
Main watch: 206eaa57c2 (#14248's merge) fully green at 21:58Z (83 of 83
contexts reported). 47413897b2 (#14263's merge) green on 82 contexts
with test-base still pending at 21:59Z, as is #14264's authorized
test-base rerun (issued 21:33Z); both are near the 30-minute limit that
the dagql hang turns into a cancellation.
#14266 pushed by the coordinator at 35a493cac6 (four commits on
b831de5b6a; approval stands per the API): 5edcaa5267 "test(dagql): join
admitted chain cleanup before observing collection" (session barrier for
both sessions; row-presence and owner-count snapshotted under egraphMu
and asserted after unlocking) and 35a493cac6 "test(dagql): assert cache
observations after releasing locks" (245 sites in 23 files, per-file
counts in the message; locked validators return errors; no production
change). Description appended by REST PATCH with a section "Why
test-base cancelled with dagql reporting nothing" (the deadlock
mechanism, the two commits, the reviewer's caveat naming the three
remaining test-local observation-mutex sites dagql/cache_test.go:1524,
:7455 and dagql/dagql_test.go:3263, none holding a cache lock, and the
evidence: main reproduces on repetition 50 at GOMAXPROCS=2; fixed tree
514 PASS once and ten times at GOMAXPROCS=2). Read back identical apart
from GitHub's trailing newline. Instruction: merge when green under the
rule, plain merge, head pinned; a test-base failure only on the :129
lifetime flake (fix in progress as a separate PR) gets one delegated
rerun, a recurrence goes to the coordinator; then #14231 and #14241
rebase onto main. Run on 35a493cac6 started 22:00Z (83 pending).
#14264's authorized test-base rerun (trace 7eda3b98caaef466b19c3b748ee71904):
"Cancelled - max execution time exceeded", 66 ok, no FAIL,
core/integration ok in 889 s, dagql without a result line. The
exemption's conditions hold: no failing test, dagql missing, and the
PR's files (core/builtincontainer.go, engine/snapshots/*, engine/server/*,
dagql/cache_snapshot_persistence_test.go and other tests) do not touch
the admitted-chain test. Every other check passes; grouville's approval
stands. Merged at 22:05Z under the exemption: plain merge, head pinned
381e345d0240bfb8cbd0e61c510aac6cd51ecc81, main first-parent 446aafb0dc
("Merge pull request #14264 from dagger/sipsma/builtin-image-blob-lifetime"),
after #14248's merge had completed. Exemption uses on #14264: traces
bf3ae1cef470acfd7d748dcc283cf12e (first cancel) and
7eda3b98caaef466b19c3b748ee71904 (this run); the middle run
ee23f81f00e5c96d9c266e7ce5b04e5b carried the TestSearch failure and did
not qualify. Main: b831de5b6a → 47413897b2 → 206eaa57c2 → 446aafb0dc.
#14266 at 35a493cac6, first run: golangci-lint:lint-all FAILED (trace
23b7913b1eebcba050df89aee521aac2), one finding:
dagql/cache_egraph_indexes_test.go:27:1 gocyclo, complexity 39 of
cacheDerivedIndexesErrorLocked (> 30), the sweep's error-returning
validator. Stack code, not rerun; reported. Fix candidate on a detached
worktree /tmp/pkg-14266-lint: follow-up commit 50bc7c7988 "test(dagql):
keep the derived-index validator whole under gocyclo", main's-form
//nolint:gocyclo with the reason on the function line (as
cmd/codegen/generator/go/templates/module_objects.go:14 carries it);
lint-all and dagql at 60 s running with head files first
(/tmp/pkg-14266-lint-fix-{lint,dagql}.{head,log}).
Main 47413897b2's test-base: "Cancelled - max execution time exceeded"
(trace 34d5feda9d60e6bc3df182a8e1be8103; 66 ok, no FAIL, dagql without a
result line: the pre-fix hang). 206eaa57c2's test-base succeeded in
21m5s (e229a0e15d9bbf8614e0654b145a572c), so 47413897b2 is superseded and
stays red as 9282dfa127 did. 446aafb0dc's run underway.
Coordinator: split, not nolint. The nolint candidate 50bc7c7988 was
discarded (reset; its lint-all DONE 0 findings and dagql 514 PASS at
/tmp/pkg-14266-lint-fix-* stand as a record of what was not used).
Split candidate 302f29572b "test(dagql): split the derived-index
validator by index family" on 35a493cac6: six helpers, one per index
family, each returning the first inconsistency; the outer function calls
them in the existing order; checks and messages unchanged; one file, 57
insertions, 4 deletions. Runs on 302f29572b, head files first, dirty=0:
lint-all DONE [50.2s], 0 findings (/tmp/pkg-14266-split-lint.{head,log});
dagql once at 60 s, 514 PASS, 0 FAIL, one inherited nested SKIP
(/tmp/pkg-14266-split-dagql.{head,log}); go vet ok. Sent to the reviewer
as the lint fix on the approved sweep; on approval I push with lease on
35a493cac6 and merge when green. Main 206eaa57c2 fully green (83);
446aafb0dc 79 green, 2 pending.
Reviewer approved 302f29572b (six bodies preserve every check, message,
order and first-error return; survivor time still sampled after the
inverse check; lock ownership with the caller; one test file; Erik
signoff; verified clean-head logs). Pushed by me at 22:17Z with lease on
35a493cac6: #14266 head 302f29572b, five commits, approval stands per the
API; its run starts. Merge when green under the rule, head pinned.
#14270 (sipsma/sharing-exact-receiver-demand at 10d896a1a4 on
b831de5b6a, "test: demand the exact receiver in snapshot sharing
lifetime coverage"; files core/integration/remote_cache_sharing_test.go,
core/schema/remote_cache_fixture{,_test}.go,
dagql/cache_transfer_fixture.go, dagql/cache_transfer_fixture_evaluate_test.go)
published by the coordinator: added to the watch and the merge rule. The
:129 lifetime flake (TestSharedHostDirectoryLifetime, trace
f7af35d5ab303806ef0e7a0421ec207a) is recorded as fixed on main pending
its merge. When #14266 and #14270 are both merged, #14231 and #14241
rebase once onto that main.
Main 446aafb0dc (#14264's merge) fully green at about 22:24Z (82 of 82
contexts reported). #14266 at 302f29572b and #14270 at 10d896a1a4 both
running with no failures yet.
#14270 at 10d896a1a4, first run, two early errors:
test-split:test-module-runtimes (trace 7882a7266b01aee65e9acdd3c8fa9429,
49.8 s) carries the Cloud-runner cross-PR go-git v6 fault (two v6 lines);
test-split:test-cache-persistence (7162a3b8135d29c37dc7735eab24eb93,
1m54s) fails its build step on the Go module proxy: engine/telemetry/labels.go:21:2
github.com/google/go-github/v59@v59.0.0: read
".../@v/v59.0.0.zip": stream error: stream ID 107; INTERNAL_ERROR;
received from peer. Script /tmp/pkg-14270-reruns.sh (log
/tmp/pkg-14270-reruns.log): after the run settles, one rerun of
test-module-runtimes under the cross-PR ruling; then one rerun of
test-cache-persistence gated on an unrelated head's
test-cache-persistence success created after 22:26Z (the gate style of
the TestSystemGoProxy ruling; the jq now prints nothing on an empty
match, the earlier script's error). Reported.
#14266 at 302f29572b: python-client:python-312:slow errored at 4m3s
(trace 7c23cb52aad16cc69cf7a0874a646ec5): the SDK test's container load
fails on the Go module proxy ("proxy.golang.org/@v/v1.49.0.zip: stream
error: stream ID 1; INTERNAL_ERROR; received from peer"); no python file
in #14266; the check passes on #14270 and main 446aafb0dc. The proxy
fault is ongoing (22:26Z on #14270, 22:31Z here). Gated rerun queued via
/tmp/pkg-gated-rerun.sh (log /tmp/pkg-14266-py312-gate.log): after the
run settles, once an unrelated head shows a python-312:slow success
created after 22:31Z.
Main 446aafb0dc's test-base errored at 17m2s (trace
62ea46dab3d6e6fbe5bd31b9827931fe): one failing test,
TestSnapshotSharingDonorReceivesSibling, the sibling flake whose fix is
in #14266 (pending merge); 66 packages ok, core/integration 787 s. Main
head at the time, so not superseded: one delegated rerun issued (the
main-owned flake, fix pending) and recorded.
Correction: the "22:26Z" and "22:31Z" fault times above were read from
my clock estimate, not the statuses; the actual status times are at or
before 22:25Z (the host clock read 22:25Z after main's rerun). The gates
keep those later bounds, which only makes them stricter.
#14270 APPROVED (per the API) while its run has three checks pending and
the two queued infrastructure reruns outstanding; merges under the rule
once those are green, after #14266.
#14266 at 302f29572b, run settled: test-base errored at 17m59s (trace
7b9e9bc9938dcb6fb33f880f5193bc6f), 66 ok, dagql finished in 38.9 s (the
hang fix held) with one failing test:
TestOfferSettlementReplacement/acceptance_after_commit_is_already_complete,
cache_offer_matrix_test.go:304 "the older slot waits for settlement":
expected 1, actual 0. Not the :129 flake, so no delegated rerun. The
sweep touched that assertion, but as a pure hoist: the read of
len(partOffers) stays under the same RLock at the same point after
OfferParts returns OfferAlreadyComplete, and only the require moved past
the RUnlock (old b831de5b6a lines 273-275 vs new 301-304). First
sighting of this test in the record. Reported for the coordinator's
call. python-312:slow still gated (run settled 22:35:53Z, no unrelated
success yet). #14270: one pending, its script waits for settle. Main
446aafb0dc's test-base rerun pending.
Ruling on #14266's test-base: one rerun now, documented cause "latent
offers-test race made visible by the sweep (previously it would have
deadlocked under the RLock), analyst item nine open"; the analyst has it
on a branch off 302f29572b, and if their fix lands before the rerun
result it goes onto #14266 too. Rerun issued on 302f29572b.
#14266 pushed by the coordinator at 0b19a16fda (six commits; lease on
302f29572b; approval stands): "test: hold offer settlement at the
acquisition body boundary" (the fixture's OwnerSyncReady signal is one of
two the chain installer can open, so settlement was never actually held;
the body is now held after commit with a bounded wait and released after
the admission-closed/old-slot check; cleanup closes the channel and joins
the acquisition even after a failed assertion; offer count and final
assertions unchanged; one file, 32 insertions, 9 deletions). Description
appended with one sentence on that hold (trace
7b9e9bc9938dcb6fb33f880f5193bc6f) before the validation line of the new
section. The test-base rerun issued on 302f29572b at 22:39Z stays
pending on that superseded head; the CLI offers no cancel (checked
`dagger cloud --help`), and the new head's run replaces it. The
python-312:slow gate script was keyed on 302f29572b and will decline its
rerun; if 0b19a16fda's run hits the proxy fault again, a new gate is
armed. Merge when green under the rule, head pinned 0b19a16fda.
#14270 at 10d896a1a4, run settled 22:48Z: test-base "Cancelled - max
execution time exceeded" (trace 19c12bccae15ee25742800d7151d838a), 66
ok, no FAIL, dagql ok, core/integration without a result line (the suite
runs inside test-base on main-based heads, near the limit). Standing
delegation (max-execution cancellation with no failing test): one rerun
issued. The script's two reruns went out at 22:48Z:
test-module-runtimes under the cross-PR ruling, and
test-cache-persistence with the gate genuinely met: #14243's
test-cache-persistence success created 22:32:33Z, after the fault. (The
script's log line printed the head sha instead of the matching PR; the
gate check itself was verified afterwards by hand.)
#14270's test-cache-persistence rerun (22:48:51Z, trace
6022fb7967239b6e58d5063f018d730d) errored in 6.7 s on the cross-PR
go-git v6 fault (four v6 lines; the first failure was the proxy). One
rerun per documented cause: the proxy rerun is spent, the cross-PR cause
gets its one rerun once the test-module-runtimes rerun (pending) is no
longer running. #14266 at 0b19a16fda: test-module-runtimes errored at
43.7 s (trace d66fc1719fdc79e21a7c74356567bf12, four v6 lines), the
cross-PR fault; one rerun after its run settles. Both queued in
/tmp/pkg-crosspr-2.sh (log /tmp/pkg-crosspr-2.log).
Coordinator's instruction on #14270's test-base: the cancellation is the
other hang class (core/integration not finishing, the engine-side stall
the investigator characterised); #14270 changes an integration test, so
if the rerun cancels again with core/integration unfinished it is not
merged on the exemption: the open spans at cancellation (which
integration tests were running) go to the coordinator and the analyst.
If it finishes, proceed as ruled.
Open spans at cancellation, method: `dagger trace <trace> --test
TestRemoteCacheTransferSuite` renders the suite's subtests with ∅ for
spans that never ended (script /tmp/pkg-open-spans.sh; the check-level
rendering only shows per-package counts, and the logs carry no per-test
lines). #14270's first cancelled test-base (19c12bccae15ee25742800d7151d838a):
core/integration counted 43 passed; the suite (∅ 23m57s) had four
subtests passed and TestSchemaRecovery open at 21m56s, with its outer
`.dagger-cli session` span open 21m56s and its "before" (16m17s) and
"after" (16m16s) spans open, while every rendered step under it had
completed, the last being the two Service.stop calls (16.2 s, 16.1 s);
TestSchemaRecoveryCold (2m60s) and TestSharedHostDirectoryLifetime
(1m30s) passed. In the earlier core/integration cancellations
(#14264's ee23f81f, #14266's f7af35d5, #14264's 7eda3b98) the suite had
completed in under six minutes, so those were other tests. #14270
changes the fixture files the suite uses (dagql/cache_transfer_fixture.go,
core/schema/remote_cache_fixture.go), so this is reported now rather
than only after the rerun.
#14270's delegated test-base rerun (22:50Z) errored in 23.1 s (trace
e42790b0622bdb77057e4be1992013e9) on the cross-PR go-git v6 build fault
(two v6 lines); it ran no tests. The cross-PR cause is a separate
documented cause, so test-base gets one rerun for it once the
cache-persistence rerun (issued 22:58Z by the earlier script, cross-PR
cause) is no longer pending (/tmp/pkg-14270-testbase-crosspr.{sh,log}).
Its test-module-runtimes rerun passed.
Main 446aafb0dc's delegated test-base rerun (trace
ab2d9be0530f092289e30acca77d7190): "Cancelled - max execution time
exceeded"; the check log fetch returned nothing, and the trace's check
view counts dagql at 84 passed against 516 in a finished run while the
suite view shows all six suite subtests passed: the dagql hang class
(main has no fix until #14266 merges). Rerun spent; left red, the fix
pending. The check view's per-package counts do not indicate openness
(the earlier #14270 run also showed "core/integration 43 passed" with
TestSchemaRecovery open); only the --test view does.
