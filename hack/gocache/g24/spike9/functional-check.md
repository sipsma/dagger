# Spike 9 functional verification

Verified through a fresh dev engine on 2026-10-21, starting at host load1 12.58.
This is functional evidence only. No local timing comparison was performed.

The targeted `TestSpike9ResultStore` passed through `engine-dev test --pkg ./core --run '^TestSpike9ResultStore$' --ebpf`. The runner's `--ebpf` flag supplied the bind-mount capability needed by the real file digest path; no BPF program was requested. It covers concurrent equal/conflicting publications, non-sticky misses, namespace separation and persistence after cache restart.

The harness ran its helper protocol, hash-validation and package-correlation tests with the race detector in a golang container. Both plain and engine arms completed first/edit/no-change builds. All four first builds compiled `runtime`; all no-change builds had zero compiler commands. Both yq binaries passed the version smoke check. Dagger CLI binaries were compiled but not executed.

| Project | Phase | Gets | Hits | Misses | Puts | Put body bytes | Exported bytes | Package matches |
|---|---|---:|---:|---:|---:|---:|---:|---:|
| yq | first | 484 | 0 | 484 | 1010 | 147757568 | 0 | 1054/1494 |
| yq | edit | 747 | 742 | 5 | 9 | 6230944 | 141508033 | 532/756 |
| yq | noop | 749 | 748 | 1 | 1 | 42797 | 147708610 | 524/750 |
| dagger | first | 1986 | 0 | 1986 | 4816 | 944534482 | 0 | 5662/6802 |
| dagger | edit | 3456 | 3384 | 72 | 88 | 52403548 | 892296060 | 2848/3544 |
| dagger | noop | 3464 | 3463 | 1 | 1 | 1308 | 944554333 | 2828/3465 |

The separate fresh consumer engine recovered both original keys and verified the 114,688-byte payload. Its fixture report recorded one provider read, one installed chain and zero consumer puts. Both engines exited after the check. The ordinary result-cache persistence check also passed independently.

Report generation was checked using synthetic timing data: paired median differences and ratios for all three phases. The report suppresses timing comparisons when given functional outputs.

**Gate:** transferable Dagger result representation demonstrated with existing fixtures. Real remote-service lookup and pull latency, remote indexing and conflicting cross-engine publication remain unverified. No remote-cache endpoint or credentials were used.

Local evidence is under `/home/exedev/g24-scratch/codex/spike9/functional-2/`, with the targeted test log at `core-test-2.log` and synthetic reporting assertions under `report-synthetic/` in the same scratch directory. These are audit pointers only; the runner needs no files from this host.
