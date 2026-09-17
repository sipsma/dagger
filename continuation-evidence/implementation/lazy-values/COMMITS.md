# Commits

Unchanged base: `018a0e695b96d0849118a510d359795812571e1b`. All commits below are new and signed off. The seven implementation steps retain their requested order; the two explicitly requested status-only checkpoints are listed separately. No reviewed commit was amended.

| Commit | Kind | Change |
|---|---|---|
| `3cc02ac9701620d0d795751c31698554a7549a94` | Implementation 1 | core: retain evaluated Lazy operations |
| `782fc85d483905120932d237458fafa89c7f1fea` | Implementation 2 | http: select a File call with resolved content |
| `cfc26b0adee7643bf0b2027168a698ce97213680` | Implementation 3 | schema: construct the schema File with FileBlobLazy |
| `2d1086c5f743e4ed3e0a6ea7d256391e92fa6212` | Implementation 4 | git: construct lazy outputs from resolved calls |
| `fc9e44605ec91526b9d4c25aa503c7939567a524` | Implementation 5 | core: make scratch mounts and builtin lazy on every path |
| `daba57f3b74e98934108af1710db8649f58b3ecd` | Implementation 6 | Names, operationState and version cut; also deletes the earlier saved-filesystem design, adds lazy-values.md, rewrites the acquisition summary and adds historical pointer notices, detailed below. |
| `73e31042d3cb103496f6fb967e64fc76ecacd58b` | Status checkpoint | docs: checkpoint lazy values verification status |
| `7ae017d77f84a64114d53374d04494a01a2da429` | Status checkpoint | docs: record ownership guard contention and decision options |
| `f1b3ed3aa49ca250fff993c35345f8d3baa334ce` | Implementation 7 | cache: verify retained Lazy values and block ownership reads on real latches |
| `378e0322c3955e6cc2d626e6bcfccb0764c5ee07` | Earlier evidence | Report, command ledger, logs, manifests, complete cold closure, counters, reader inventory, request counts and measured costs. |
| `4a77b4487ede06bdfda9c154a1db4a41a3b280b3` | R5 | Release the pointer latch while waiting for whole Container bodies; deterministic race case with live capture and the gated diagnostic. |
| `d5aaf3e251a5535227952ebf5870c3b03d989cb0` | R6 and notes | DagQL completion contracts, metadata-consumer comment and HTTP cost timestamp order. |
| `9ad8cbfb8785ecd4eb773d3a244d966465b7aba8` | R4 | Persisted-input vocabulary and retained-pointer description in the two internal documents. |
| `c01bbbb29b56a44e17eb4785229612f011f6b6ef` | R1 | Acquisition summary explicitly restates the converged designs without adding requirements. |
| `f30f4ab780de7ee72832d4c3c176a6608baf1e46` | R7 | Cache model completion comment and two schema comment typos; other R7 comments were included with R6. |
| `313a82e5a4132700415d86d24b5d21f8c8d1659a` | R8 | One result-link traversal and direct collector mode; retain the revision-validation adapter. |
| This commit | R1, R2, R3, R6, R9 evidence and cost note | Documentation scope, completed-body guard rationale, new cold/mixed results and race log, complete base reader enumeration and mutex-wait cancellation cost. Earlier passed logs remain unchanged, with the two HTTP/Git statuses labeled beside their ledger links. |

The documentation changes in `daba57f3b7` are part of its scope, beyond code renames: the former 26-line saved-filesystem design was deleted (blob `0dd481da61e809041bf9970b2f28fc5bb0107f39`); [lazy-values.md](../../../hack/designs/remote-cache/lazy-values.md) was added; [remote-cache-acquisition.md](../../../hack/designs/remote-cache/remote-cache-acquisition.md) was rewritten; historical pointer notices were added to Directory/File deferred opening, Stage 2 part evaluation, and the part-evaluation, part-persistence and result-foundations HTML documents. The live-cache descriptions and remote-cache data flows also changed. R1 records these edits and clarifies the acquisition summary's status. [Report](REPORT.md).

The evidence commit SHA is returned with the final tip; it is not embedded recursively into its own content.
