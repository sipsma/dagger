package core

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/debug"
	"sync"

	"github.com/moby/buildkit/session/sshforward"
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

var debugStreamID = 0
var debugLock = sync.Mutex{}

func (p *socketProxy) ForwardAgent(stream sshforward.SSH_ForwardAgentServer) error {
	debugLock.Lock()
	debugStreamID++
	id := debugStreamID
	debugLock.Unlock()
	fmt.Fprintf(os.Stderr, "%d FORWARDAGENT\n", id)

	conn, err := p.dial()
	if err != nil {
		return err
	}

	conn = &debugReadWriteCloser{conn, id}

	err = sshforward.Copy(context.TODO(), conn, stream, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%d FORWARDAGENT ERROR: %s\n", id, err)
	} else {
		fmt.Fprintf(os.Stderr, "%d FORWARDAGENT DONE\n", id)
	}
	return err
}

type debugReadWriteCloser struct {
	io.ReadWriteCloser
	id int
}

func (d *debugReadWriteCloser) Close() error {
	fmt.Fprintf(os.Stderr, "%d CLOSE\nSTACK: %s\n", d.id, debug.Stack())
	// TODO:
	// return d.ReadWriteCloser.Close()
	return nil
}

func (d *debugReadWriteCloser) Read(p []byte) (int, error) {
	n, err := d.ReadWriteCloser.Read(p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%d READ ERROR: %v\n", d.id, err)
	} else {
		fmt.Fprintf(os.Stderr, "%d READ: %s\n", d.id, string(p[:n]))
	}
	return n, err
}

func (d *debugReadWriteCloser) Write(p []byte) (int, error) {
	n, err := d.ReadWriteCloser.Write(p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%d WRITE ERROR: %v\n", d.id, err)
	} else {
		fmt.Fprintf(os.Stderr, "%d WRITE: %s\n", d.id, string(p[:n]))
	}
	return n, err
}
