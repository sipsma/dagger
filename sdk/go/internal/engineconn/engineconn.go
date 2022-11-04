package engineconn

import (
	"context"
	"fmt"
	"io"
	"net/url"

	"github.com/Khan/genqlient/graphql"
)

type RegisterFunc func(*url.URL) (EngineConn, error)

var helpers = map[string]RegisterFunc{}

type EngineConn interface {
	Connect(ctx context.Context, cfg *Config) (graphql.Client, error)
	Close() error
}

func Get(host string) (EngineConn, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, err
	}

	fn, ok := helpers[u.Scheme]
	if !ok {
		return nil, fmt.Errorf("invalid dagger host %q", host)
	}

	return fn(u)
}

type Config struct {
	Workdir      string
	ConfigPath   string
	NoExtensions bool
	LogOutput    io.Writer
}

// Register registers new connectionhelper for scheme
func Register(scheme string, fn RegisterFunc) {
	helpers[scheme] = fn
}
