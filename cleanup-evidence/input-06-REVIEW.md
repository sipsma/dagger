# Cleanup review of candidate 1ba150a79b8a3f7568e27a22cee492f578ffa538

Reviewer: forked scope auditor (Fable 5.1, xhigh), 13 September 2026. Role: audit designs and
implementations for unauthorized or unwanted changes. This review answers one question: is the
retained cleanup, as it stands in the immutable candidate, correct and complete? Technical
acceptance here is separate from permission to retain, which the Human has already given.

## Candidate

- Commit `1ba150a79b8a3f7568e27a22cee492f578ffa538`, tree `d5a73331e65629681246351a864fd2ecda9b899e`
  (both verified in my worktree; HEAD equals the candidate; tree clean).
- Reviewed delta: `a161ceb34c` → candidate, seven commits: docs/comment cleanup `8a8a4dfda2` and six
  implementation commits `c49db1291f`, `371c77af48`, `8d144785d4`, `5c3fe15eb6`, `c7e20ab5d4`,
  `1ba150a79b`. Also inspected: the whole delta from the pre-codec base `17f7dd89f4`, because
  completeness cannot be judged from the range alone.
- Excluded, and confirmed absent from the candidate: the dirty stack's four-file graph delta
  (`dagql/cache_persistence_graph.go`, its test, and the tracked edits to
  `cache_persistence_capture.go` and `cache_persistence_codec.go`). The stack at
  `/tmp/remote-cache-engine-implementation-20260911/stack` still shows exactly those four paths
  dirty; I read it only through git status and changed nothing there.

## Verdict

No blocking findings. No should-fix findings for cleanup correctness or completeness. Two
non-blocking observations are recorded below. The candidate contains no residue of the removed
equality, byte-observation, nested-forwarding or LLM/runtime work, no model or schema residue, and
the six implementation commits do not regress the local behavior they touch as far as unit tests,
vet and source inspection at the candidate show. What remains unfinished is future feature work,
listed separately, not a cleanup defect.

## What I read before judging

The coordinator takeover audit, the reconciled lead handoff and the former-lead audit, each in
full; the 38 direct Human messages in `takeover-evidence/human-instructions.md`; the preserved
original guidance (`index.txt`, `engine-foundations.txt`, `source.txt`); the repository's
`skills/engine-debugging/SKILL.md` in full before running or interpreting any test. No root
`CLAUDE.md` or `AGENTS.md` exists in this checkout. I kept the Human's instructions apart from
brainstorming and reviewer suggestions; the criteria applied are the Human's own: remove what was
invented under autonomy (13811, 13831, 13847), keep ordinary recipe joining (14151), the small
export-side extra-digest control (5369, 5465), original producer inputs for pending and completed
values (5209), the Changeset synthetic-call root fix the Human himself suggested (5077, 5658), no
fixes to unrelated local behavior (13811, guidance G24), default-deny for unconfirmed cases (G26).

## Coverage and evidence

### 1. Removed work is gone from the candidate

- Identifier search over `dagql`, `core`, `engine`, `hack`, `.dagger` at the candidate for every
  mechanism named in the removal map: `ResultDigestEvidence`, value classes, call associations,
  byte observations / requirements / binding proofs / construction bindings, nested recorders and
  read records, `sameEstablishedContent`, first-handle validation, derived identity: zero hits.
- Names that do appear are baseline: `RuntimeID` (4 hits, identical count at `17f7`: unrelated
  existing uses), `CollectedContent` (24 = 24), `resultDigestPosting` (13 = 13, the existing
  posting index, not batch 3's candidate indexing), `LLMRuntime|LLMSkill` (85 vs 84: the single
  extra line is `core/schema/persisted_test.go:114`, which asserts that `LLMSkill` has no codec,
  i.e. it records the exclusion).
- Removed commits are not ancestors of the candidate: `7e1978c32d` (batch-3 aggregate),
  `ebc06bc127` (batch-4 tip), `e0830e8834` (Changeset observation leaf), `c2d391286e` (nested model
  tip). They survive only under the three `archive/removed-remote-cache-*-20260913` refs.
- The model and storage formats are untouched since the pre-codec base: `git diff 17f7..candidate`
  over `dagql/tla`, `.dagger/modules/tla-check`, `dagql/persistdb` and `dagql/call/callpbv1` is
  empty; `cachePersistenceSchemaVersion` is `"19"`; no file outside `core/`, `dagql/` and
  `hack/designs/` changed between `17f7` and the candidate (no `go.mod`, `dagger.lock`, SDK or
  docs-site changes).
- The graph draft's identifiers (`PersistedGraph`, `ImportPersistedGraph`, `CapturePersistedGraph`,
  `PersistedRefCallID`) do not occur; `dagql/` contains no graph file; the committed
  `cache_persistence_capture.go` hashes to the reviewed `1bd14843…` (119 lines).

### 2. Documents

- The three plan documents the removal map deletes (`cache-byte-observations.md`,
  `cache-candidates-and-value-equality.md`, `llm-configuration-and-persistence.md`) are absent.
  The four rewrites and the trimmed `persisted-value-graphs.md` are present in reduced form (29 to
  35 lines each) and I read all five at the candidate.
- Every remaining occurrence of a removed-concept word in those five documents is an exclusion
  statement ("have been removed from this effort, including their models and future implementation
  obligations"; "No per-read conditions, value-evidence tables or first-handle byte validators";
  "adds no nested-client participation or read recorder"; "No read records, nested observation
  forwarding or LLM/MCP producer work"). No "deferred" LLM obligation remains; the term "replay",
  "hostile", "schema version 20/21" and "value class/association" do not occur.
- The docs commit `8a8a4dfda2` changes only the five design documents plus comment lines in three
  Go files; the non-comment diff of its Go changes is empty. Those comment edits replaced
  "deferred with the LLM conversation batch" with "have no persisted representation", which is the
  right wording under 13831.
- The five baseline documents that predate autonomy (`result-foundations.html`,
  `stage2-part-evaluation.md`, `directory-file-deferred-opening.md`, the two per-part HTML pages)
  are byte-identical to `17f7`.

### 3. The batch-1 codec base (`a161`) and LLM scope

`a161` is the retained codec base; its acceptance is historical and not re-litigated here. For
residue only: its LLM-related content is two mechanical interface changes on the pre-existing
`LLMTokenUsage` and `LLMVariable` codecs (`core/llm.go`, signature of encode/decode context) and
test lines asserting that `LLM`, `LLMContentBlock`, `LLMMessage`, `LLMSkill` are not persisted.
That is codec support for already-persistable types, not the removed LLM project.

### 4. The six implementation commits

Each commit's content equals the diff I reviewed before it was committed (my reviews 03, 05, 08,
10, 13 and 15 recorded the committed blob hashes). At the candidate I re-checked the invariants
that carry the Human's criteria:
- `c49db1291f`: no `newChangesetFromMerge` or `changeset_merge_output` in code; two internal
  fields produce the merged directory; the only remaining mention of the old synthetic op is a
  historical row in the baseline page `result-foundations.html` (observation 1).
- `371c77af48`: the exec-lazy encoder serializes `originalExecMD`; the runtime derivation and
  overwrite are unchanged.
- `8d144785d4`, `5c3fe15eb6`: completed rows keep producer bytes without decoding (`completedRecipeJSON`
  set only in the complete-row decode branches of Container, Directory and File); local
  missing-snapshot opening still errors; no ancestor decode.
- `c7e20ab5d4`: the `remote-cache` label has exactly one production use, the pinned
  `Container.from` identity (`core/schema/container.go:1247`); no protobuf, table, hash or lookup
  change.
- `1ba150a79b`: one-row capture as reviewed; no closure, no import, no policy.
No commit touches the e-graph joining rules; recipe digests join exactly as at `17f7` (14151).

### 5. Regressions (proportionate checks run at the candidate)

- `go test -p 1 ./dagql/call ./dagql ./core ./core/schema -count=1` at HEAD `1ba150a79b`:
  all four packages `ok` (0.008s, 2.373s, 2.477s, 8.433s), EXIT=0. Log:
  `/tmp/scope-auditor-cleanup-unit.log`.
- `go vet ./dagql/call ./dagql ./core ./core/schema ./core/integration` (compiles the integration
  test package that `c49` edited, without running it): EXIT=0, no diagnostics, at HEAD `1ba150a79b`. Log:
  `/tmp/scope-auditor-cleanup-vet.log`.
- I ran no native engine suites, no TLC, no generators. The coordinator report's validation table
  records the historical native evidence and its source-at-run limits; the ones that matter for
  this candidate are: the 28-case Changeset native run predates the final guard edits and only the
  restart subtest and the `generate_multiple` generator case ran on final `c49` source; the `a161`
  native restart case ran at `f69` plus the later query fix, not at the final successor; single-row
  capture has focused, package and race evidence at `1ba`. None of that demonstrates a connected
  cross-engine path, and none was claimed to.

## Observations (non-blocking)

1. `hack/designs/remote-cache/result-foundations.html` (a baseline page, unchanged since `17f7`)
   still has a table row describing `changeset_merge_output` as synthetic, with a permalink to the
   old code. `c49db1291f` replaced that op. The row is historical and accurate for its permalink,
   but a reader of the current tree may be misled. Bounded correction if the council wants one:
   one sentence in that row noting the replacement by the internal merge producers. Not a cleanup
   defect: the page predates autonomy and was outside the removal criteria.
2. The reduced documents carry status lines ("source cleanup complete; remaining foundations
   proceed privately", "one-row only, graph work remaining"). They are accurate as of the
   candidate. They should not be read as design approval for the remaining work.

## Future feature work, not cleanup defects

Graph capture and admission (the excluded draft and the two narrow post-anchor reviews are
evidence only), remote acquisition through existing lazy evaluation, from-image metadata check,
lookup preference (D15), offers for unstarted work and address refresh (5077), and the complete
design set review the Human's process requires. `live-cache-descriptions.md` and
`remote-cache-acquisition.md` say so themselves.

## Limits of this review

Read-only source inspection plus the unit tests and vet named above. No native engine runs, no
model runs, no rerun of historical validation. I did not re-audit `a161` beyond residue and LLM
scope. Council synthesis and the retention decision belong to the coordinator and the Human.
