//go:build !testonly_cache_transport

package main

import (
	"net/http"

	"github.com/dagger/dagger/engine/server"
)

// The test-only cache-bundle export endpoint is compiled out of this build
// (see debug_cachetransport_testonly.go and the testonly_cache_transport
// tag, set only by the engine-dev test build).
func registerTestOnlyCacheTransportHandlers(*http.ServeMux, *server.Server) {}
