package dagger

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"

	"github.com/Khan/genqlient/graphql"
	"go.dagger.io/dagger/router"
)

type SecretID string

type FSID string

type Filesystem struct {
	ID          FSID        `json:"id"`
	Exec        *Exec       `json:"exec"`
	Dockerbuild *Filesystem `json:"dockerbuild"`
	File        *string     `json:"file"`
}

type Exec struct {
	Fs       *Filesystem `json:"fs"`
	Stdout   *string     `json:"stdout"`
	Stderr   *string     `json:"stderr"`
	ExitCode *int        `json:"exitCode"`
	Mount    *Filesystem `json:"mount"`
}

type clientKey struct{}

type contextTransport struct {
	base http.RoundTripper
}

func (t *contextTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if v, ok := os.LookupEnv(router.DaggerContextEnv); ok {
		req = req.Clone(req.Context())
		req.Header.Set(router.DaggerContextEnv, v)
	}
	return t.base.RoundTrip(req)
}

func Client(ctx context.Context) (graphql.Client, error) {
	client, ok := ctx.Value(clientKey{}).(*http.Client)
	if !ok {
		return nil, errors.New("no dagger client in context")
	}
	client.Transport = &contextTransport{base: client.Transport}
	return graphql.NewClient("http://fake.invalid/query", client), nil
}

func WithHTTPClient(ctx context.Context, c *http.Client) context.Context {
	return context.WithValue(ctx, clientKey{}, c)
}

func WithUnixSocketAPIClient(ctx context.Context, socketPath string) context.Context {
	return WithHTTPClient(ctx, &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	})
}
