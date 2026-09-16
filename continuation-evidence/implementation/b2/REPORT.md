# Batch 2: blocked at cold native module setup

Branch: `remote-cache-b2-transfer-implementer-26387947`.
Base: `1ca9f28a60f1d9597c1b0df01e65a91707ce3b0f`. The original first command reset
there; log/status confirmed the requested base and a clean tree. This resumption
preserved the implementation commits already on that base.

Steps 1–4 are implemented. Step 5 is a buildable checkpoint with passing focused
unit tests and a failing native acceptance case, **not a completed batch**.
The addendum `ba20b55292003fc7c9a41bf4fc44af12383e162b` resolves the previous
File/Directory capture blocker. Its permanent negative tests and the Container
control pass. The older capture-gap evidence remains historical.

## Decision required

The native `RemoteCacheTransferSuite/TestSchemaRecovery` reaches this boundary:

1. A loads and serves the real Go module, executes its report, and records one
   actual function entry.
2. A exports the report's metadata closure. The harness copies only the committed
   bundle between distinct fixture volumes; the engines have distinct state.
3. B imports successfully, with zero report entries.
4. B's first normal `ModuleSource(".").AsModule().Serve(ctx)` fails:
   `failed to call module "cache-probe" to get functions: call constructor: imported filesystem part is unavailable: Container.fs`.

See [the exact failure](native-cold-runtime-blocker.log) and
`core/integration/remote_cache_transfer_test.go:183`. This is before an ordinary
report call or a saved-handle contextual method. The imported closure includes
SDK/runtime values, and normal module setup encounters their pending filesystem
state. The error is consistent with the required batch-2 unavailable-part
boundary; the cold setup is not metadata-only in this run.

Focused design `7a998854cc3b2efc7f221ffb9ec6debf5ab3d44e`, §§9–10, requires this
before-normal-loading scenario while excluding acquisition and describing
metadata transfer as sufficient. Per the commission's stop-on-disagreement rule,
implementation stopped instead of changing lookup eligibility or adding an
acquisition mechanism.

Two options:

1. **Revise the standalone setup:** prepare B's SDK/module runtime through normal
   `AsModule` before import, without serving its schema or executing the measured
   report. Then import and serve B's operational Module. This tests import before
   schema installation; explicitly move the truly cold loading proof later.
2. **Retain the cold acceptance requirement:** keep this failing case and make
   its acceptance depend on integration with batch 4 acquisition. Steps 1–4 and
   the fixture remain prerequisites; do not declare standalone batch 2 complete
   from a warmed substitute.

No option was implemented.

## Signed commits, in order

- `de126ebe775d078a4c6e240d4d697d3bfdc6834c` — records, offer owners, visitors, lifetime/accounting and format cut.
- `85de1ed96d12d1b91589dfdba4368b7cbc512557` — foreign codecs, pending forms, part mapping and identity labels.
- `a9112444f626464590af400205bd39baf2cc00a6` — historical first-blocker evidence.
- `f72572cb9a264a23d56791185a60765213412d33` — authorized File/Directory guards and typed output revisions.
- `336de2cdb379323827f1be90ec32c9f548447bd4` — held export, atomic import, complete desired-role intent and decode publication.
- `74b24a43784c4e6e52ec5cbe599c3fc78ac36695` — installed-module schema preference and foreign-path guards.
- `ee7ccb765a3fb9b4d45f8f2efe5ba31952116ac7` — gated fixture, additional tests and native blocker checkpoint.
- This final separate evidence commit — report, inventories and logs.

## Verification and remaining work

[VERIFICATION.md](VERIFICATION.md) records the narrow sequential commands and
what they prove. The final DagQL selection, required targeted race selection,
core selection, schema reader/fixture tests, server reader test and production
build pass. Coverage includes concurrent owner replacement/export/collection,
import visibility and Close, fresh IDs, pending-owner persistence A→B→C,
foreign root/inline validation, every Module reference position, stable installed
candidate traversal, Workspace-backed subpath changes, foreign item removal,
exact fixture handles, repeated imports, concurrent counters and rooted paths.

The native case is blocked as above. Its later assertions have not been proved:
import after loading, ordinary B report hit, saved node/contextual B contents,
restart, both residual foreign-context cases, interface/custom-scalar recovery
and native bound-tool controls. Those additional native controls are not all
implemented. The existing local bound-tool defining-schema regression passes.
Real selected-chain peers compile but skip on this host for lack of read-only
bind-mount privileges; no real chain-byte result is claimed. Privileged host/Git/
Container/view coverage, redundant-final-offer restart coverage and the broader
native matrix remain acceptance work. The parallel batch-1 producer/HTTP changes
have not been imported into this private branch.

The complete writer list is part of this report in
[OUTPUT-WRITERS.md](OUTPUT-WRITERS.md); it includes every production publication
and private initializer found by the included AST audit. All four path/snapshot
accessor writes are centralized in guarded setters. Producer completion retains
the body latch after clearing Lazy. Capture checks typed output and encoded
representation revisions after copying. The exhaustive reader classification is
[FOREIGN-PATH-READERS.md](FOREIGN-PATH-READERS.md).

Review especially independent owner retention versus lookup requirements,
E/D/object lock ordering, decode publication against complete desired link maps,
installed operational Module selection, and the fixture's exact temporary holds.
Schema 20 / envelope 4 is a hard cut: older local checkpoints cold-start under
the existing reset policy, with no migration. Bundle version is 1.

No acquisition, offer scheduling, sharing, pushes, PRs, tags, infrastructure
changes, agent transcript reads or author/reviewer contact were performed.
