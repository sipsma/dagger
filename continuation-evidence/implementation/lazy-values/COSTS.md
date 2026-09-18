# Measured costs

Measurements are single samples on Linux/amd64 under the current shared host load; they are not latency guarantees. Exact commands and outputs are in the command ledger and logs.

| Layout, bytes | Base | Current |
|---|---:|---:|
| LazyState | 32 | 32 |
| Directory | 264 | 248 |
| File | 264 | 248 |
| Container | 736 | 720 |
| Scratch operation | 32 | 32 |
| Directory mount operation | 248 | 248 |
| File mount operation | 248 | 248 |
| Builtin operation | 136 | 136 |

The identical `unsafe.Sizeof` probe ran at the unchanged base in an isolated worktree and at the final implementation. Its temporary base file was removed afterward. LazyState's atomic whole-operation latch does not enlarge the struct. Removing the extra live operation pointer saves 16 inline bytes per Directory/File/Container; retained operation inputs still keep their exact rows alive. This is struct layout, not a complete retained-heap measurement. There are 28 fewer `completedRecipe`-family references in Directory, File, Container and Container persistence, with none remaining in those files; this is a source count, not a measured CPU saving.

Unused scratch handle construction measured **459.1 ns/op, 392 B/op, five allocations**, with zero snapshot calls. Schema construction retains **8,120 schema bytes** and a **10,961-byte pending JSON payload** in the focused schema fixture; snapshot calls are zero at selection and one at first use. These bytes depend on the installed schema. Mount construction and metadata demand open zero snapshot handles; delegation first opens the required handles. The ownership tests count both releases and surviving parent/child owners, including failure and reversed release order.

HTTP pin acquisition averaged **3.276 ms** over 20 iterations. With the state mutex deliberately occupied for 20 ms, acquisition plus release took **28.209 ms**. This preserved sample predates R6's timestamp correction: its timer began just after releasing the worker, so it can omit time before that timestamp; the corrected test starts timing before release and was not rerun. The real pin path takes the snapshot-manager mutex while holding the state mutex; the measurement includes that acquisition, but does not synthesize separate manager-lock contention. Reuse can wait behind an HTTP resolution request holding the state mutex. Derivation remains outside it. The real-GC ownership test replaces state, synchronizes its lease, releases unrelated ownership and runs GC while derivation is paused; the independent pin keeps the matched bytes available.

The HTTP fixture retains two rows after its first public selection: HTTPState and the resolved File. Pending and completed hits in new sessions add zero result rows. A new layout name adds one File row. Each Git reference selection adds one result row, and its tree adds one more; direct fixed references have the same measured row deltas. The internal re-selection frames are present and validated, but the public aliases do not retain separate duplicate result rows. Thus the proposal's predicted extra physical row is not observed in this fixture. These counts measure `Cache.Size`, not the memory of alias equations or call terms.

The required cold SDK workflow still forces the whole builtin through its following metadata demand. Uniform operation representation removes the side mechanism; no saved builtin import work is claimed for that workflow.

| Existing acquisition cost | Current sample |
|---|---|
| Re-key 32 root leases | 136.819 ms; 32 new leases, 32 stale removals, one scan, peak 64 owners |
| Inline collector, width 1/depth 1 | 3.228 µs/op; 1,793 B/op; 17 allocations |
| Inline collector, width 128/depth 1 | 267.524 µs/op; 133,097 B/op; 1,185 allocations |
| Inline collector, width 8/depth 3 (512 outputs) | 1.837 ms/op; 882,079 B/op; 5,444 allocations |
| Concurrent inline publication, width 128 | 8,192 publications; 134 attempts, six retries, 128 accepted; 45.234 ms |
| Concurrent inline publication, nested 512 outputs | 8,192 publications; 134 attempts, six retries, 128 accepted; 244.489 ms |
| Native host callback / activated host | 0.623 ns / 1.682 µs; activated host 504 B and 13 allocations |

Delegation bookkeeping at depth 1, 8 and 32 measured **8.510 ms, 76.496 ms and 388.722 ms** respectively. Each level adds one independent pin, one owner synchronization and one accessor ref. The measured latency grows with the number of levels; the dedicated per-demand context path is not retained in PartDemandState.

Decision 1 adds no fields to these value layouts. Blocking owner synchronization reads each supported value's revision and links together, then reads them again for final revision validation; the second read allocates a temporary link slice. The earlier inline-collector measurements above exercise the preserved nonblocking collector, not a new throughput measurement of this blocking path. Contention waits on the existing mutexes; no timer, sleep or scheduling poll is used. A reader waiting on an incumbent body's or reader's mutex does not wake on its own cancellation until the holder releases it; the chosen design adds no notification mechanism. The deterministic race tests establish lock ordering, not a production latency guarantee.

Blocking owner reads occupy latches also used by nonblocking capture and acquisition probes, so those probes can defer more often while a read is held; no frequency measurement is claimed. R5 drops the pointer hold during a busy whole-body wait, allowing a live encoder to return not-ready promptly. The new regression measures ordering through deterministic events, not mutex throughput.
