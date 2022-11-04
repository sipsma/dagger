package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"dagger.io/dagger/internal/engineconn"
	"github.com/Khan/genqlient/graphql"
	"github.com/adrg/xdg"
	"github.com/gorilla/websocket"
	"github.com/moby/buildkit/identity"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	exec "golang.org/x/sys/execabs"
)

func init() {
	// TODO: docker-exec is the wrong name now
	engineconn.Register("docker", New)
}

var _ engineconn.EngineConn = &Docker{}

type Docker struct {
	ctrName string
}

func New(u *url.URL) (engineconn.EngineConn, error) {
	return &Docker{
		ctrName: u.Host + u.Path,
	}, nil
}

func (c *Docker) Connect(ctx context.Context, cfg *engineconn.Config) (graphql.Client, error) {
	// TODO: also support using a cloak bin already in $PATH or a user-specified path
	// TODO: currently we'll never clean up old cloak bins, is that okay for now? it's in the user's cache dir so they can feel free to delete them, but that's annoying still.
	// TODO: maybe some sort of dev mode where cloak bin is always thrown away at the end

	cacheDir := filepath.Join(xdg.CacheHome, "dagger")
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return nil, err
	}

	// TODO: the binary should be namespaced by image SHA and sdk version (though the case of multiple sdks on same machine will be weird...)
	// TODO: ctrName can be set via env var, so make sure to sanitize it
	// TODO: ctrName should perhaps be a hash that also includes the options, in case we change it, but be sure to have a cleanup mechanism in place too then...
	id := digest.FromString(c.ctrName).Encoded()

	helperBinPath := filepath.Join(cacheDir, "dagger-engine-helper-"+id)
	containerName := "dagger-engine-" + id
	volumeName := "dagger-engine-" + id

	if output, err := exec.CommandContext(ctx,
		"docker", "run",
		"--name", containerName,
		"-v", volumeName+":/var/lib/buildkit",
		"-d",
		"--restart", "always",
		"--privileged",
		c.ctrName,
		"--debug",
	).CombinedOutput(); err != nil {
		if !strings.Contains(
			string(output),
			fmt.Sprintf(`Conflict. The container name "/%s" is already in use by container`, containerName),
		) {
			return nil, errors.Wrapf(err, "failed to run container: %s", output)
		}
	}

	if _, err := os.Stat(helperBinPath); os.IsNotExist(err) {
		tmpbin, err := os.CreateTemp(cacheDir, "dagger-engine-helper")
		if err != nil {
			return nil, err
		}
		defer tmpbin.Close()
		defer os.Remove(tmpbin.Name())

		if output, err := exec.CommandContext(ctx,
			"docker", "cp",
			containerName+":/usr/bin/cloak-"+runtime.GOOS, // TODO: update this name
			tmpbin.Name(),
		).CombinedOutput(); err != nil {
			return nil, errors.Wrapf(err, "failed to copy cloak bin: %s", output)
		}

		if err := tmpbin.Chmod(0700); err != nil {
			return nil, err
		}

		if err := tmpbin.Close(); err != nil {
			return nil, err
		}

		// TODO: verify checksum?

		// this succeeds even if the file already exists and is being executed
		if err := os.Rename(tmpbin.Name(), helperBinPath); err != nil {
			return nil, err
		}

		// TODO: technically it's possible to unlink this after we exec it... could avoid
		// needing to manage state in exchange for no caching of bins (slower start)

	} else if err != nil {
		return nil, err
	}

	args := []string{
		"dial-stdio",
	}
	if cfg.Workdir != "" {
		args = append(args, "--workdir", cfg.Workdir)
	}
	if cfg.ConfigPath != "" {
		args = append(args, "--project", cfg.ConfigPath)
	}

	buildkitHost := "docker-container://" + containerName

	commandConn, err := newCommandConn(ctx, cfg.LogOutput, buildkitHost, helperBinPath, args...)
	if err != nil {
		return nil, err
	}

	wsc, err := newWSClient(ctx, commandConn)
	if err != nil {
		return nil, err
	}
	return wsc, nil

	/*
		return &http.Client{
			Transport: &http2.Transport{
				AllowHTTP: true,
				DialTLS: func(network, addr string, cfg *tls.Config) (net.Conn, error) {
					return commandConn, nil
				},
			},
		}, nil

			return &http.Client{
				Transport: &http.Transport{
					DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
						return commandConn, nil
					},
				},
			}, nil
	*/
}

func (c *Docker) Close() error {
	return nil
}

type wsClient struct {
	conn *websocket.Conn
	reqs map[string]chan *graphql.Response
	mu   sync.Mutex
}

func newWSClient(ctx context.Context, netConn net.Conn) (*wsClient, error) {
	dialer := &websocket.Dialer{
		NetDial: func(network, addr string) (net.Conn, error) {
			return netConn, nil
		},
		HandshakeTimeout: 10 * time.Second,
	}

	wsConn, _, err := dialer.DialContext(ctx, "ws://fake", nil)
	if err != nil {
		return nil, err
	}

	client := &wsClient{
		conn: wsConn,
		reqs: make(map[string]chan *graphql.Response),
	}

	go func() {
		for {
			resp := RespID{
				Response: &graphql.Response{},
			}
			if err := wsConn.ReadJSON(&resp); err != nil {
				fmt.Println("error reading json:", err)
				client.mu.Lock()
				for _, ch := range client.reqs {
					close(ch)
				}
				client.mu.Unlock()
				return
			}
			client.mu.Lock()
			ch, ok := client.reqs[resp.ID]
			if ok {
				ch <- resp.Response
				close(ch)
				delete(client.reqs, resp.ID)
			} else {
				fmt.Println("no channel for response", resp.ID)
			}
			client.mu.Unlock()
		}
	}()

	return client, nil
}

func (d *wsClient) MakeRequest(ctx context.Context, req *graphql.Request, resp *graphql.Response) error {
	id := identity.NewID()

	d.mu.Lock()

	if _, ok := d.reqs[id]; ok {
		d.mu.Unlock()
		return errors.New("request ID already in use")
	}

	ch := make(chan *graphql.Response, 1)
	d.reqs[id] = ch

	if err := d.conn.WriteJSON(ReqID{
		ID:      id,
		Request: req,
	}); err != nil {
		d.mu.Unlock()
		return err
	}
	d.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-ch:
		resp.Errors = r.Errors
		resp.Extensions = r.Extensions
		// TODO: stupid
		bytes, err := json.Marshal(r.Data)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(bytes, resp.Data); err != nil {
			return err
		}
		return nil
	}
}

type ReqID struct {
	ID string `json:"id"`
	*graphql.Request
}

type RespID struct {
	ID string `json:"id"`
	*graphql.Response
}

// New returns net.Conn
func newCommandConn(ctx context.Context, stderr io.Writer, buildkitHost string, cmd string, args ...string) (net.Conn, error) {
	// TODO: doesn't work on windows...
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}
	clientF := os.NewFile(uintptr(fds[0]), "")
	serverF := os.NewFile(uintptr(fds[1]), "")

	proc := exec.CommandContext(ctx, cmd, args...)

	// we assume that args never contains sensitive information
	proc.Env = append(os.Environ(), "BUILDKIT_HOST="+buildkitHost)
	proc.Stderr = stderr
	proc.ExtraFiles = []*os.File{serverF}
	proc.SysProcAttr = &syscall.SysProcAttr{}

	conn, err := net.FileConn(clientF)
	if err != nil {
		return nil, err
	}
	return conn, proc.Start()
}
