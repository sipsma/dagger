package persistdb

import (
	"context"
	"database/sql"
	"errors"
)

const (
	MetaKeySchemaVersion = "schema_version"
	MetaKeyCleanShutdown = "clean_shutdown"
)

const selectMeta = `SELECT key, value FROM meta WHERE key = ?`

func (q *Queries) SelectMeta(ctx context.Context, key string) (*Meta, error) {
	row := q.queryRow(ctx, q.selectMetaStmt, selectMeta, key)
	var m Meta
	err := row.Scan(&m.Key, &m.Value)
	return &m, err
}

const upsertMeta = `
INSERT INTO meta (key, value)
VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET
	value = EXCLUDED.value
`

func (q *Queries) UpsertMeta(ctx context.Context, key, value string) error {
	_, err := q.exec(ctx, q.upsertMetaStmt, upsertMeta, key, value)
	return err
}

func (q *Queries) SelectMetaValue(ctx context.Context, key string) (string, bool, error) {
	m, err := q.SelectMeta(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return m.Value, true, nil
}

const upsertResult = `
INSERT INTO results (
	result_key, id_digest, output_digest, output_extra_digests_json, output_effect_ids_json,
	self_type, self_version, self_payload, dep_of_persisted_result, safe_to_persist_cache,
	unsafe_marker, persist_reason, created_at_unix_nano, expires_at_unix, record_type, description,
	deleted, deleted_at_unix_nano
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (result_key) DO UPDATE SET
	id_digest = EXCLUDED.id_digest,
	output_digest = EXCLUDED.output_digest,
	output_extra_digests_json = EXCLUDED.output_extra_digests_json,
	output_effect_ids_json = EXCLUDED.output_effect_ids_json,
	self_type = EXCLUDED.self_type,
	self_version = EXCLUDED.self_version,
	self_payload = EXCLUDED.self_payload,
	dep_of_persisted_result = EXCLUDED.dep_of_persisted_result,
	safe_to_persist_cache = EXCLUDED.safe_to_persist_cache,
	unsafe_marker = EXCLUDED.unsafe_marker,
	persist_reason = EXCLUDED.persist_reason,
	created_at_unix_nano = EXCLUDED.created_at_unix_nano,
	expires_at_unix = EXCLUDED.expires_at_unix,
	record_type = EXCLUDED.record_type,
	description = EXCLUDED.description,
	deleted = EXCLUDED.deleted,
	deleted_at_unix_nano = EXCLUDED.deleted_at_unix_nano
`

func (q *Queries) UpsertResult(ctx context.Context, arg Result) error {
	_, err := q.exec(ctx, nil, upsertResult,
		arg.ResultKey, arg.IDDigest, arg.OutputDigest, arg.OutputExtraDigests, arg.OutputEffectIDs,
		arg.SelfType, arg.SelfVersion, arg.SelfPayload, arg.DepOfPersistedResult, arg.SafeToPersistCache,
		arg.UnsafeMarker, arg.PersistReason, arg.CreatedAtUnixNano, arg.ExpiresAtUnix, arg.RecordType, arg.Description,
		arg.Deleted, arg.DeletedAtUnixNano,
	)
	return err
}

const tombstoneResult = `
UPDATE results
SET deleted = 1, deleted_at_unix_nano = ?
WHERE result_key = ?
`

func (q *Queries) TombstoneResult(ctx context.Context, resultKey string, deletedAtUnixNano int64) error {
	_, err := q.exec(ctx, nil, tombstoneResult, deletedAtUnixNano, resultKey)
	return err
}

const upsertTerm = `
INSERT INTO terms (
	term_digest, self_digest, input_digests_json, created_at_unix_nano, deleted, deleted_at_unix_nano
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (term_digest) DO UPDATE SET
	self_digest = EXCLUDED.self_digest,
	input_digests_json = EXCLUDED.input_digests_json,
	created_at_unix_nano = EXCLUDED.created_at_unix_nano,
	deleted = EXCLUDED.deleted,
	deleted_at_unix_nano = EXCLUDED.deleted_at_unix_nano
`

func (q *Queries) UpsertTerm(ctx context.Context, arg Term) error {
	_, err := q.exec(ctx, nil, upsertTerm,
		arg.TermDigest, arg.SelfDigest, arg.InputDigestsJSON, arg.CreatedAtUnixNano, arg.Deleted, arg.DeletedAtUnixNano,
	)
	return err
}

const tombstoneTerm = `
UPDATE terms
SET deleted = 1, deleted_at_unix_nano = ?
WHERE term_digest = ?
`

func (q *Queries) TombstoneTerm(ctx context.Context, termDigest string, deletedAtUnixNano int64) error {
	_, err := q.exec(ctx, nil, tombstoneTerm, deletedAtUnixNano, termDigest)
	return err
}

const upsertTermResult = `
INSERT INTO term_results (term_digest, result_key, deleted, deleted_at_unix_nano)
VALUES (?, ?, ?, ?)
ON CONFLICT (term_digest, result_key) DO UPDATE SET
	deleted = EXCLUDED.deleted,
	deleted_at_unix_nano = EXCLUDED.deleted_at_unix_nano
`

func (q *Queries) UpsertTermResult(ctx context.Context, arg TermResult) error {
	_, err := q.exec(ctx, nil, upsertTermResult,
		arg.TermDigest, arg.ResultKey, arg.Deleted, arg.DeletedAtUnixNano,
	)
	return err
}

const tombstoneTermResult = `
UPDATE term_results
SET deleted = 1, deleted_at_unix_nano = ?
WHERE term_digest = ? AND result_key = ?
`

func (q *Queries) TombstoneTermResult(ctx context.Context, termDigest, resultKey string, deletedAtUnixNano int64) error {
	_, err := q.exec(ctx, nil, tombstoneTermResult, deletedAtUnixNano, termDigest, resultKey)
	return err
}

const upsertDep = `
INSERT INTO deps (parent_result_key, dep_result_key, deleted, deleted_at_unix_nano)
VALUES (?, ?, ?, ?)
ON CONFLICT (parent_result_key, dep_result_key) DO UPDATE SET
	deleted = EXCLUDED.deleted,
	deleted_at_unix_nano = EXCLUDED.deleted_at_unix_nano
`

func (q *Queries) UpsertDep(ctx context.Context, arg Dep) error {
	_, err := q.exec(ctx, nil, upsertDep,
		arg.ParentResultKey, arg.DepResultKey, arg.Deleted, arg.DeletedAtUnixNano,
	)
	return err
}

const tombstoneDep = `
UPDATE deps
SET deleted = 1, deleted_at_unix_nano = ?
WHERE parent_result_key = ? AND dep_result_key = ?
`

func (q *Queries) TombstoneDep(ctx context.Context, parentResultKey, depResultKey string, deletedAtUnixNano int64) error {
	_, err := q.exec(ctx, nil, tombstoneDep, deletedAtUnixNano, parentResultKey, depResultKey)
	return err
}

const upsertEqFact = `
INSERT INTO eq_facts (owner_result_key, lhs_digest, rhs_digest, created_at_unix_nano, deleted, deleted_at_unix_nano)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (owner_result_key, lhs_digest, rhs_digest) DO UPDATE SET
	created_at_unix_nano = EXCLUDED.created_at_unix_nano,
	deleted = EXCLUDED.deleted,
	deleted_at_unix_nano = EXCLUDED.deleted_at_unix_nano
`

func (q *Queries) UpsertEqFact(ctx context.Context, arg EqFact) error {
	_, err := q.exec(ctx, nil, upsertEqFact,
		arg.OwnerResultKey, arg.LHSDigest, arg.RHSDigest, arg.CreatedAtUnixNano, arg.Deleted, arg.DeletedAtUnixNano,
	)
	return err
}

const tombstoneEqFact = `
UPDATE eq_facts
SET deleted = 1, deleted_at_unix_nano = ?
WHERE owner_result_key = ? AND lhs_digest = ? AND rhs_digest = ?
`

func (q *Queries) TombstoneEqFact(ctx context.Context, ownerResultKey, lhsDigest, rhsDigest string, deletedAtUnixNano int64) error {
	_, err := q.exec(ctx, nil, tombstoneEqFact, deletedAtUnixNano, ownerResultKey, lhsDigest, rhsDigest)
	return err
}

const upsertSnapshotRef = `
INSERT INTO snapshot_refs (
	ref_key, kind, snapshot_id, snapshotter_name, lease_id, content_blob_digest,
	chain_id, blob_chain_id, diff_id, media_type, blob_size, urls_json,
	record_type, description, created_at_unix_nano, size_bytes, deleted, deleted_at_unix_nano
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (ref_key) DO UPDATE SET
	kind = EXCLUDED.kind,
	snapshot_id = EXCLUDED.snapshot_id,
	snapshotter_name = EXCLUDED.snapshotter_name,
	lease_id = EXCLUDED.lease_id,
	content_blob_digest = EXCLUDED.content_blob_digest,
	chain_id = EXCLUDED.chain_id,
	blob_chain_id = EXCLUDED.blob_chain_id,
	diff_id = EXCLUDED.diff_id,
	media_type = EXCLUDED.media_type,
	blob_size = EXCLUDED.blob_size,
	urls_json = EXCLUDED.urls_json,
	record_type = EXCLUDED.record_type,
	description = EXCLUDED.description,
	created_at_unix_nano = EXCLUDED.created_at_unix_nano,
	size_bytes = EXCLUDED.size_bytes,
	deleted = EXCLUDED.deleted,
	deleted_at_unix_nano = EXCLUDED.deleted_at_unix_nano
`

func (q *Queries) UpsertSnapshotRef(ctx context.Context, arg SnapshotRef) error {
	_, err := q.exec(ctx, nil, upsertSnapshotRef,
		arg.RefKey, arg.Kind, arg.SnapshotID, arg.SnapshotterName, arg.LeaseID, arg.ContentBlobDigest,
		arg.ChainID, arg.BlobChainID, arg.DiffID, arg.MediaType, arg.BlobSize, arg.URLsJSON,
		arg.RecordType, arg.Description, arg.CreatedAtUnixNano, arg.SizeBytes, arg.Deleted, arg.DeletedAtUnixNano,
	)
	return err
}

const tombstoneSnapshotRef = `
UPDATE snapshot_refs
SET deleted = 1, deleted_at_unix_nano = ?
WHERE ref_key = ?
`

func (q *Queries) TombstoneSnapshotRef(ctx context.Context, refKey string, deletedAtUnixNano int64) error {
	_, err := q.exec(ctx, nil, tombstoneSnapshotRef, deletedAtUnixNano, refKey)
	return err
}

const upsertResultSnapshotRef = `
INSERT INTO result_snapshot_refs (result_key, ref_key, role, slot, deleted, deleted_at_unix_nano)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (result_key, ref_key, role, slot) DO UPDATE SET
	deleted = EXCLUDED.deleted,
	deleted_at_unix_nano = EXCLUDED.deleted_at_unix_nano
`

func (q *Queries) UpsertResultSnapshotRef(ctx context.Context, arg ResultSnapshotRef) error {
	_, err := q.exec(ctx, nil, upsertResultSnapshotRef,
		arg.ResultKey, arg.RefKey, arg.Role, arg.Slot, arg.Deleted, arg.DeletedAtUnixNano,
	)
	return err
}

const tombstoneResultSnapshotRef = `
UPDATE result_snapshot_refs
SET deleted = 1, deleted_at_unix_nano = ?
WHERE result_key = ? AND ref_key = ? AND role = ? AND slot = ?
`

func (q *Queries) TombstoneResultSnapshotRef(ctx context.Context, resultKey, refKey, role, slot string, deletedAtUnixNano int64) error {
	_, err := q.exec(ctx, nil, tombstoneResultSnapshotRef, deletedAtUnixNano, resultKey, refKey, role, slot)
	return err
}

const listLiveResults = `
SELECT
	result_key, id_digest, output_digest, output_extra_digests_json, output_effect_ids_json,
	self_type, self_version, self_payload, dep_of_persisted_result, safe_to_persist_cache,
	unsafe_marker, persist_reason, created_at_unix_nano, expires_at_unix, record_type, description,
	deleted, deleted_at_unix_nano
FROM results
WHERE deleted = 0
`

func (q *Queries) ListLiveResults(ctx context.Context) ([]Result, error) {
	rows, err := q.db.QueryContext(ctx, listLiveResults)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Result
	for rows.Next() {
		var row Result
		if err := rows.Scan(
			&row.ResultKey, &row.IDDigest, &row.OutputDigest, &row.OutputExtraDigests, &row.OutputEffectIDs,
			&row.SelfType, &row.SelfVersion, &row.SelfPayload, &row.DepOfPersistedResult, &row.SafeToPersistCache,
			&row.UnsafeMarker, &row.PersistReason, &row.CreatedAtUnixNano, &row.ExpiresAtUnix, &row.RecordType, &row.Description,
			&row.Deleted, &row.DeletedAtUnixNano,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

const listLiveTerms = `
SELECT term_digest, self_digest, input_digests_json, created_at_unix_nano, deleted, deleted_at_unix_nano
FROM terms
WHERE deleted = 0
`

func (q *Queries) ListLiveTerms(ctx context.Context) ([]Term, error) {
	rows, err := q.db.QueryContext(ctx, listLiveTerms)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Term
	for rows.Next() {
		var row Term
		if err := rows.Scan(
			&row.TermDigest, &row.SelfDigest, &row.InputDigestsJSON, &row.CreatedAtUnixNano, &row.Deleted, &row.DeletedAtUnixNano,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

const listLiveTermResults = `
SELECT term_digest, result_key, deleted, deleted_at_unix_nano
FROM term_results
WHERE deleted = 0
`

func (q *Queries) ListLiveTermResults(ctx context.Context) ([]TermResult, error) {
	rows, err := q.db.QueryContext(ctx, listLiveTermResults)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TermResult
	for rows.Next() {
		var row TermResult
		if err := rows.Scan(
			&row.TermDigest, &row.ResultKey, &row.Deleted, &row.DeletedAtUnixNano,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

const listLiveDeps = `
SELECT parent_result_key, dep_result_key, deleted, deleted_at_unix_nano
FROM deps
WHERE deleted = 0
`

func (q *Queries) ListLiveDeps(ctx context.Context) ([]Dep, error) {
	rows, err := q.db.QueryContext(ctx, listLiveDeps)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Dep
	for rows.Next() {
		var row Dep
		if err := rows.Scan(
			&row.ParentResultKey, &row.DepResultKey, &row.Deleted, &row.DeletedAtUnixNano,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

const listLiveEqFacts = `
SELECT owner_result_key, lhs_digest, rhs_digest, created_at_unix_nano, deleted, deleted_at_unix_nano
FROM eq_facts
WHERE deleted = 0
`

func (q *Queries) ListLiveEqFacts(ctx context.Context) ([]EqFact, error) {
	rows, err := q.db.QueryContext(ctx, listLiveEqFacts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EqFact
	for rows.Next() {
		var row EqFact
		if err := rows.Scan(
			&row.OwnerResultKey, &row.LHSDigest, &row.RHSDigest, &row.CreatedAtUnixNano, &row.Deleted, &row.DeletedAtUnixNano,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

const listLiveSnapshotRefs = `
SELECT
	ref_key, kind, snapshot_id, snapshotter_name, lease_id, content_blob_digest,
	chain_id, blob_chain_id, diff_id, media_type, blob_size, urls_json,
	record_type, description, created_at_unix_nano, size_bytes, deleted, deleted_at_unix_nano
FROM snapshot_refs
WHERE deleted = 0
`

func (q *Queries) ListLiveSnapshotRefs(ctx context.Context) ([]SnapshotRef, error) {
	rows, err := q.db.QueryContext(ctx, listLiveSnapshotRefs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SnapshotRef
	for rows.Next() {
		var row SnapshotRef
		if err := rows.Scan(
			&row.RefKey, &row.Kind, &row.SnapshotID, &row.SnapshotterName, &row.LeaseID, &row.ContentBlobDigest,
			&row.ChainID, &row.BlobChainID, &row.DiffID, &row.MediaType, &row.BlobSize, &row.URLsJSON,
			&row.RecordType, &row.Description, &row.CreatedAtUnixNano, &row.SizeBytes, &row.Deleted, &row.DeletedAtUnixNano,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

const listLiveResultSnapshotRefs = `
SELECT result_key, ref_key, role, slot, deleted, deleted_at_unix_nano
FROM result_snapshot_refs
WHERE deleted = 0
`

func (q *Queries) ListLiveResultSnapshotRefs(ctx context.Context) ([]ResultSnapshotRef, error) {
	rows, err := q.db.QueryContext(ctx, listLiveResultSnapshotRefs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ResultSnapshotRef
	for rows.Next() {
		var row ResultSnapshotRef
		if err := rows.Scan(
			&row.ResultKey, &row.RefKey, &row.Role, &row.Slot, &row.Deleted, &row.DeletedAtUnixNano,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
