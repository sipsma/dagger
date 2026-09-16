# HTTP request counts

The focused tests use a local origin and count every request. Outer resolution and the File operation are measured separately. The stored `__httpFile` frame contains all identity inputs and no HTTPState reference; its exact persisted dependencies are empty.

| Case | Outer GET/revalidation requests | Operation GETs | Result |
|---|---:|---:|---|
| Genuine internal miss, first use with matching local state | 1 | 0 | Saved bytes |
| Pending internal hit, matching state; new outer session | 1 | 0 | Saved bytes |
| Completed usable hit; new outer session revalidates | 1 | 0 | Same completed File |
| Pending internal hit, absent state; direct internal selection | 0 | 1 | Saved bytes, then zero GETs on completed reuse |
| Pending internal hit after a separate outer resolution advanced state | 1 | 1 | Saved bytes, state unchanged, then zero GETs on completed reuse |
| Pending internal hit, advanced state and changed origin body | 1 | 1 | Digest mismatch; no installed output; state unchanged |
| Imported File with matching offered chain | 0 on B | 0 on B | Chain read, one installation, zero Lazy entries |

The advanced-state rows count the separate outer resolution used to establish the mismatch. Internal selection itself performs no resolution. The chain fixture uses one GET on A to create the saved File; B makes none. Failure, cancellation, status, checksum, path/mode/time and cleanup cases are in the full HTTP selection. Local pin ownership is tested across real snapshot GC, including injected derivation/release failures. A snapshot no longer available locally is not treated as a usable completed hit.

See [HTTP checks](logs/http-core.log), [actual frame and hit checks](logs/operation-schema.log) and [pin latency](logs/http-costs.log). Engine HTTP results are recorded separately in the final command ledger.
