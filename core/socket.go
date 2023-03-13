package core

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/moby/buildkit/session/sshforward"
	"google.golang.org/grpc/metadata"
)

type Socket struct {
	ID SocketID `json:"id"`
}

type SocketID string

func (id SocketID) String() string { return string(id) }
func (id SocketID) LLBID() string  { return fmt.Sprintf("socket:%s", id) }

type socketIDPayload struct {
	HostPath string `json:"host_path,omitempty"`
}

func (id SocketID) decode() (*socketIDPayload, error) {
	var payload socketIDPayload
	if err := decodeID(&payload, id); err != nil {
		return nil, err
	}

	return &payload, nil
}

func (payload *socketIDPayload) ToSocket() (*Socket, error) {
	id, err := encodeID(payload)
	if err != nil {
		return nil, err
	}

	return NewSocket(SocketID(id)), nil
}

func NewSocket(id SocketID) *Socket {
	return &Socket{id}
}

func NewHostSocket(absPath string) (*Socket, error) {
	payload := socketIDPayload{
		HostPath: absPath,
	}

	return payload.ToSocket()
}

func (socket *Socket) IsHost() (bool, error) {
	payload, err := socket.ID.decode()
	if err != nil {
		return false, err
	}

	return payload.HostPath != "", nil
}

func (socket *Socket) Server() (sshforward.SSHServer, error) {
	payload, err := socket.ID.decode()
	if err != nil {
		return nil, err
	}

	return &socketProxy{
		dial: func() (io.ReadWriteCloser, error) {
			return net.Dial("unix", payload.HostPath)
		},
	}, nil
}

type socketProxy struct {
	dial func() (io.ReadWriteCloser, error)
}

var _ sshforward.SSHServer = &socketProxy{}

func (p *socketProxy) CheckAgent(ctx context.Context, req *sshforward.CheckAgentRequest) (*sshforward.CheckAgentResponse, error) {
	return &sshforward.CheckAgentResponse{}, nil
}

func (p *socketProxy) ForwardAgent(stream sshforward.SSH_ForwardAgentServer) error {
	// TODO:
	// TODO:
	// TODO:
	// TODO:
	// TODO:
	// TODO:
	// TODO:
	var id string
	opts, _ := metadata.FromIncomingContext(stream.Context()) // if no metadata continue with empty object
	if v, ok := opts[sshforward.KeySSHID]; ok && len(v) > 0 && v[0] != "" {
		id = v[0]
	}
	fmt.Fprintf(os.Stderr, "starting copy of socket connection for %s\n", id)

	conn, err := p.dial()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(stream.Context())
	return sshforward.Copy(ctx, conn, stream, func() error {
		// TODO:
		// TODO:
		// TODO:
		// TODO:
		// TODO:
		// TODO:
		fmt.Fprintf(os.Stderr, "closing socket connection for %s\n", id)

		cancel()
		return nil
	})
}
