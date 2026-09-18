# Packaging the remote cache engine work as stacked pull requests

Stack integrator, 19 September 2026. Private evidence branch
`stack-integrator-evidence`; nothing here goes into a pull request.

## Step 1: rebase Erik's stack onto `main`

Inputs, fetched from `upstream` (`dagger/dagger`):

| PR | branch | head before | commits above the PR below |
|---|---|---|---|
| #13962 | `sipsma/remote-cache-track5-reader-cancel` | `a5c4b2570a` | 16 above `main`'s merge base `0d031c08ef` |
| #13969 | `sipsma/remote-cache-track6-session-resources` | `73234a8d12` | 52 |
| #14043 | `sipsma/remote-cache-track7-per-part-evaluation` | `1d85bd34aa` | 43 |
| #14049 | `sipsma/remote-cache-track8-terminology` | `18f0d54c86` | 7 |
| #14050 | `sipsma/remote-cache-container-part-persistence` | `9375bbb985` | 12 |
| #14051 | `sipsma/remote-cache-snapshot-chains` | `62d62bd0c3` | 16 |
| #14093 | `sipsma/remote-cache-deferred-filesystem-restoration` | `17f7dd89f4` | 9 |

`upstream/main` at `06660c3cde`, 439 commits above the stack's base
`0d031c08ef`. Each branch above #13962 is a strict descendant of the one
below it. GitHub reports #14049 as CONFLICTING against its base before
any rebase; the rest MERGEABLE.

Rebase log follows, bottom first.

### #13962 `sipsma/remote-cache-track5-reader-cancel`

Candidate head `c6653c6d04` (working branch `pkg/track5`), 16 commits on
`upstream/main` `06660c3cde`. Map, original to candidate, in order:
`4e993be3fb`, `e6b45caeac`, `86e0babb50`, `ed0f6cf2f5`, `22605cb304`,
`61c5790ca2`, `cb739d2b9c`, `9180e61009`, `2f59ec6361`, `1042f8419b`,
`ebad6b1c15`, `241af6a1a2`, `410d7cb525`, `13e8677318`, `a5c4b2570a` are
each one candidate commit with the same message; `1ca59a2b59` (chore:
regenerate tla-check module bindings) is `c6653c6d04`, a fresh
regeneration on the rebased tree (`dagger generate -y go-sdk:generate`
on `remote-cache-engine`, source-map lines only), placed at the tip
rather than in its original position because its content depends on
every other commit's lines.

Conflicts and resolutions:

1. `e6b45caeac`, `.dagger/modules/tla-check/main.go` and
   `dagql/tla/README.md`: main added the client-lifecycle model, its
   expected-outcome map and the orphaned-lease mutation; the commit adds
   three decode configurations and a finding sentence. Kept main's text
   and inserted the commit's additions in place (both additive).
2. `cb739d2b9c`, README: the same paragraph, this time closing the
   finding. Kept main's mutation sentence and replaced the finding
   sentence with the commit's closed-finding wording.
3. `1ca59a2b59`, `dagger.gen.go`: main had regenerated the same file
   (its client-lifecycle API). Took main's file at that point and
   regenerated at the tip, above.
4. `241af6a1a2`, `dagger.toml`: main migrated the file to the
   `sdks.go.scopes` format and registers tla-check as a top-level
   module; the commit moves tla-check into the dev environment (old
   format). Resolved by moving main's `[modules.tla-check]` entry to
   `[env.dev.modules.tla-check]` in main's format. Note: main's
   `.github/workflows/checks.yml` filter matrix has no tla-check entry,
   so its `+check` functions do not run in CI on main either way; the
   commit's intent (dev only) is carried unchanged.

Mishap, corrected: the first resolution of 4 failed partway and the
rebase continued with conflict markers in `dagger.toml` for the last four
commits. Rewound to before that commit, redid it, re-applied the three
above it; no candidate commit carries markers (checked per commit).

Tests on `c6653c6d04`, clean tree, `go test -v -count=1 -timeout 60s
./dagql/ ./dagql/idtui/`, log /tmp/pkg-track5-c6653.log: see the result
lines recorded below once the run is in. `e2e/helm` (the branch changes
`k3s.go`) is its own Go module whose tests connect to the host's own
`dagger`; on this host they fail identically on pristine main ("module
requires dagger v0.21.9, but you have v0.21.7", three tests), so they are
not evidence here and are left to CI.
