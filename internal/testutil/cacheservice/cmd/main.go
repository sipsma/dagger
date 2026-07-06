// The test cache service as a standalone binary: the /v1 protocol over a
// filesystem directory with a static bearer token. Integration tests run it
// as a Dagger service between dev engines; it is test infrastructure, never
// a production artifact.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	cacheservice "github.com/dagger/dagger/internal/testutil/cacheservice"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	root := flag.String("root", "/data", "storage root directory")
	token := flag.String("token", os.Getenv("TEST_CACHE_SERVICE_TOKEN"), "static bearer token (or TEST_CACHE_SERVICE_TOKEN)")
	flag.Parse()

	svc, err := cacheservice.New(*root, *token)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("test cache service listening on %s (root %s)", *addr, *root)
	log.Fatal(http.ListenAndServe(*addr, svc.Handler()))
}
