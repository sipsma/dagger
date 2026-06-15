package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dagger/dagger/dagql"
	"github.com/stretchr/testify/require"
)

func TestDebugCachemoneyStatsHandler(t *testing.T) {
	stats := dagql.CachemoneyDebugStats{
		MaterializationOutcomesByRole: map[string]map[string]uint64{
			"rootfs": {
				dagql.CachemoneyMaterializationHydrated: 2,
			},
		},
		RecomputeReasons: map[string]uint64{
			dagql.CachemoneyRecomputeReasonIndexMiss: 1,
		},
		ImportsCompleted: 1,
	}
	mux := newDebugMux(fakeDebugServer{stats: stats})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/dagql/cache/stats", nil)
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.JSONEq(t, `{
		"materialization_outcomes_by_role": {"rootfs": {"hydrated": 2}},
		"recompute_reasons": {"index_miss": 1},
		"imports_completed": 1
	}`, rec.Body.String())
}

func TestDebugCachemoneyStatsHandlerRejectsPost(t *testing.T) {
	mux := newDebugMux(fakeDebugServer{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/dagql/cache/stats", nil)
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestDebugCachemoneyStatsHandlerUnavailable(t *testing.T) {
	mux := newDebugMux(fakeDebugServer{statsErr: errors.New("dagql cache not available")})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/dagql/cache/stats", nil)
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "dagql cache not available")
}

type fakeDebugServer struct {
	stats    dagql.CachemoneyDebugStats
	statsErr error
}

func (f fakeDebugServer) DagqlDebugSnapshot() *dagql.EGraphDebugSnapshot {
	return nil
}

func (f fakeDebugServer) WriteDagqlCacheDebugSnapshot(io.Writer) error {
	return nil
}

func (f fakeDebugServer) DebugCachemoneyStats() (dagql.CachemoneyDebugStats, error) {
	return f.stats, f.statsErr
}

func (f fakeDebugServer) DebugCachemoneyExport(context.Context, string) (*dagql.CachemoneyDebugExportResult, error) {
	return nil, nil
}

func (f fakeDebugServer) DebugCachemoneyImport(context.Context, string) (*dagql.CachemoneyDebugImportResult, error) {
	return nil, nil
}
