## Summary

Three test-only fixes for tests that failed once each in CI on main or on PRs that do not touch them. Each fix makes the test's own contract explicit instead of relying on timing or on a random fixture. No production code changes. None of the three reproduces the failed CI run itself; where the recorded trace does not prove the exact cause, the section says so.

## Checkout reuse: force the callers to overlap

`TestWorkspaceGitCheckoutReuse/discard=false/concurrent` expects exactly one retained materialization, but the cache deliberately permits a redundant execution when another call finishes between the lookup and the singleflight check. The test now holds the fixture backend until every selector has reached synctest quiescence and releases it only then, so the callers overlap by construction; each cache and its result channels stay in one synctest scope, the backend gate and worker joins are bounded, and callers are released and cancelled before cleanup joins them. The exact-one and object-identity assertions are unchanged.

Evidence: focused `core/schema` `-race -count=50 -timeout 120s` passes before and after the change, 50 parents and 300 subcases each. The recorded failure (test-base on #14051, trace `07b546ec4257e74448d37914dd9013ef`, `workspace_test.go:148` "materialize the retained checkout only once: expected 1, actual 2") did not reproduce on the baseline; the diagnosis follows the source contract, so this does not prove the original trace's exact cause.

## dagger up: name the module-loading stage and bound it

`TestUp/TestUpRunService` and `TestUp/TestWorkspaceUpPortMapping` spent their readiness budget resolving workspace module definitions before any service had started, and reported that as a readiness failure. The test now prepares the module definitions with `dagger up -l` under a named five-minute bound before the service-readiness budget begins, bounds each HTTP request and the CLI shutdown wait, and names the failing stage with its elapsed time. The service and response assertions are unchanged.

Evidence: isolated shell fixtures with bounded process cleanup exercise successful preparation and shutdown, preparation errors and timeouts, blocked readiness and body requests, mismatched bodies, and an ignored TERM. The recorded failures (test-base on #14231, trace `1f52fa2fb01e6eed553563f60202a4b7`, `up_test.go:709` and `:748`) expired readiness inside module-definition resolution; the slow constructor evaluation behind that is unproven, and the engine's `GracefulStop` change on the same PR is not causal to the sequence. The four other `daggerUpVerify` call sites were source-reviewed, not executed.

## Binary search fixture: a deterministic NUL

`TestDirectory/TestSearch/binary_files_are_skipped` wrote 1024 random bytes ahead of the matching text and relied on ripgrep's binary heuristic, which needs a NUL byte that random bytes do not guarantee; a NUL-free prefix returns `binary.bin` next to `text.txt`. The fixture now writes both files directly with an explicit leading NUL in `binary.bin`, keeping the matching text in both files and the text.txt-only assertion.

Evidence: a deterministic probe through `SearchOpts.RunRipgrep` reproduces the old assertion failure with a NUL-free prefix and passes with the leading NUL (about a 1.8% chance per run of the old fixture). The recorded failure (test-base on #14264, trace `ee23f81f00e5c96d9c266e7ce5b04e5b`, `directory_test.go:2379`, expected `[text.txt]` got `[binary.bin text.txt]`) did not record its random bytes, so the probe establishes the fixture defect rather than recovering that run's payload.

## Validation

Each commit was reviewed on its own before joining this branch; `go vet ./core/integration/` and `go vet ./core/schema/` pass on the tip. CI runs every package on the pushed head.
