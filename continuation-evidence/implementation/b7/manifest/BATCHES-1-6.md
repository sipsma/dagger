# Stack manifest completion records, batches 1 to 6

Author B, batch 7. Generated from Git on the integrated line ending at the batch 6 head `c5b299142c`. The coordinator's `stack-manifest.json` stays the manifest; this is the input §8.1 of the batch 7 design asks for. A commit is **evidence-only** when every path it changes lies under `continuation-evidence/`, `cleanup-evidence/` or `cleanup-review-evidence/`; **mixed** when it changes those and anything else; otherwise **kept**. The message plays no part. The JSON beside this file has every commit with its full SHA, tree, parent, class and the non-evidence paths of each mixed commit.

## Batch 1: producers

Integrated parent `1ca9f28a60f1d9597c1b0df01e65a91707ce3b0f`, head `a88e0e0cdde5383d085347c55288ff84651239e2`, head tree `e9f805672be8effba712ea963307bfc61b5b2e51`. 12 commits: 10 kept, 0 mixed, 2 evidence-only. Packaged production and test diff: `git diff 1ca9f28a60 a88e0e0cdd -- . ':!continuation-evidence' ':!cleanup-evidence' ':!cleanup-review-evidence'`.

Evidence-only, omitted at packaging: `7eb998467e`, `a88e0e0cdd`.

Kept although the message begins with `docs`: `688bef3408`.

## Batch 2 and the b1-b2 integration: value transfer, schema recovery, foreign paths

Integrated parent `a88e0e0cdde5383d085347c55288ff84651239e2`, head `77f6279559061fd1bb6b3b18e6b08582c7b013a3`, head tree `04908f3ab099f2a94a5452802c5855770900636f`. 27 commits: 19 kept, 0 mixed, 8 evidence-only. Packaged production and test diff: `git diff a88e0e0cdd 77f6279559 -- . ':!continuation-evidence' ':!cleanup-evidence' ':!cleanup-review-evidence'`.

Evidence-only, omitted at packaging: `ba657b2f1e`, `26f6f71970`, `24f2a9cc66`, `410dc674c4`, `5483cfc495`, `97ee3385fd`, `f035d2c2a3`, `cd06753d8f`.

Kept although the message begins with `docs`: `56e40116ee`.

## Batch 4: per-part acquisition, including the lazy-values rework

Integrated parent `77f6279559061fd1bb6b3b18e6b08582c7b013a3`, head `fd9cfd55a98b176bf82cf84ee1b4c3ae1257fde5`, head tree `6c35b11d5fc89e2d2f0232caa7841d9bc33b9eee`. 56 commits: 43 kept, 1 mixed, 12 evidence-only. Packaged production and test diff: `git diff 77f6279559 fd9cfd55a9 -- . ':!continuation-evidence' ':!cleanup-evidence' ':!cleanup-review-evidence'`.

Evidence-only, omitted at packaging: `21234ff019`, `b4b84f5c60`, `fbd4013309`, `6d5cbe20a4`, `8d4b99c011`, `e1443fff63`, `018a0e695b`, `73e31042d3`, `7ae017d77f`, `378e0322c3`, `f0a18735d7`, `fd9cfd55a9`.

Mixed, to be split at packaging (keep the listed paths, drop the evidence):

- `733cec2bb0` fix(core): retain only ID-shaped module object fields (B4-D1): `core/object.go`, `core/object_test.go`

Kept although the message begins with `docs`: `d5aaf3e251`, `9ad8cbfb87`, `c01bbbb29b`, `f30f4ab780`.

## Batch 5: offers, renewal, offer ownership

Integrated parent `fd9cfd55a98b176bf82cf84ee1b4c3ae1257fde5`, head `a26dc93750e42daf2de76678b0e54f51454cea33`, head tree `d4865a0e749a94d7f2b3966665ed22b2dcd0b7c0`. 24 commits: 18 kept, 0 mixed, 6 evidence-only. Packaged production and test diff: `git diff fd9cfd55a9 a26dc93750 -- . ':!continuation-evidence' ':!cleanup-evidence' ':!cleanup-review-evidence'`.

Evidence-only, omitted at packaging: `d49e309bcd`, `f7af21e6f1`, `23b2787ac8`, `2d76465239`, `361ef7f789`, `a26dc93750`.

Kept although the message begins with `docs`: `2b564d9c9c`.

## Batch 6: early snapshot sharing

Integrated parent `a26dc93750e42daf2de76678b0e54f51454cea33`, head `c5b299142ca672cbd2ef0a389492f11de85ff08b`, head tree `9e4eb60b7bd9136a404ec1d8eeff753ccfc40757`. 33 commits: 23 kept, 2 mixed, 8 evidence-only. Packaged production and test diff: `git diff a26dc93750 c5b299142c -- . ':!continuation-evidence' ':!cleanup-evidence' ':!cleanup-review-evidence'`.

Evidence-only, omitted at packaging: `a0ce9c123d`, `3277121605`, `e509c41964`, `37631180d3`, `490c386592`, `e1755079db`, `fff95b21a0`, `c5b299142c`.

Mixed, to be split at packaging (keep the listed paths, drop the evidence):

- `167630f0b7` docs: record the batch 6 implementation and its verification: `dagql/cache_snapshot_sharing_family_test.go`, `dagql/cache_snapshot_sharing_test.go`, `engine/server/snapshot_sharing_test.go`
- `aecadc5261` core/integration: bound the restarted read and prove its attribution: `core/integration/remote_cache_sharing_test.go`, `dagql/cache_snapshot_sharing_family_test.go`, `dagql/cache_snapshot_sharing_test.go`

