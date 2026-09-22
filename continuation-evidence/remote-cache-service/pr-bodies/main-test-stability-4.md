## Summary

One test-only fix for a failure seen once, on an unrelated PR whose changes do not touch the test. It makes one subcase wait for a release it was already implicitly depending on, instead of taking an ownership baseline while a worker may still hold the parent. No production code changes and no assertion is relaxed. The fix does not reproduce the failed CI run itself; the section says so.

## Delegation parent holds: wait for the parent's preparation before the baseline

`TestPartDelegationRealStore/sync-retry` prepares the exact parent with `EvaluateParts`, reads the parent's incoming ownership count as a baseline, injects a child-sync owner failure, and checks the count again. `EvaluateParts` wakes its caller before the acquisition worker drops its own hold on the parent row, so the baseline could include that worker's hold; when the worker released it, the second read saw one fewer hold and the test reported "temporary parent hold survived failed child sync" with expected 2, actual 1. That is an observation before a deferred release, not a parent hold left behind by the failed sync. For this subcase the parent is now prepared in a separate client session and the test waits on the existing bounded `cachetest.ReleaseSessionAndWait` for that session before taking the baseline, in `core/part_delegation_test.go`. The exact-count comparison, the injected owner error, the retained child pin and the successful retry assertions are unchanged, and the other subcases prepare the parent as before.

Evidence: the subcase passed 50 of 50 repetitions under `-race` with a 120-second timeout on 232a80cbd3 both before and after the change (no local reproduction in the baseline), the "after" run on a saved file byte-identical to this change. The recorded failure (#14280's test-base at ad41b6afdd, trace `09a9beca8e95a89647ea25dfd8a95980`, `part_delegation_test.go:163`, expected 2 actual 1; its rerun passed) did not reproduce locally, so the diagnosis follows the source contract rather than a replay of that run.

## Validation

Reviewed on its own before joining this branch; `go vet ./core/` passes on the tip. CI runs every package on the pushed head.
