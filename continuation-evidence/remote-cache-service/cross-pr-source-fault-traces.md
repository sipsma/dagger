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

## Engine mapping (added 2026-09-22 06:15Z)

Method: `dagger-namespace-resolve.sh --lookup-type trace_id` under flock for each faulting trace and for each of #14256's check traces (91 with trace ids; 90 resolved, the `load` check bdb30381… unresolved by Godmode). The resolver's engine_id is the durable key; its instance_id is the engine's latest instance, not the one at trace time (confirmed by the investigator against raw namespace_instances), so instances are omitted here. #14256 (fix/go-git-worktreeconfig, one commit fe5dadece4) ran all its checks on 2026-09-20 09:36–09:46Z and was closed; nothing of it ran within a day of any fault.

| Fault trace | PR / head | Check | When | Engine | #14256 runs on the same engine (check @ time) |
| --- | --- | --- | --- | --- | --- |
| a51b7f8adb8ed6e945ef0e86d60103db | #14231 aac0c40269 | test-split:test-modules | 09-21 19:39–19:42 | b3daa210-4166-461a-b6f6-0bbd5ffcc923 | test-split:test-base@2026-09-20T09:38:09Z |
| bd1b19ab2a150cac49be948183151d9e | #14231 aac0c40269 | test-split:test-call-and-shell | 09-21 19:39–19:42 | cd356970-9f55-4fd5-b643-14e9003f2d71 | test-split:test-call-and-shell@2026-09-20T09:38:09Z; test-split:test-telemetry@2026-09-20T09:38:09Z |
| d8c9dfeb24743a8b0299d6ec41f2f077 | #14231 aac0c40269 | test-split:test-telemetry | 09-21 19:39–19:42 | cd356970-9f55-4fd5-b643-14e9003f2d71 | test-split:test-call-and-shell@2026-09-20T09:38:09Z; test-split:test-telemetry@2026-09-20T09:38:09Z |
| 2a6efe6c363a7f856864b5c4a766b2a8 | #14231 aac0c40269 | java-client:test | 09-21 19:39–19:42 | f26f8952-cf22-4ae4-a453-9c4dc1b38039 | changie:generate:up-to-date@2026-09-20T09:37:33Z; committer:check-rebase-helpers@2026-09-20T09:37:33Z; docs:references:up-to-date@2026-09-20T09:37:45Z; dotnet-client:csharpier@2026-09-20T09:37:45Z; typescript-client:client-library:up-to-date@2026-09-20T09:38:01Z; typescript-client:format:up-to-date@2026-09-20T09:38:05Z |
| b839822729150c3ef3b5384e1fb0f6fa | #14231 aac0c40269 | python-client:python-310:slow | 09-21 19:39–19:42 | f26f8952-cf22-4ae4-a453-9c4dc1b38039 | changie:generate:up-to-date@2026-09-20T09:37:33Z; committer:check-rebase-helpers@2026-09-20T09:37:33Z; docs:references:up-to-date@2026-09-20T09:37:45Z; dotnet-client:csharpier@2026-09-20T09:37:45Z; typescript-client:client-library:up-to-date@2026-09-20T09:38:01Z; typescript-client:format:up-to-date@2026-09-20T09:38:05Z |
| 7bd9cdad29f1511dd396a64a044b701c | #14231 aac0c40269 | java-client:release-dry-run | 09-21 19:39–19:42 | f26f8952-cf22-4ae4-a453-9c4dc1b38039 | changie:generate:up-to-date@2026-09-20T09:37:33Z; committer:check-rebase-helpers@2026-09-20T09:37:33Z; docs:references:up-to-date@2026-09-20T09:37:45Z; dotnet-client:csharpier@2026-09-20T09:37:45Z; typescript-client:client-library:up-to-date@2026-09-20T09:38:01Z; typescript-client:format:up-to-date@2026-09-20T09:38:05Z |
| 714ab6d7e778d952d07cab4c006ee143 | #14264 381e345d02 | test-split:test-base | 09-21 19:42 | cd8a9414-1ceb-4083-8ac2-5cf87b119478 | none |
| 50470f8d7df035b3d663b028cd0a896b | #14241 02ce73c6f5 | test-split:test-cache-persistence | 09-21 20:42 | 86571086-c179-474b-8aa3-bff1deef1eee | none |
| e5e58959ade0f7071c3f04aee66df893 | #14241 02ce73c6f5 | test-split:test-module-runtimes | 09-21 20:42 | 86571086-c179-474b-8aa3-bff1deef1eee | none |
| 7882a7266b01aee65e9acdd3c8fa9429 | #14270 10d896a1a4 | test-split:test-module-runtimes | 09-21 22:18 | cfd3d414-03b1-417e-8391-15270736e189 | test-split:test-cache-persistence@2026-09-20T09:44:22Z |
| 6022fb7967239b6e58d5063f018d730d | #14270 10d896a1a4 | test-split:test-cache-persistence (rerun) | 09-21 22:48 | f4f846cd-8341-4032-80ac-4999b9ae5fd3 | test-split:test-modules@2026-09-20T09:46:45Z |
| e42790b0622bdb77057e4be1992013e9 | #14270 10d896a1a4 | test-split:test-base (rerun) | 09-21 22:50 | f4f846cd-8341-4032-80ac-4999b9ae5fd3 | test-split:test-modules@2026-09-20T09:46:45Z |
| d66fc1719fdc79e21a7c74356567bf12 | #14266 0b19a16fda | test-split:test-module-runtimes | 09-21 22:5x | f4f846cd-8341-4032-80ac-4999b9ae5fd3 | test-split:test-modules@2026-09-20T09:46:45Z |
| 4d2d02b0f77d6b7281b32dc6d559ab00 | #14271 949d3ea685 | test-split:test-module-runtimes | 09-22 00:0x | 6b26c948-a2dc-41a3-b764-04898355d88a | none |
| 2b50dbfa88a19858566fb1f62a20c058 | main ee26234869 (#14265 merge) | test-split:test-module-runtimes | 09-21 23:58 | cd8a9414-1ceb-4083-8ac2-5cf87b119478 | none |
| 632488901857ec2b710192b7e64cec08 | #14275 c2aae9490c | test-split:test-module-runtimes | 09-22 02:0x | cfd3d414-03b1-417e-8391-15270736e189 | test-split:test-cache-persistence@2026-09-20T09:44:22Z |
| 7e9b01c240dfdf703d08ff771880c8a1 | #14278 a895ad02d6 | test-split:test-base | 09-22 03:22 | cfd3d414-03b1-417e-8391-15270736e189 | test-split:test-cache-persistence@2026-09-20T09:44:22Z |
| 293806d7cad47bd0b953d6d652742997 | #14279 0aa68a3c9c | test-split:test-module-runtimes | 09-22 05:40 | ee750b29-5ccf-44d1-934c-c7b5dd26f69d | none |
| f6920da3dadf2d14480d80af26e50e5f | #14279 0aa68a3c9c | test-split:test-module-runtimes (rerun) | 09-22 05:43 | e531dbf6-914e-4d55-9fcc-35fbc88a93b5 | test-split:test-module-runtimes@2026-09-20T09:38:09Z |

Summary: 13 of 19 faulting runs ran on an engine that had run a #14256 check about 34 hours earlier (engines b3daa210, cd356970, f26f8952, cfd3d414, f4f846cd, e531dbf6); 6 faulting runs (engines cd8a9414 ×2, 86571086 ×2, 6b26c948, ee750b29) ran on engines with no #14256 run among the 90 resolved. The #14256 checks on the shared engines were of mixed kinds (test-base, call-and-shell, telemetry, test-modules, cache-persistence, module-runtimes, and misc lint/docs checks), i.e. any #14256 check that built the engine there could have seeded that engine's build cache. #14256 ran on 29 distinct engines in total, so the six unmatched victim engines need another route (an engine that never ran #14256 still produced the v6 import), which the investigator's and analyst's Go import-index hypothesis would have to explain (a stat-keyed index shared beyond one engine, or a cache mount shared across engines). Raw resolutions: /tmp/pkg-crosspr-resolved.jsonl, /tmp/pkg-14256-resolved.jsonl; table builder /tmp/pkg-crosspr-table.py.

#14256 engines (29) with check counts and times:
- f26f8952-cf22-4ae4-a453-9c4dc1b38039 6 checks; first 2026-09-20T09:37:33Z last 2026-09-20T09:38:05Z
- d1c4f849-2cc2-4936-815b-6573c49de643 4 checks; first 2026-09-20T09:37:15Z last 2026-09-20T09:39:29Z
- 471bebfc-9d3a-49f2-9fc6-55b74bb4f277 4 checks; first 2026-09-20T09:37:33Z last 2026-09-20T09:37:45Z
- d99b7fa5-ff7a-43bc-94b7-bdf99caf2fb5 4 checks; first 2026-09-20T09:38:02Z last 2026-09-20T09:38:17Z
- d4dfbd14-8390-4012-886b-de7d6e247a6a 4 checks; first 2026-09-20T09:38:23Z last 2026-09-20T09:38:43Z
- 16366fa4-a1a4-4c5b-9ddd-a53dac76795d 4 checks; first 2026-09-20T09:38:17Z last 2026-09-20T09:38:19Z
- b7a6bdef-6fbb-445b-9248-88424522cd81 4 checks; first 2026-09-20T09:38:23Z last 2026-09-20T09:38:37Z
- b925c565-ef8a-4b5e-b555-ead4ff721b13 4 checks; first 2026-09-20T09:37:52Z last 2026-09-20T09:41:31Z
- 0491bffb-b918-4c56-aaa3-70b1667620af 4 checks; first 2026-09-20T09:38:22Z last 2026-09-20T09:38:27Z
- 85dcd6b9-abf2-43f1-af41-17cf9204d870 4 checks; first 2026-09-20T09:39:29Z last 2026-09-20T09:40:17Z
- a32d60ea-39b3-4871-81e9-5b78018a113e 4 checks; first 2026-09-20T09:38:17Z last 2026-09-20T09:38:27Z
- 33b95e63-faef-4ebc-a302-f428de7be231 4 checks; first 2026-09-20T09:38:03Z last 2026-09-20T09:39:08Z
- 1bb64efe-54ae-4530-9ed9-9f3e93a990b0 4 checks; first 2026-09-20T09:38:24Z last 2026-09-20T09:38:27Z
- a8e3f4d9-927a-448d-8c99-10fe72821045 4 checks; first 2026-09-20T09:37:33Z last 2026-09-20T09:40:19Z
- 1c42f530-a6d6-48a0-bb4e-267168aa15c6 4 checks; first 2026-09-20T09:38:36Z last 2026-09-20T09:38:41Z
- 35a93e23-ad8c-4f5b-a86e-86469b7de315 4 checks; first 2026-09-20T09:38:03Z last 2026-09-20T09:39:32Z
- 0c1c6e3b-dd0a-430f-856b-f84b31067574 4 checks; first 2026-09-20T09:37:52Z last 2026-09-20T09:37:52Z
- 4ea5da2c-3fd8-4c11-9853-37e844c5a2de 3 checks; first 2026-09-20T09:38:20Z last 2026-09-20T09:38:22Z
- c893d4e3-73cf-4e45-92ba-c45790a108f5 3 checks; first 2026-09-20T09:42:25Z last 2026-09-20T09:45:50Z
- 7665a73c-4ca8-4716-aeac-29f9e2ba791c 3 checks; first 2026-09-20T09:40:14Z last 2026-09-20T09:41:13Z
- ab9f7220-fb07-4043-bdbb-742905bc53de 2 checks; first 2026-09-20T09:37:30Z last 2026-09-20T09:37:33Z
- cd356970-9f55-4fd5-b643-14e9003f2d71 2 checks; first 2026-09-20T09:38:09Z last 2026-09-20T09:38:09Z
- b3daa210-4166-461a-b6f6-0bbd5ffcc923 1 checks; first 2026-09-20T09:38:09Z last 2026-09-20T09:38:09Z
- a608d79f-4ced-4668-8386-736ba5d8dfbf 1 checks; first 2026-09-20T09:38:27Z last 2026-09-20T09:38:27Z
- 427e96dc-5987-42e5-817b-c7777bbd09fb 1 checks; first 2026-09-20T09:38:10Z last 2026-09-20T09:38:10Z
- e531dbf6-914e-4d55-9fcc-35fbc88a93b5 1 checks; first 2026-09-20T09:38:09Z last 2026-09-20T09:38:09Z
- f4f846cd-8341-4032-80ac-4999b9ae5fd3 1 checks; first 2026-09-20T09:46:45Z last 2026-09-20T09:46:45Z
- a973f65d-85c1-4157-b4ea-01c0f523d1d3 1 checks; first 2026-09-20T09:43:53Z last 2026-09-20T09:43:53Z
- cfd3d414-03b1-417e-8391-15270736e189 1 checks; first 2026-09-20T09:44:22Z last 2026-09-20T09:44:22Z
