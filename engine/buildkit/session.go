package buildkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/client"
	"github.com/dagger/dagger/internal/distconsts"
	"github.com/moby/buildkit/identity"
	bksession "github.com/moby/buildkit/session"
	sessioncontent "github.com/moby/buildkit/session/content"
	"github.com/moby/buildkit/util/bklog"
)

const (
	// OCIStoreName is the name of the OCI content store used for OCI tarball
	// imports.
	OCIStoreName = "dagger-oci"

	// BuiltinContentOCIStoreName is the name of the OCI content store used for
	// builtins like SDKs that we package with the engine container but still use
	// in LLB.
	BuiltinContentOCIStoreName = "dagger-builtin-content"
)

func (c *Client) GetSessionCaller(ctx context.Context, clientID string) (bksession.Caller, error) {
	waitForSession := true
	return c.SessionManager.Get(ctx, clientID, !waitForSession)
}

func (c *Client) RegisterSocket(socketName string, clientID string) error {
	c.socketMu.Lock()
	defer c.socketMu.Unlock()
	if _, ok := c.socketNameToClientID[socketName]; ok {
		return fmt.Errorf("socket %s already registered", socketName)
	}
	c.socketNameToClientID[socketName] = clientID
	return nil
}

func (c *Client) ClientIDForSocket(socketName string) (string, error) {
	c.socketMu.RLock()
	defer c.socketMu.RUnlock()
	clientID, ok := c.socketNameToClientID[socketName]
	if !ok {
		return "", fmt.Errorf("socket %s not registered", socketName)
	}
	return clientID, nil
}

type SessionResource interface {
	dagql.Typed
	AttachToSession(ctx context.Context, bk *Client, sess *Session) error
}

type SessionAttachable interface {
	bksession.Attachable
	Attach(name string, clientID string) error
}

type Session struct {
	*bksession.Session

	Sockets SessionAttachable
	// TODO:
	// secret store
	// auth proxy
}

func (c *Client) newSession(ctx context.Context, srv *dagql.Server) (*Session, error) {
	bksess, err := bksession.NewSession(ctx, identity.NewID(), "")
	if err != nil {
		return nil, err
	}

	sess := &Session{
		Session: bksess,
		Sockets: &socketProxy{
			bk:                   c,
			socketNameToClientID: map[string]string{},
		},
	}

	builtinStore, err := local.NewStore(distconsts.EngineContainerBuiltinContentDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create builtin content store: %w", err)
	}
	sess.Allow(sessioncontent.NewAttachable(map[string]content.Store{
		// the "oci:" prefix is actually interpreted by buildkit, not just for show
		"oci:" + OCIStoreName:               c.Worker.ContentStore(),
		"oci:" + BuiltinContentOCIStoreName: builtinStore,
	}))

	// NOTE: it would be nice to get rid of these now, just need to rework Container.asTarball impl
	sess.Allow(&client.AnyDirSource{})
	sess.Allow(&client.AnyDirTarget{})

	sessionResources, err := dagql.ReferencedTypes[SessionResource](ctx, dagql.CurrentID(ctx), srv)
	if err != nil {
		return nil, fmt.Errorf("failed to load allowed sockets: %w", err)
	}
	for _, res := range sessionResources {
		if err := res.AttachToSession(ctx, c, sess); err != nil {
			return nil, fmt.Errorf("failed to attach resource to session: %w", err)
		}
	}
	sess.Allow(sess.Sockets)

	clientConn, serverConn := net.Pipe()
	dialer := func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) { //nolint: unparam
		go func() {
			defer serverConn.Close()
			err := c.SessionManager.HandleConn(ctx, serverConn, meta)
			if err != nil {
				lg := bklog.G(ctx).WithError(err)
				if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
					lg.Debug("session conn ended")
				} else {
					lg.Error("failed to handle session conn")
				}
			}
		}()
		return clientConn, nil
	}
	go func() {
		defer clientConn.Close()
		defer sess.Close()
		err := sess.Run(ctx, dialer)
		if err != nil {
			lg := bklog.G(ctx).WithError(err)
			if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
				lg.Debug("client session ended")
			} else {
				lg.Error("failed to run session")
			}
		}
	}()

	return sess, nil
}
