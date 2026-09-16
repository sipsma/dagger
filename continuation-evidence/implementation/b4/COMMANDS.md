# Focused commands

Each test selection was run sequentially. No recursive package selection was used.

## Initial implementation commit

```sh
go test -race ./dagql -run '^Test(PartTask|PartGate|CacheEvaluate(RetiresFinishedAttemptBeforeWaitersDrain|OwnCancellationOnlyCancelsOwnWait|SettlesBookkeepingBeforeReportingComplete|PendingBookkeepingSkipsUnclearedCallback)|EvaluateParts(SiblingGroupsRunConcurrently|OneCallRunsGroupsConcurrently|SyncFailureRetriesOnlyBookkeepingPerGroup))' -count=1

go test -race ./core -run '^Test(PartPureRoutes|Container(DirectEvaluateRunsRefinedGroups|ConcurrentGroupCompletionClearsLazyOnce|RoutingReadsRaceRefinedClear|RoutingReadsRaceUnrefinedClear))$' -count=1

git diff --check
```

Final output is in `logs/step1-dagql-race.log` and `logs/step1-core-race.log`. Earlier focused non-race selections also passed; final race selections supersede them.

## Parent-only blocker probe

```sh
git worktree add --detach /tmp/dagger-b4-inline-probe-77f62795 77f6279559061fd1bb6b3b18e6b08582c7b013a3
```

Copy `probes/inline_snapshot_roles_test.go.txt` from this evidence directory to `core/b4_inline_contract_probe_test.go` in that detached worktree. In that worktree:

```sh
go test ./core -run '^TestB4InlineSnapshotRoleContract$' -count=1 -v
```

Exit status 1 is expected. The standalone control passes; link preservation and inline decode assertions fail. See `logs/inline-parent-probe.log` (tabs expanded and trailing whitespace removed; diagnostic text unchanged). No parent commit was modified. The probe is stored as a text artifact so the implementation branch does not acquire a permanently failing test.
