package schema

import (
	"github.com/containerd/containerd/content"
	"github.com/dagger/dagger/auth"
	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/router"
	"github.com/dagger/dagger/secret"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/worker"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

type InitializeArgs struct {
	Router         *router.Router
	Gateway        *core.GatewayClient
	OCIStore       content.Store
	Platform       specs.Platform
	Auth           *auth.RegistryAuthProvider
	Secrets        *secret.Store
	Worker         worker.Worker
	SessionManager *session.Manager
}

func New(params InitializeArgs) []router.ExecutableSchema {
	base := &baseSchema{
		router:         params.Router,
		gw:             params.Gateway,
		platform:       params.Platform,
		auth:           params.Auth,
		secrets:        params.Secrets,
		worker:         params.Worker,
		sessionManager: params.SessionManager,
	}
	host := core.NewHost()
	return []router.ExecutableSchema{
		&querySchema{base},
		&directorySchema{base, host},
		&fileSchema{base, host},
		&gitSchema{base},
		&containerSchema{base, host, params.OCIStore},
		&cacheSchema{base},
		&secretSchema{base},
		&hostSchema{base, host},
		&environmentSchema{base},
		&httpSchema{base},
		&platformSchema{base},
		&socketSchema{base, host},
	}
}

type baseSchema struct {
	router         *router.Router
	gw             *core.GatewayClient
	platform       specs.Platform
	auth           *auth.RegistryAuthProvider
	secrets        *secret.Store
	worker         worker.Worker
	sessionManager *session.Manager
}
