CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS results (
    result_key TEXT PRIMARY KEY,
    id_digest TEXT NOT NULL,
    output_digest TEXT NOT NULL DEFAULT '',
    output_extra_digests_json TEXT NOT NULL DEFAULT '[]',
    output_effect_ids_json TEXT NOT NULL DEFAULT '[]',
    self_type TEXT NOT NULL,
    self_version INTEGER NOT NULL,
    self_payload BLOB NOT NULL,
    dep_of_persisted_result INTEGER NOT NULL DEFAULT 0,
    safe_to_persist_cache INTEGER NOT NULL DEFAULT 1,
    unsafe_marker INTEGER NOT NULL DEFAULT 0,
    persist_reason TEXT NOT NULL,
    created_at_unix_nano INTEGER NOT NULL,
    expires_at_unix INTEGER NOT NULL DEFAULT 0,
    record_type TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    deleted INTEGER NOT NULL DEFAULT 0,
    deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_results_output_digest ON results(output_digest);
CREATE INDEX IF NOT EXISTS idx_results_deleted ON results(deleted);

CREATE TABLE IF NOT EXISTS terms (
    term_digest TEXT PRIMARY KEY,
    self_digest TEXT NOT NULL,
    input_digests_json TEXT NOT NULL,
    created_at_unix_nano INTEGER NOT NULL,
    deleted INTEGER NOT NULL DEFAULT 0,
    deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS term_results (
    term_digest TEXT NOT NULL,
    result_key TEXT NOT NULL,
    deleted INTEGER NOT NULL DEFAULT 0,
    deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(term_digest, result_key),
    FOREIGN KEY(term_digest) REFERENCES terms(term_digest),
    FOREIGN KEY(result_key) REFERENCES results(result_key)
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS deps (
    parent_result_key TEXT NOT NULL,
    dep_result_key TEXT NOT NULL,
    deleted INTEGER NOT NULL DEFAULT 0,
    deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(parent_result_key, dep_result_key),
    FOREIGN KEY(parent_result_key) REFERENCES results(result_key),
    FOREIGN KEY(dep_result_key) REFERENCES results(result_key)
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS eq_facts (
    owner_result_key TEXT NOT NULL,
    lhs_digest TEXT NOT NULL,
    rhs_digest TEXT NOT NULL,
    created_at_unix_nano INTEGER NOT NULL,
    deleted INTEGER NOT NULL DEFAULT 0,
    deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(owner_result_key, lhs_digest, rhs_digest),
    FOREIGN KEY(owner_result_key) REFERENCES results(result_key)
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS snapshot_refs (
    ref_key TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    snapshot_id TEXT NOT NULL,
    snapshotter_name TEXT NOT NULL,
    lease_id TEXT NOT NULL,
    content_blob_digest TEXT NOT NULL DEFAULT '',
    chain_id TEXT NOT NULL DEFAULT '',
    blob_chain_id TEXT NOT NULL DEFAULT '',
    diff_id TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL DEFAULT '',
    blob_size INTEGER NOT NULL DEFAULT 0,
    urls_json TEXT NOT NULL DEFAULT '[]',
    record_type TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    created_at_unix_nano INTEGER NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    deleted INTEGER NOT NULL DEFAULT 0,
    deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS result_snapshot_refs (
    result_key TEXT NOT NULL,
    ref_key TEXT NOT NULL,
    role TEXT NOT NULL,
    slot TEXT NOT NULL DEFAULT '',
    deleted INTEGER NOT NULL DEFAULT 0,
    deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(result_key, ref_key, role, slot),
    FOREIGN KEY(result_key) REFERENCES results(result_key),
    FOREIGN KEY(ref_key) REFERENCES snapshot_refs(ref_key)
) STRICT, WITHOUT ROWID;

