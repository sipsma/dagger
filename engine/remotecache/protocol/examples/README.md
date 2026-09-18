# Example messages

One JSON body per endpoint of the remote cache service protocol. The types
are in the parent package. `TestExamplesRoundTrip` in the parent package
decodes each file into its type with unknown fields refused, encodes it
again, and requires the two to be equal, so these files and the Go types
cannot drift apart.

| File | Endpoint | Type |
| --- | --- | --- |
| `p1-poll-request.json` | P1 `POST /v1/poll` request | `PollRequest` |
| `p1-poll-response.json` | P1 response: one import command and one export command | `PollResponse` |
| `p2-import-result.json`, `p2-import-result-failed.json` | P2 `POST /v1/commands/<id>/result` for an import command | `CommandResult` |
| `p2-export-result.json`, `p2-export-result-not-found.json`, `p2-export-result-failed.json` | P2 for an export command | `CommandResult` |
| `p3-session-report.json` | P3 `POST /v1/session-reports` | `SessionReport` |
| `p4-blob-check-request.json` | P4 `POST /v1/blobs/check` request | `BlobCheckRequest` |
| `p4-blob-check-response.json` | P4 response | `BlobCheckResponse` |
| `p5-bundle-upload-request.json` | P5 `POST /v1/bundles` request | `BundleUploadRequest` |
| `p5-bundle-upload-response.json` | P5 response | `BundleUploadResponse` |
| `p5-bundle-upload-conflict.json` | P5 status 409 body | `ErrorResponse` |
| `error.json` | Any non-2xx body | `ErrorResponse` |

What is real and what is illustrative in the bundles:

- The bundle skeleton (`version`, `roots`, `values[].ordinal`, `values[].record.envelope`, `values[].record.call`, `values[].dependencyIDs`) is the output of a real `Cache.WithExportedValues` call on an in-process cache, with the type names changed to `Container` and `Directory`.
- `objectJSON` is opaque to the service. A real value's payload is the core type's own encoding, not the placeholder shown here.
- `outputs[].chain.layers` has the shape of `snapshots.ExportLayer`, whose fields have no JSON tags, so the keys are `Descriptor`, `Description` and `CreatedAt`.
- The P1 example's bundle has `addresses` filled in, one per layer digest, as the service sends it. The P5 example's bundle has no `addresses`, as the engine posts it. Otherwise the two bundles are identical.
- Digests are SHA-256 digests of short strings, and the bundle ID in the examples is not the digest of the example bundle's bytes.
