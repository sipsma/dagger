# Packaged publication branches, foundations and batches 1 to 6

Author B, batch 7. Built locally by `package.py` beside this file; **local refs only, nothing pushed, no pull request, no tag**. The branches carry the design's names under the prefix `b7-packaging/`, so that no ref with a publication name exists before the Human decides to publish. `packaged-1-6.json` has the full original-to-packaged SHA map (an omitted commit maps to `null`).

## How each branch is built

The integrated line is linear (no merges in any segment). For every original commit in order, the packaged commit's tree is the original commit's tree without the evidence entries, which are all top-level: `continuation-evidence/`, `cleanup-evidence/`, `cleanup-review-evidence/`, `CLEANUP-IMPLEMENTATION.md`. A commit whose tree then equals its packaged parent's is omitted: that is exactly an evidence-only commit. A mixed commit keeps its non-evidence paths. Author and author date are kept; the committer is set to the author and the author date, so a rebuild gives the same SHAs. Messages lose `Co-Authored-By` and generated-with lines; existing `Signed-off-by` lines are kept, de-duplicated and moved to the end, and a commit with none gets its author's.

## My reading of §8.1 where it leaves room

- **The foundations branch is built too.** Batch 1's parent must be F, the packaged foundation head, and F does not exist until that branch is constructed, so `transfer-foundations` is built first on the last existing PR head `a7d4bad229`: ten commits kept, `a2c01e5792` omitted, the final fix `1ca9f28a60` applied after `625c5a9b9e`'s mapped commit, as §8.1 orders. F is `93ee3e933b64927a76a4255f43c838dc032d528b`.
- **Batch 2 includes the b1-b2 integration.** Its segment is `a88e0e0cdd..77f6279559` on the integrated line: batch 2's commits as the integration author rebased them, plus the three integration follow-ups (`8d99a010af`, `17384b793f`, `77f6279559`), which §8.1 says to keep. The integration's evidence checkpoints on that line (`97ee3385fd`, `f035d2c2a3`, `cd06753d8f`) are omitted; its fourth, `a1a9ca9d2a`, is not an ancestor of the integrated line and so never enters.
- **Batch boundaries are the integrated parents**, not the source branches' original parents: `1ca9f28a60`, `a88e0e0cdd`, `77f6279559`, `fd9cfd55a9`, `a26dc93750`, `c5b299142c`. Batch 4 includes the lazy-values rework, which landed inside its segment.
- **Not done, because §8.1 leaves it to the Human's pre-publication view:** splitting batch 4's `core/object.go` commits into a PR of their own (the manifest's packaging note); they are in `b4-acquisition` in their original order.

## Branches

| Branch | Original parent..head | Kept | Omitted | Packaged head | Packaged tree |
| --- | --- | ---: | ---: | --- | --- |
| `b7-packaging/remote-cache/transfer-foundations` | `a7d4bad229..1ca9f28a60` | 10 | 1 | `93ee3e933b64927a76a4255f43c838dc032d528b` | `63920de01aa82e57dd6dbe4ccd99cd6bcb6b0b54` |
| `b7-packaging/remote-cache/b1-producers` | `1ca9f28a60..a88e0e0cdd` | 10 | 2 | `42a57419de184b607eb922863596cfb188bdce59` | `80a47c8746fa13e603ba40e0fec9d182a95c74c2` |
| `b7-packaging/remote-cache/b2-transfer` | `a88e0e0cdd..77f6279559` | 19 | 8 | `d46b43fc0a0aba18d41efe926fe6076179433543` | `5274009624056dff56dd4cce9fc7ec69d5e3a357` |
| `b7-packaging/remote-cache/b4-acquisition` | `77f6279559..fd9cfd55a9` | 44 | 12 | `a7605125014f19d2cba509f8e1e3f3fd21f0e2b7` | `c21f7ac730dd1c94f25aa4c887205e87826ffd8a` |
| `b7-packaging/remote-cache/b5-offers` | `fd9cfd55a9..a26dc93750` | 18 | 6 | `abb3ce9750ba7f085a1fed87501750abfe7a7e85` | `e1dcd52ee5a9191d438c0168011d1d46c93f2898` |
| `b7-packaging/remote-cache/b6-sharing` | `a26dc93750..c5b299142c` | 25 | 8 | `38583498cff7f68147b701b16c5077732cf22b43` | `0bf6f1075c819d9a8926825576d97f280169939a` |

Each branch's parent is the previous branch's packaged head; the first sits on `a7d4bad229`.

## Verification

- **Tree comparison.** For every branch, `git diff <original head> <packaged head> -- . ':!continuation-evidence' ':!cleanup-evidence' ':!cleanup-review-evidence' ':!CLEANUP-IMPLEMENTATION.md'` is empty, and the packaged tree has no evidence entry. Because every packaged commit's tree is its original's tree without evidence, each packaged commit's diff is its original's non-evidence diff.
- **Mixed commits.** `733cec2bb0` keeps `core/object.go` and `core/object_test.go`; `167630f0b7` keeps the three sharing test files; `aecadc5261` keeps `core/integration/remote_cache_sharing_test.go` and the two `dagql` sharing test files. For each, the packaged commit's blob changes equal the original's non-evidence blob changes exactly.
- **Counts** agree with `BATCHES-1-6.md`: 10, 10, 19, 44, 18, 25 kept; 1, 2, 8, 12, 6, 8 omitted.
- **Trailers.** All 126 packaged messages end with `Signed-off-by`, none contains `Co-Authored-By` or a generated-with line.
- **Buildable.** Each head checked out detached in my worktree: `go build ./...` and `go vet ./dagql ./core ./core/schema ./engine/snapshots` (which compiles their tests), process bounds 540 s and 300 s. All six succeeded, 54 to 101 s each. `go vet`'s one pre-existing complaint in `engine/server/session_attachables.go` is base debt and is not in these packages.
- **Not verified:** no test was run on a packaged head (the trees equal trees whose tests were run in their batches), and only the head of each branch was built, not every intermediate commit.

Batch 7's branch is built last, once author A's native cases are in.
