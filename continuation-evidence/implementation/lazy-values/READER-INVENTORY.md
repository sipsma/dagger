# Lazy reader inventory

Base `018a0e695b96d0849118a510d359795812571e1b`: 375 matching lines (213 direct pointer matches, 162 helper matches) in 48 files. Current tree: 495 matches in 65 files.

The exhaustive machine-readable [inventory](reader-inventory.json) includes assignments and tests as well as readers. It records base line hashes and positions, current names, enclosing functions, current source lines and classifications. Historical names are normalized in the presentation; the source hashes are calculated before normalization. Deleted harnesses are marked. The scan uses the two patterns recorded in the JSON against every Go file, with one entry per matching line per pattern.

| Reader family | Final meaning and validation |
|---|---|
| Directory/File pending checks and detached clones | `IsEvaluated` plus available accessors; retained operations do not block a clone. Clone ownership and missing-accessor failures are tested. |
| Directory/File encoding and dependency attachment | Presence selects operation data, including evaluated operations. Restore-only wrappers retain their encoded operation and do not become a new recipe. |
| Container routing, whole completion and partial groups | The mutex protects operation lookup. `GroupConsumed` is authoritative for refined groups; whole operations use `IsEvaluated`. Bookkeeping still receives admission after body completion. |
| Container parent delegation | Evaluated parents are final; refined pending parents must have consumed metadata and the specific source group. Final-parent sweep remains inside the attempt. |
| Schema cloning and metadata consumers | Metadata is demanded before reading config, expansion, platform, owner or mount shape. Fresh accessors prevent constructor clones being overwritten by later delegation. |
| Acquired File/Directory and detached stored snapshots | May have no live Lazy pointer. Saved operation bytes remain available to private acquisition. A detached Container mount snapshot has no operation to retain. |
| Capture and persistence body latch | Retained operations do not erase the body/output revision boundary. Concurrent queries do not add revisions. |
| Tests and fixtures | Retention assertions replace completion-by-nil assertions; intentional nil fixtures still represent no operation or detached snapshots. |

The two patterns intentionally include constructor writes and unrelated similarly named functions. The per-occurrence enclosing function and source make these cases visible. The earlier field-recording fixture was removed; pending/evaluated cases replace it in the retained codec, attachment and persistence harnesses.

Metadata consumer audit: `from`, `withRootfs`, `rootfs`, `build`, `withSymlink`, `withWorkdir`, both mount fields, the cache dynamic-input hook and resolver, volume/temp mounts, mount removal, Directory/File selection, secret and socket fields, path writers/removers, and both legacy `asService` branches settle metadata before reading it. Direct builtin Directory/File consumers, config-normalized and fallback platforms, relative workdir and inherited owner have focused assertions.

Decision 1 ownership-read audit: Container owner synchronization takes the pointer and state latches with ordinary locks, skips completed groups, and releases both before waiting on a running group's latch. File/Directory retain output publication exclusion and release it before a pending-body wait; evaluated bodies are not waited on. Encoding reads its operation pointer before taking the state latch. Capture, Commit, boot and import continue using the nonblocking reader. The race checks cover reader/reader, reader/body and reader/encoder contention, root and inline selection, and quiescent shutdown.
