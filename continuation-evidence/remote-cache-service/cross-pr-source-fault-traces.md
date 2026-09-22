# Cloud-runner cross-PR source fault: trace list

Signature: a check errors within about 10–70 seconds, before any test runs, with the engine build failing on
`util/fsxutil/gitignore_matcher.go:13:2: no required module provides package github.com/go-git/go-git/v6/plumbing/format/gitignore`.
None of the affected heads, nor main at the time, imports go-git v6 (all import v5; go.mod v5.19.0); the only source in the
repository with that import is the open PR #14256 "chore: migrate go-git to v6" (fix/go-git-worktreeconfig). The runs
therefore built another PR's source. All times UTC, 2026-09-21/22.

"Verified" means the check's log fetched by `dagger cloud logs <trace>` carries the v6 import error (count of lines).
The first burst's traces were recorded from the coordinator's ruling at the time; their whole-trace logs fetched now
return empty or contain no build output, so they are listed on that ruling's authority.

| Trace | PR / head | Check | When | Verified |
| --- | --- | --- | --- | --- |
| a51b7f8adb8ed6e945ef0e86d60103db | #14231 aac0c40269 | test-split:test-modules | 09-21 19:39–19:42 | v6 lines: 2 |
| bd1b19ab2a150cac49be948183151d9e | #14231 aac0c40269 | test-split:test-call-and-shell | 09-21 19:39–19:42 | ruling; log now empty |
| d8c9dfeb24743a8b0299d6ec41f2f077 | #14231 aac0c40269 | test-split:test-telemetry | 09-21 19:39–19:42 | ruling; log now empty |
| 2a6efe6c363a7f856864b5c4a766b2a8 | #14231 aac0c40269 | java-client:test | 09-21 19:39–19:42 | ruling; log has no build output |
| b839822729150c3ef3b5384e1fb0f6fa | #14231 aac0c40269 | python-client:python-310:slow | 09-21 19:39–19:42 | ruling; log now empty |
| 7bd9cdad29f1511dd396a64a044b701c | #14231 aac0c40269 | java-client:release-dry-run | 09-21 19:39–19:42 | ruling; log has no build output |
| 714ab6d7e778d952d07cab4c006ee143 | #14264 381e345d02 | test-split:test-base | 09-21 19:42 | v6 lines: 2 |
| 50470f8d7df035b3d663b028cd0a896b | #14241 02ce73c6f5 | test-split:test-cache-persistence | 09-21 20:42 | v6 lines: 4 |
| e5e58959ade0f7071c3f04aee66df893 | #14241 02ce73c6f5 | test-split:test-module-runtimes | 09-21 20:42 | v6 lines: 4 |
| 7882a7266b01aee65e9acdd3c8fa9429 | #14270 10d896a1a4 | test-split:test-module-runtimes | 09-21 22:18 | v6 lines: 2 |
| 6022fb7967239b6e58d5063f018d730d | #14270 10d896a1a4 | test-split:test-cache-persistence (rerun) | 09-21 22:48 | v6 lines: 4 |
| e42790b0622bdb77057e4be1992013e9 | #14270 10d896a1a4 | test-split:test-base (rerun) | 09-21 22:50 | v6 lines: 2 |
| d66fc1719fdc79e21a7c74356567bf12 | #14266 0b19a16fda | test-split:test-module-runtimes | 09-21 22:5x | v6 lines: 4 |
| 4d2d02b0f77d6b7281b32dc6d559ab00 | #14271 949d3ea685 | test-split:test-module-runtimes | 09-22 00:0x | v6 lines: 4 |
| 2b50dbfa88a19858566fb1f62a20c058 | main ee26234869 (#14265 merge) | test-split:test-module-runtimes | 09-21 23:58 | v6 lines: 4 |
| 632488901857ec2b710192b7e64cec08 | #14275 c2aae9490c | test-split:test-module-runtimes | 09-22 02:0x | v6 lines: 4 |
| 7e9b01c240dfdf703d08ff771880c8a1 | #14278 a895ad02d6 | test-split:test-base | 09-22 03:22 | v6 lines: 4 |
| 293806d7cad47bd0b953d6d652742997 | #14279 0aa68a3c9c | test-split:test-module-runtimes | 09-22 05:40 | v6 lines: 4 |
| f6920da3dadf2d14480d80af26e50e5f | #14279 0aa68a3c9c | test-split:test-module-runtimes (rerun) | 09-22 05:43 | v6 lines: 2 |

Observations: the fault hits early build steps of shard checks (module-runtimes and cache-persistence most often, test-base
when its build is first), on PRs and on one main head; a rerun usually clears it, but #14279's rerun hit it again within
three minutes while main's own module-runtimes passed at 05:17Z.
