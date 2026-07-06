//go:build !testonly_cache_transport

package server

import "context"

// The test-only cache-bundle file transport is compiled out of this build
// (see cachetransport_testonly.go and the testonly_cache_transport tag,
// set only by the engine-dev test build).
func (srv *Server) testOnlyCacheTransportBoot(context.Context) {}
