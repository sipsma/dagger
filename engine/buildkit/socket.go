package buildkit

import (
	"context"
	"fmt"
	"sync"

	"github.com/moby/buildkit/session/sshforward"
	"github.com/moby/buildkit/util/grpcerrors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

type socketProxy struct {
	bk                   *Client
	socketNameToClientID map[string]string
	mu                   sync.RWMutex
}

var _ SessionAttachable = (*socketProxy)(nil)

func (p *socketProxy) Register(srv *grpc.Server) {
	sshforward.RegisterSSHServer(srv, p)
}

func (p *socketProxy) Attach(name string, clientID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.socketNameToClientID[name]; ok {
		return fmt.Errorf("socket %s already registered", name)
	}
	p.socketNameToClientID[name] = clientID
	return nil
}

func (p *socketProxy) CheckAgent(ctx context.Context, req *sshforward.CheckAgentRequest) (*sshforward.CheckAgentResponse, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.socketNameToClientID[req.ID]
	if !ok {
		return nil, grpcerrors.WrapCode(fmt.Errorf("socket %s", req.ID), codes.NotFound)
	}
	return &sshforward.CheckAgentResponse{}, nil
}

func (p *socketProxy) ForwardAgent(stream sshforward.SSH_ForwardAgentServer) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	incomingMD, _ := metadata.FromIncomingContext(ctx)
	ctx = metadata.NewOutgoingContext(ctx, incomingMD)

	p.mu.RLock()
	clientID, ok := p.socketNameToClientID[incomingMD.Get("socket-name")[0]]
	p.mu.RUnlock()
	if !ok {
		return grpcerrors.WrapCode(fmt.Errorf("socket not found"), codes.NotFound)
	}

	caller, err := p.bk.GetSessionCaller(ctx, clientID)
	if err != nil {
		return err
	}

	forwardAgentClient, err := sshforward.NewSSHClient(caller.Conn()).ForwardAgent(ctx)
	if err != nil {
		return err
	}
	return proxyStream[sshforward.BytesMessage](ctx, forwardAgentClient, stream)
}
