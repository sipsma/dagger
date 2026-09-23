## Summary

One test-only fix for a failure seen once, on an unrelated PR's CI run. It makes one test wait for the sharing queue to be quiet before it checks the queue, instead of treating a pass signal as proof that the queue has drained. No production code changes and no assertion is relaxed. The fix does not reproduce the failed CI run itself; the section says so.

## Restored-frame sharing: drain setup passes before checking the queue

`TestSnapshotSharingSelectsRestoredFrame` waits for three sharing passes through the test hook's buffered channel and then requires the sharing queue to be empty ("the graph lock is free again"). Each signal carries only a slot count: it does not say which pass produced it, and a pass is not a completion barrier for the queue. A lazy attempt closes its done channel and wakes its caller before it queues its sharing notification, and a notification that arrives after the worker took an item creates a separately held successor. The fixture's setup also queues work of its own through two synchronous digest teachings. So a setup pass still sitting in the buffered channel can satisfy the restored-frame wait while another item is still pending, and the final check then sees one pending item. The fixture now runs inside `synctest.Test`. After the live-frame and empty-successor passes it waits for quiescence and drains the remaining buffered setup passes, requiring each to have zero slots. Only then does it restore the frame and trigger sharing, and it waits for that work before reading the queue depth. The exact slot counts and the zero-pending assertion are unchanged, with no sleeps, polling, new hooks or relaxed counts, in `dagql/cache_snapshot_sharing_test.go`.

Evidence: the test passed 50 of 50 repetitions under `-race` with a 120-second timeout on 48c0b95791, both before and after the change (no local reproduction in the baseline), on a saved file byte-identical to this change. The full `dagql` package passed once with a 60-second timeout (527 top-level PASS, one inherited nested SKIP). The recorded failure (#14302's test-base, trace `69d0bf6f18941396d543e1d1cc680adf`, `cache_snapshot_sharing_test.go:1037`, "Should be zero, but was 1") did not reproduce locally, so the diagnosis follows the source contract rather than a replay of that run.

## Validation

Reviewed on its own before joining this branch; `go vet ./dagql/` passes on the tip. CI runs every package on the pushed head.
