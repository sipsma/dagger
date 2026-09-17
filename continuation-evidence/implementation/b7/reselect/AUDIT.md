# Batch 7, author B: audit of the reselect class, before any code

Author B, 17 September 2026. Base `c5b299142c`. Read-only work: no code changed, no test run. Scope of the search: `grep -rn "ErrPartReselect\|ErrPersistStateNotReady\|partCanReselect"` over `dagql`, `core` and `engine`, non-test files, then every hit read in its function. "Verified" below means I read the lines; "inferred" means I reasoned about callers I did not trace to the end.

## Conclusion

1. **The packet's premise is too narrow in three ways.** The class is retried by **seven** unbounded loops, not two. It has **two** sentinels, not one. And each sentinel is returned for busy causes and for changed causes alike, sometimes by the same line, so a site cannot be classified by its sentinel or even by its line.
2. **A sound rule exists, and it is local to each site.** A site may report *changed* only when a monotonic counter it compares has moved away from what this attempt observed; it then records the counter's current value. The same value recorded twice at the same site within one demand cannot happen by contention, so it is a hard error. Everything else retries as today. I recommend *busy as the default*: a site I misjudge then costs coverage, never a false failure.
3. **Of the 56 returns, 11 are changed, and 8 of those would record a stamp.** They include the one that spun in batch 6. The rest: 20 busy, 9 ended, 13 reachable only from a sharing pass, 3 derived.
4. **Two findings outside the rule.** `core/file.go:316` and `core/directory.go:338` return the not-ready sentinel for a *state* ("missing snapshot and lazy op"), not for a held guard. Inside `demandPart` that spins forever and the rule cannot see it. And four of the seven loops carry no warning at all.
5. **I would not add a blocking wait on any core guard.** Three gate-level busy sites already have a real signal they could wait on; I list them as optional, not required.

## The seven loops

| # | Loop | Site | Has batch 6's warning | Checks `ctx` each round | What reaches it |
| --- | --- | --- | --- | --- | --- |
| 1 | `evaluateOne` | `dagql/cache.go:4037` | no | no (callee does) | the native route: a body's `RunNative` refusal, `ErrLazyTaskBusy` is not in the class |
| 2 | `EvaluateParts` | `dagql/cache.go:4067` | no | no (callee does) | same |
| 3 | `PartHost.Evaluate` | `dagql/cache_part_host.go:38` | no | no (callee does) | same, for an inline output |
| 4 | `demandPart`, outer | `dagql/cache_part_demand.go:157` | yes | through `RunLazyTask` (inferred) | what the acquire Body returns, to owner and joiners |
| 5 | `demandPart`, acquire Body | `dagql/cache_part_demand.go:161` | yes | yes | selection, obtain, delegation, decision |
| 6 | `publishEvaluatedParts` | `dagql/cache_part_lazy.go:33` | yes | yes | its own prepare and Commit |
| 7 | `installChainPart`, re-preparation | `dagql/cache_part_content.go:613` | no | yes | its own prepare and Commit, after the download |

`runLazyOperationDecision` has a bounded loop of two scans (`cache_part_demand.go:428`) and then returns the sentinel to loop 5. `demandPart` never returns the class to its caller, so loops 1 to 3 see it only from the native route (verified for `evaluateAcquiredParts`; inferred for every native body in core).

## The class

`partCanReselect` (`cache_part_source.go:652`) accepts `ErrPartReselect` and `ErrPersistStateNotReady`, through single wrapping, and a joined error only if every member is in the class. So every `fmt.Errorf("%w: ...", ErrPersistStateNotReady)` in `core` is retried by these loops. Those are part of this audit.

## What can serve as a stamp: the monotonic counters, and who writes them

| Counter | Writers (verified) | Monotonic | Advanced by the demand's own failed attempt |
| --- | --- | --- | --- |
| `row.payloadRevision` | `cache.go:2495` owner links, `cache.go:5855` first value, `cache_part_install.go:638` Commit, `cache_persistence_import.go:504,853` decode | yes | no |
| typed `OutputRevision` (core, per value) | core publication | yes (inferred from its contract, `cache_output_revision.go:5`) | no |
| `row.transferRevision`, `dependencyOwnershipRevision` | `cache_offer_owner.go:192..228`, `cache_part_install.go:418`, `cache.go:3057,6119` | yes | no |
| `row.requiredSessionResourcesGen` | atomic add only | yes | no |
| `demand.revision` | `cache_part_content.go:561`, exhaustion only | yes | yes, and that is real progress: the exhausted source is excluded from the next scan (`cache_part_source.go:526`) |
| `gate.revision` | every permit create and release, every group transition | yes | **yes**: `TryAcquire` and `Release` bump the receiver's gate. A stamp containing the receiver's gate revision never repeats, so it proves nothing. |
| output phase | Pending, Installed, Complete; one way (`cache_part_install.go:646`, `cache_part_host.go:131`, `cache_part_content.go:697`) | yes | no |
| `gate.managed` | set true at three sites, never cleared | yes | no |

Not usable: `row.expiresAtUnix` (goes up and down, `cache.go:1411..1414`), `persistedEnvelope` pointer, `hasValue`, candidate-set membership, session resource satisfaction, offer-owner authority, wall-clock availability of an offer. `persistedEnvelope` and `hasValue` change only together with `payloadRevision`, except two boot-import writers (`cache_persistence_import.go:190,387`) that run before any demand exists; so a payload comparison is covered by `payloadRevision`.

## The rule I propose

At a changed site, with `E` the counters this attempt observed and `C` their current values under the site's own lock:

- if `C == E`, the refusal was caused by something uncounted: return the plain sentinel (busy);
- otherwise return the sentinel wrapped with `(site, row, C)`. The loop records it in the demand's `PartDemandState`. If `(site, row, C)` is already recorded for this demand, the loop returns `ErrPartNoProgress` carrying the site, `E` and `C`. A caller with no demand state, which is every sharing slot, gets the plain sentinel.

Why it cannot fire under contention. Attempt *k* observes `E_k`, is refused at time `r_k` with `C_k != E_k`, and counters only grow, so `C_k > E_k` in some component. Attempt *k+1* observes after `r_k`, so `E_{k+1} >= C_k`, and a changed refusal means `C_{k+1} > E_{k+1}` in some component. Every recorded `C` at one site is therefore distinct. A repeat means the site refused although nothing it counts moved: a wrong expectation, which is a defect. It needs three things, which the table checks per site: the components are monotonic; each attempt observes afresh (verified for loops 5, 6, 7 and the two-scan loop: each round calls `probePart`, `scanPartSources` or `capturePartRecord` again); and the demand's own attempt does not advance them.

What it would have done in batch 6. Defect 2 refused at `cache_part_install.go:614` (the comparison is line 613) with `expected.payloadRevision == 0` and the row at, say, 5. First round records `(614, row, 5)`. Second round records the same again: hard error on the second iteration instead of a seven minute spin.

What it does not catch: a deterministic refusal at a busy or ended site; a deterministic not-ready state (finding 4); a spin whose stamp the attempt itself advances. Batch 6's warning stays for those, and I would add it to loops 1, 2, 3 and 7.

The rule adds no wait and no lock. It costs one small map on a demand that is already retrying.

## Sites: `ErrPartReselect`, 56 returns

Class: **C** changed, with the counter; **B** busy, with the holder; **E** ended, another owner's attempt or this task's authority is over and the next round starts a new generation or exits; **S** reachable only from a sharing pass, which has no retry loop, and reaches a joined demand only as E through `shareSlotEnded`.

### `dagql/cache_part_install.go`

| Line | Condition | Class | Counter, holder or reason |
| --- | --- | --- | --- |
| 223 | delegation proof not current at prepare | B | compares frames, deps, session, not only counters |
| 242 | receiver's part already complete at prepare | C | output phase; next round opens it or joins its installation |
| 254 | prefix base no longer matches | S | |
| 470 | receiver `version.check` failed | **C / B, same line** | C when `payloadRevision` or an `OutputRevision` moved; B when the core try-lock inside `PersistedOutputRevision` was held. Needs `version.check` to say which. |
| 476 | delegation child `version.check` | C / B | same split, child row |
| 484 | donor `version.check` | C / B | same split, donor row |
| 491 | receiver unregistered or task inactive | E | next `RunLazyTask` fails hard or the Body has ended |
| 499, 502, 515 | sessionless receiver, expiry, donated facts | S | |
| 507 | donor unregistered | B | set membership; next scan drops it |
| 518 | donor facts differ | C | donor's `offers, resources, ownership, payload, gate`; `expires` excluded. Donor gate is not advanced by this attempt, because a receiver that is its own complete source returns at `cache_part_demand.go:185` before any Commit. |
| 535 | donor no longer an eligible equivalent | B | set membership |
| 538 | offer owner no longer allowed | B | authority, uncounted |
| 542, 555 | a dependency not allowed, or not held | B | authority and set membership |
| 579, 583 | own group not Running, or own permit gone | E | only this task or its end changes them; reachable when the kernel has ended the task, and then `ctx` is cancelled and the loop exits on its next check |
| 595, 598 | predecessor not installed as reserved | S | |
| 606 | `p.store.TryLock()` failed | B | core part-store guard; holder is a capture or a publication; no signal exposed |
| **614** | receiver representation is not the expected one | **C** | `payloadRevision`. Batch 6 defect 2. |
| 705 | `InstallReadyPart` maps a refused outcome | derived | carries whatever Commit returned |

### `dagql/cache_part_source.go`

| Line | Condition | Class | Counter, holder or reason |
| --- | --- | --- | --- |
| 531 | own part complete but Busy | B | holder is the installing task; next round joins it at `cache_part_demand.go:172` (a real wait) |
| 546 | a candidate's `version.check` failed | C / B | same split as 470, per candidate |
| 555 | a candidate's facts moved, or the session no longer satisfies it | B | facts include that candidate's `gate`, and when the receiver is a candidate its gate is advanced by this demand's own permits; keep busy |
| 572 | selected row no longer a candidate | B | set membership |
| 585 | offer owner no longer allowed | B | authority |
| 696 to 753 (6 returns) | sessionless constructor | S | |

### `dagql/cache_part_demand.go`

| Line | Condition | Class | Counter, holder or reason |
| --- | --- | --- | --- |
| 180 | receiver probe not ready; ends the acquire generation | B | see the not-ready table |
| 232 | obtain Body entered after the caller released the source | E | |
| 243 | `TryAcquire` says already installed | C | output phase |
| 348 | source check stale under E | B | candidate set and facts, including gate |
| 353 | drain not current or not drained | B | holder is a new writer permit; signal `ticket.done` exists (optional wait 1) |
| 395 | `PrepareOriginal` busy | B | holder is another group's task; its token is joinable (optional wait 2) |
| 462 | `BeginOriginal` not granted | B | compares `gate.revision`, which sibling permits move |
| 483 | both scans used | derived | |
| 501 | no running group found to join | E | the holder ended between the refusal and the join. Inferred risk: a `GateBusy` caused by a writer *permit*, not a group, also lands here and retries at once; I believe one address has one acquire generation at a time, so the holder can only be a decision permit, which is short. Worth the designer's eye. |
| 506, 510 | joined the running task; it finished | E | after a real wait |

### The rest

| Site | Condition | Class | Counter, holder or reason |
| --- | --- | --- | --- |
| `cache_part_host.go:103` | `RunNative` on a managed gate | C | `gate.managed`; loops 1 to 3 then route to acquisition. No demand state exists there, so the rule does not apply; warning only. |
| `cache_part_delegation.go:122` | mapping differs on re-capture | C | child `payloadRevision` between the two captures |
| `cache_part_delegation.go:173, 218` | proof not current, or parent facts moved | B | frames, deps, session, parent gate |
| `cache_part_lazy.go:84` | own group not Running | E | as 579 |
| `cache_part_content.go:607` | chain content failed; source exhausted | C | `demand.revision`; always advances, so it can never repeat. Listed for completeness. |
| `cache_part_content.go:623` | offer owner no longer allowed | B | authority |
| `cache_part_content.go:639` | maps a refused Commit | derived | |
| `cache_part_content.go:669` | re-acquire not granted | B | gate busy; holder is a group or permit |
| `cache_snapshot_sharing.go:916` | `shareSlotEnded` | E for the joined demand | |
| `cache_snapshot_sharing.go:1074` | gate outcome inside a pass | S | |

## Sites: `ErrPersistStateNotReady` reachable from the loops

All arrive through `probePart`, `capturePartRecord`, `version.check` or a part store's prepare, on the receiver, a candidate, or a delegated parent.

| Site | Condition | Class | Holder |
| --- | --- | --- | --- |
| `dagql/cache_output_revision.go:52` | typed output revision moved during capture | C | `OutputRevision` |
| `dagql/cache_output_revision.go:75` | row representation moved during capture | C | `payloadRevision` |
| `core/filesystem_output.go:43` | body latch held | B | a running body or another capture |
| `core/filesystem_output.go:106, 166` | `outputMu` held | B | a publication or another capture |
| `core/container_persistence.go:126, 143` | whole or group latch held | B | a running body |
| `core/part_store.go:647, 665, 681` | operation pointer, whole or group latch held | B | a running body or a publication |
| **`core/file.go:316`, `core/directory.go:338`** | value has neither snapshot nor operation | **neither** | **a state, read under `outputMu`, not a held guard. Retried forever by loop 4 through `cache_part_demand.go:180`.** I have no reproducer. It should be a plain error, or the designer should say what transient it stands for. |

A candidate's not-ready probe is skipped, not retried (`cache_part_source.go:510`), so a donor whose native body runs for minutes does not spin a receiver. The receiver itself is either encoded, where capture reads the envelope and touches no core guard, or managed, where bodies run on a private decoded copy (`cache_part_demand.go:422`) and not under the row's own latch. That is why the busy spins are short today (inferred; it is the argument for leaving them alone).

## What I would build

1. `version.check` returns a changed error carrying the counters, distinct from the busy error it passes through. No behavior change: both stay in the class.
2. A small wrapped error `(site, counters)` that still satisfies `partCanReselect`, and a recorder on `PartDemandState`. The eight C sites with a demand in reach return it: install 242, 470, 476, 484, 518, 614; source 546; delegation 122. `demand.go:243` and `content.go:607` need nothing: one ends the demand, the other cannot repeat.
3. `ErrPartNoProgress`, outside the class, returned by loops 5, 6 and 7 when a recording repeats. It names the site, the expected and the current counters.
4. Batch 6's warning added to loops 1, 2, 3 and 7.
5. Tests, all in process and unprivileged: one per C site that forces a wrong expectation and asserts the hard error on the second round; one contention test per C site that moves the counter between rounds many times and asserts no error; the batch 6 defect 2 shape as a named regression.

Optional, only if the council wants them, since each is a new wait: `demand.go:353` re-waits the drain; `demand.go:395` joins the holder's task as the obtain path already does at `demand.go:254`.

## Questions for the council

1. Is busy-by-default accepted? The packet's outline reads the other way round (busy sites wait, the rest are changed). I think that outline is unsafe: 20 sites compare something no counter covers (set membership, authority, frames, a try-lock), three more mix both causes on one line, and only three of them have a signal to wait on.
2. `core/file.go:316` and `core/directory.go:338`: hard error now, or a named limit?
3. Do loops 1 to 3 get anything beyond the warning? I say no: they hold no demand state and only ever see the one-way route switch.
