//go:build testonly_cache_transport

package main

import (
	"net/http"
	"os"

	"github.com/dagger/dagger/engine/server"
)

// The test-only cache-bundle export endpoint: cross-engine integration
// tests drive it to move a bundle + chain blobs over a shared volume,
// standing in for the chunk-C service client. Compiled only into the
// engine-dev test build (the testonly_cache_transport tag) and env-gated
// at runtime as defense in depth.
func registerTestOnlyCacheTransportHandlers(m *http.ServeMux, eng *server.Server) {
	if os.Getenv("_DAGGER_TESTONLY_CACHE_TRANSPORT") != "1" {
		return
	}
	m.Handle("/debug/testonly/export-cache-bundle", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if eng == nil {
			http.Error(rw, "engine server not available", http.StatusServiceUnavailable)
			return
		}
		bundlePath := req.URL.Query().Get("bundle")
		if bundlePath == "" {
			http.Error(rw, "missing bundle path", http.StatusBadRequest)
			return
		}
		casDir := req.URL.Query().Get("cas")
		if err := eng.TestOnlyExportCacheBundle(req.Context(), bundlePath, casDir); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		rw.WriteHeader(http.StatusOK)
	}))
}
