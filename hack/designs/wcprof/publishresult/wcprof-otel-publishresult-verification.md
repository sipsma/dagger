# wcprof × OTel — the publishResult "root" finding: TWO competing diagnoses + the lead's measurements (OPEN — scrutinize all of it)

## Status: this is OPEN, not settled. Be suspicious of everything here.

The 6-reviewer council split into **two contradictory diagnoses** of why ~330 OTel "roots" are
`dagql.publishResult` spans. The lead then ran some diagnostics on the actual captures. **None of
this is settled.** The ONLY settled thing is the governing PRINCIPLE (the analysis is a rational
function of *faithful* data; never compensate; fix the EMIT when the data is unfaithful; debug model
vs data separately — see `wcprof-otel-chunk4-firstprinciples-handoff.md`). *Which* theory is right,
and what the fix is, is entirely open for you to investigate, verify independently, and land on. Be
suspicious of the lead's measurements AND of chunk2's finding AND of the other five. Run your own
experiments.

## THEORY A — five reviewers (design author, both Codex, chunk3, replay owner): an EMIT GAP

`publishResult` is emitted *parentless* (its `parentId` is empty) because it's created in the waiter
path **after `call_exec` ended**, and context-propagation through an ended span doesn't carry the
parent. Chunk3's deduction: "gate passes ⟹ `call_exec` present ⟹ if `parentId` were `call_exec` it'd
resolve ⟹ but they're roots ⟹ `parentId` must be EMPTY." Proposed fix: make the parent explicit
(set the natural OTel `parentId` to `call_exec`, or — the lead's earlier suggestion — stamp
`wcprof.parent`). Design author: most likely the ended-span propagation failure; **run the
diagnostic to confirm**. (Reviews in your worktree:
`wcprof-otel-publishresult-{design-author,codex-fresh,codex-existing,chunk3-impl,replay-owner}.md`.)

## THEORY B — chunk2: a CAPTURE ARTIFACT (the emit is faithful)

chunk2 captured fresh traces and *measured*: every `publishResult` HAS a non-empty `parentId` (emit
faithful); the `call_exec` parent *span* is ABSENT because the local `otlpdump` live-export drain
**batch-drops spans**. Its proof: (a) 100% of orphan `publishResult`s have `parentId` set; (b) the
loss is **bidirectional** (`call_execs` also missing their `publishResult` children — an emit gap
can't do this); (c) decile-concentrated (a batch, not spread); (d) out-of-order (ended children
missing earlier-ended parents — not a clean time-cut). Conclusion: no emit fix; reject the
`wcprof.parent` stamp (redundant — `parentId` already set; ineffective — target span still absent);
"the gate already fails via unresolved-targets." (Review:
`wcprof-otel-publishresult-chunk2-impl.md`.)

## THE LEAD'S MEASUREMENTS (I had the capture files; this is EVIDENCE, not a verdict — re-run it yourself)

I ran the decisive diagnostic on **your `/tmp/otel-exec.jsonl`** (the exec capture):

- `publishResult` spans: 9206. `parentId` EMPTY (the emit-gap signature): **0**. `parentId` SET but
  target span ABSENT (the capture-drop signature): **537**. `parentId` SET + target present: 8669.
- **Bidirectional:** `call_exec` spans **9974** ≠ `publishResult` **9206** (a 1:1 emit can't); **1143
  `call_execs` missing their `publishResult` child.**
- The 537 orphans: **100% in a single timeline decile** (decile **3** — mid-stream, NOT the tail
  chunk2 saw on its module capture).
- **Gate on `/tmp/otel-exec.jsonl`: PASS** — 331 roots, **0 unresolved-targets**, 0 cycles, baseline
  6.11s.

My PROVISIONAL read (and I want you to challenge it): this leans *strongly* toward chunk2's
capture-artifact view (0 emit-gap signature, `parentId` set, bidirectional loss). BUT it also shows
chunk2's "the gate already catches this" is **wrong for the exec capture** — the gate PASSES with 331
orphan roots. (chunk2's *module* capture failed via unresolved-targets because its dropped
`call_execs` were wait targets; the exec capture's apparently weren't.) So even chunk2 isn't fully
right, and the gate does NOT reliably catch the drop. **Don't take my numbers as settled — reproduce
them, and run your own.**

### Reproducible commands (adapt to your captures)
```
# empty vs dropped parent:
jq -rn '[inputs] as $s | ($s|map(.spanId)|INDEX(.)) as $ids
 | ($s|map(select(.name=="dagql.publishResult")|.+{cp:(.attrs["wcprof.parent"]//.parentId)})) as $p
 | "empty=\([$p[]|select(.cp==""or .cp==null)]|length)  dropped=\([$p[]|select(.cp!=""and $ids[.cp]==null)]|length)  ok=\([$p[]|select(.cp!=""and $ids[.cp]!=null)]|length)"' CAPTURE.jsonl
# bidirectional (call_execs missing their publishResult child):
jq -rn '[inputs] as $s | ($s|map(select(.name=="dagql.publishResult")|.parentId)|INDEX(.)) as $pp
 | ($s|map(select(.attrs["wcprof.op.kind"]=="call_exec"))) as $ce
 | "call_exec=\($ce|length) missing_child=\([$ce[]|select($pp[.spanId]==null)]|length)"' CAPTURE.jsonl
# gate:
go run ./cmd/wcprof-otel-analyze CAPTURE.jsonl
```

## What it WOULD mean if the capture-artifact diagnosis holds (evaluate, don't accept)

- No emit fix (the emit is faithful); the `wcprof.parent` stamp is the wrong fix.
- The data problem is the local capture *instrument* (otlpdump/live-export batch loss) — and "wait
  longer" may not fix it (mid-stream loss; chunk2's poll-until-stable still dropped 225).
- A new internal-root / orphaned-sub-op **gate signal** is likely needed (the unresolved-target check
  is demonstrably insufficient — the exec capture passed with 331 orphan roots).
- Cloud ingest (production) may be complete (force-flush on close) — **verify**, don't assume.
- The prior OTel cross-source validation ran on incomplete captures that *passed* the gate —
  re-confirm on a complete capture.
- Item 3 (the rational root model) is unaffected either way (chaining was the forbidden inference
  regardless).

## OPEN QUESTIONS — investigate suspiciously, with your own experiments

1. Is the capture-artifact diagnosis actually right? Re-run the diagnostic on your captures + fresh
   ones. Is `parentId` really set 100%? Is the loss really bidirectional? Is there ANY subset that's a
   genuine emit gap (empty `parentId`)? Could the lead's jq or filters be wrong?
2. Why does the gate PASS on the exec capture but FAIL on chunk2's module capture? Is the gate
   coverage hole real? Is an internal-root signal the right fix, and how designed?
3. What is the ACTUAL capture-loss mechanism (mid-stream batch loss, decile 3)? otlpdump? the
   live-export pipeline? the LiveSpanProcessor? Is it fixable, and how? Does "capture completely" even
   work, or is the loss mid-stream and not a drain race?
4. Is Cloud ingest actually complete (no artifact in production)? Can you verify?
5. Anything the lead OR any reviewer missed or got wrong?

You can agree, disagree, present things back for more discussion, make whatever change you believe is
right, or some combination. The only thing settled is the principle.
