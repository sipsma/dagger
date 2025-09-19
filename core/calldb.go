package core

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"net/url"
	"sync"

	"github.com/dagger/dagger/core/db"
	_ "modernc.org/sqlite"
)

//go:embed db/schema.sql
var CallDBSchema string

type CallDB struct {
	queries *db.Queries
	mu      sync.RWMutex
}

func InitCallDB(ctx context.Context, dbPath string) (*CallDB, error) {
	connURL := &url.URL{
		Scheme: "file",
		Host:   "",
		Path:   dbPath,
		RawQuery: url.Values{
			"_pragma": []string{
				// TODO: fix all these
				"journal_mode=WAL",   // readers don't block writers and vice versa
				"synchronous=OFF",    // we don't care about durability and don't want to be surprised by syncs
				"busy_timeout=10000", // wait up to 10s when there are concurrent writers
			},
			"_txlock": []string{"immediate"}, // use BEGIN IMMEDIATE for transactions
		}.Encode(),
	}
	sqlDB, err := sql.Open("sqlite", connURL.String())
	if err != nil {
		return nil, fmt.Errorf("open calldb: %v", err)
	}
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping calldb: %v", err)
	}
	if _, err := sqlDB.Exec(CallDBSchema); err != nil {
		return nil, fmt.Errorf("migrate calldb: %v", err)
	}

	queries, err := db.Prepare(ctx, sqlDB)
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("prepare calldb: %v", err)
	}
	return &CallDB{
		queries: queries,
	}, nil
}
