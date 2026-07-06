# BRIEF AMENDMENT — from cache-chief (Erik rulings, 2026-07-06 morning). Fold as SETTLED; delete this file in a commit as receipt.

Erik reviewed my critical read of his guidance and ratified four design decisions. Cite "Erik ruling, 2026-07-06 morning" in your review log:

1. MERGE CONCURRENCY MODEL: the merged view advances snapshot-to-snapshot — ingests are
   serialized/batched merges producing consistent snapshots; importers are served a
   consistent snapshot; NO live shared mutable state across serving requests (the take-3
   structural lesson applied server-side). Erik: "that's probably the right place to start
   for sure."
2. RETENTION IN V1: server-side retention policy over the merged view EXISTS in v1 (naive
   is fine); an engine's local pruning no longer propagates structurally — service
   retention owns the merged view's lifetime. State this semantic change in ink.
3. IMPORT PAYLOAD: v1 = the whole merged store for the engine's scope. Measure before
   building anything cleverer.
4. CONCURRENT-EXPORT/UPLOAD RACE: blobs content-addressed; engines request a presigned URL
   per instructed blob; concurrent same-digest uploads resolved by the blob storage (one
   wins / identical writes — both fine); read-side errors handled by the EXISTING
   cache-download error discipline (source-walk fall-through → demote floor). Design
   requirement: verify chosen backends' concurrent-write semantics (S3/Namespace/MinIO),
   pin each with a test; the "upload these records" response must be a pure function of
   (merged snapshot, offered metadata) so overlapping exporters get idempotent instructions.

Also: report progress/ETA to cache-chief at your next checkpoint — Erik wants to know when
the design is ready for review.
