## Summary

Two test-only fixes for tests that failed once each in CI on main or on PRs that do not touch them. Each makes the test's own contract explicit instead of relying on a live external branch or on a UI callback's timing. No production code changes. Neither reproduces the failed CI run itself; where the recorded trace does not prove the exact cause, the section says so.

## SDK lockfile update: a test-owned git branch

`TestGenerators/TestSDKModuleClientUpdateRefreshesLockAndRegenerates` resolved a live public branch twice and required the same commit after refreshing a stale lock, so any merge to that branch between the two resolutions changed the pin and failed the assertion. The test now serves a test-owned Git repository, resolves its service IP for the non-nested engine session, and bounds the forced cleanup; the stale-pin, exact-resolved-commit and regenerated-client assertions are unchanged.

Evidence: the focused shared-engine integration subtest passed with the saved pre-commit patch; the initial fixture attempt failed on its short hostname and was corrected to use the existing service-IP helper. The recorded failure (test-base on #14248, trace `1c5c02b2958b7a372b5a189ce4c186cc`, `generators_test.go:659`, lock-file content differing) is explained by the mechanism but was not replayed, so this does not prove that run's exact cause.

## Headless agent focus: settle before the next key

`awaitFocus` returned as soon as the fake focus handler had recorded its new target, before the shell worker had queued the UI completion; stepping once then could leave `focusInFlight` set and `lastFocusedAgent` unset, and the next key waited on UI work the headless test never drives. The seven `awaitFocus` consumers now run in synctest bubbles: they wait for the workers to enqueue completion, step the UI, and assert settlement and the exact handler history, finishing the known key-highlight timers in virtual-time cleanup so no background worker outlives the bubble. The navigation, prompt, draft, roster, zoom and transcript assertions are preserved.

Evidence: the baseline pair reproduced one live-tree failure in 50 race repetitions; all seven corrected tests passed 50 race repetitions, and the full `dagql/idtui` package passed once with a 60-second timeout; the evidence is for the saved pre-commit patch, byte-identical to these test changes. The recorded failures (main f094ab5580's test-base, trace `ac7a9907c09524c5b553066d52a601b4`, `TestNavToggleReturnsToLastAgent`; #14231's test-base, trace `1f52fa2fb01e6eed553563f60202a4b7`, `TestLiveTreeFollowsFocusedAgent`; both `agent_focus_test.go:158` "waiting for focus [agent-scout agent-chief], got [agent-scout]") match the reproduced mechanism.

## Sharing cancellation: wait for the attempt's receiver release

`TestSnapshotSharingCancelDuringPreparation` cancels sharing while a preparation is parked in `PinSnapshot`, unblocks it, waits for the one-slot pass, and then compares exact ownership counts. The pass joins its `RunLazyTask` caller, which can return before the attempt worker has dropped its own separate receiver hold, so the receiver comparison could see three holds where the test's original two were expected; that is an observation before a delegated release, not leaked ownership on cancellation. The test now arms the existing bounded attempt-release hook after pair setup and waits on it once after the pass returns, before the ownership comparisons. The no-install, pin-balance, cancellation and donor/receiver assertions are unchanged.

Evidence: the focused test passed 50 repetitions before and after the change under `-race` with a 120-second timeout, and the full `dagql` package passed once with a 60-second timeout, on the saved pre-commit patch byte-identical to this change; the CI failure (main 50164dc9db's test-base, trace `7913f1583003bef7970ee6fe46d4f136`, `cache_snapshot_sharing_test.go:1127`, expected 2 actual 3) did not reproduce locally, so the diagnosis follows the source contract rather than a replay of that run.

## Validation

Each commit was reviewed on its own before joining this branch; `go vet ./core/integration/`, `go vet ./dagql/idtui/` and `go vet ./dagql/` pass on the tip. CI runs every package on the pushed head.

