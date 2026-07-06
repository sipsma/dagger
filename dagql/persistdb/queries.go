package persistdb

import (
	"context"
	"database/sql"
	"errors"
)

const (
	MetaKeySchemaVersion = "schema_version"
	MetaKeyCleanShutdown = "clean_shutdown"

	// Per-boot result counts written at flush; the self-check that importing
	// and re-exporting a store adds no rows reads these.
	MetaKeyResultsTotal            = "results_total"
	MetaKeyResultsImported         = "results_imported"
	MetaKeyResultsExecutedThisBoot = "results_executed_this_boot"

	// MetaKeyStoreUUID is this store's identity: minted at store creation,
	// wiped with it. It is the first half of every locally-minted result
	// origin pair.
	MetaKeyStoreUUID = "store_uuid"
	// MetaKeyMaxResultID is the allocator high-water mark: the maximum
	// result ID ever allocated in this store's lifetime, flushed at
	// shutdown. Boot resumes allocation above max(surviving rows, this
	// mark) so a pruned-then-restarted store can never re-allocate an ID
	// that an earlier export already bound to a different result's origin.
	MetaKeyMaxResultID = "max_result_id"
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
