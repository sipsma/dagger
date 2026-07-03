# Remote Cache Reset — Lessons

Started 2026-07-03 at the reset checkpoint. This list is deliberately **fresh** —
not seeded from any prior lessons document, so nothing stale frames the next
effort.

Rules for this file:

- **General lessons only.** No hyper-specific war stories from previous efforts;
  detailed failure narratives create the wrong framing even when phrased as
  "don't do this." The old docs remain available for targeted analysis when a
  specific question demands it.
- **Every addition is approved by Erik before it lands here.**

---

## L1 — Design artifacts must be genuinely human-reviewable

Long markdown design documents get skimmed, and skimmed approval is how design
misses get seeded. Designs and major proposals are written as well-formatted
HTML pages (served locally, opened in a browser pane): real typography,
diagrams wherever they carry weight, interactive elements when genuinely
useful. Same rigor and level of detail as any design doc — different
presentation. The HTML page **is** the source of truth, not a rendering of a
separate markdown doc.

## L2 — Continuous integration-test gates, not milestone-end validation

Unit suites green is not enough signal between milestones. A curated set of
integration tests (the persistence/restart suite plus a deliberately chosen,
affordable set) is a standing gate for **each landed chunk of work**, not just
milestone completion. Intermediate states that legitimately fail a gate are
handled explicitly, case by case — a named, temporary, owner-acknowledged
exception rather than a silently skipped gate.

## L3 — Review necessity, not just soundness

Review that only asks "is this correct/complete/well-tested?" will approve an
unnecessary mechanism every time, because each increment can be locally
justified. Every review round must also ask, with real authority: **should
this exist? which requirement does it trace to? what would deleting it cost?**
Human review time is budgeted for exactly this question — it is the one
reviewers-of-diffs are structurally worst at.

---

## Proposed — awaiting Erik's approval

- **P1 — Two inert fix-rounds on one symptom ⇒ stop and re-examine the
  architecture.** Never a third fix round on momentum, no matter how
  evidence-anchored each individual round looks. Repeated falsification of
  "the enforcement surface is now complete" claims is a design smell, not a
  diligence gap.
- **P2 — Budget anchors in every brief.** Agent work orders carry explicit
  wall-clock/token checkpoints with a default action of stop-and-escalate.
  Progress is measured against the goal and the budget, not by artifacts
  produced per round.
- **P3 — Prefer decision-point policies over continuous invariants.** When a
  property can be enforced at a discrete decision point (a lookup, a
  publication, an import), do that; a design that instead maintains the
  property continuously across concurrent shared state must justify itself —
  and any invariant whose enforcement requires auditing "every reader/writer/
  entry point" is presumptively the wrong mechanism.
