CREATE TABLE IF NOT EXISTS calls (
    dagql_cache_key TEXT PRIMARY KEY NOT NULL,
    buildkit_cache_key TEXT NOT NULL,
    ttl_unix_time INTEGER NOT NULL
) STRICT;
