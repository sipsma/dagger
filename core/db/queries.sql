-- name: SelectCall :one
SELECT * FROM calls WHERE dagql_cache_key = ? AND ttl_unix_time > ?;

-- name: InsertCall :exec
INSERT INTO calls (
    dagql_cache_key,
    buildkit_cache_key,
    ttl_unix_time
) VALUES (
    ?, ?, ?
);

-- name: RemoveCall :exec
DELETE FROM calls WHERE dagql_cache_key = ?;
