package dockerexec

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"dagger.io/dagger/internal/engineconn"
	"github.com/adrg/xdg"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	exec "golang.org/x/sys/execabs"
)

func init() {
	// TODO: docker-exec is the wrong name
	engineconn.Register("docker-exec", New)
}

var _ engineconn.EngineConn = &DockerExec{}

type DockerExec struct {
	ctrName string
}

func New(u *url.URL) (engineconn.EngineConn, error) {
	return &DockerExec{
		ctrName: u.Host + u.Path,
	}, nil
}

func (c *DockerExec) Connect(ctx context.Context, cfg *engineconn.Config) (*http.Client, error) {
	// TODO: also support using a cloak bin already in $PATH or a user-specified path
	// TODO: currently we'll never clean up old cloak bins, is that okay for now? it's in the user's cache dir so they can feel free to delete them, but that's annoying still.
	// TODO: maybe some sort of dev mode where cloak bin is always thrown away at the end

	cacheDir := filepath.Join(xdg.CacheHome, "dagger")
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return nil, err
	}

	// TODO: just using the imageRef to namespace binary right now, ideally should always be resolved image sha even when that's not provided
	cloakBinPath := filepath.Join(cacheDir, "cloak-"+url.PathEscape(c.ctrName)) // TODO: url escape feels silly
	if _, err := os.Stat(cloakBinPath); os.IsNotExist(err) {
		tmpbin, err := os.CreateTemp(cacheDir, "cloak")
		if err != nil {
			return nil, err
		}
		defer tmpbin.Close()
		defer os.Remove(tmpbin.Name())

		// TODO: we could also use native registry clients for each lang instead of shelling out to docker, but engine still shells out to docker anyways

		// could also use docker cp, but then you have to create a container first which makes it
		// more annoying to handle cleanup corner cases relative to using run w/ --rm
		dockerRunCmd := exec.CommandContext(ctx,
			"docker", "run",
			"--rm",
			"--entrypoint", "/bin/cat",
			c.ctrName,
			"/usr/bin/cloak-"+runtime.GOOS,
		)
		dockerRunCmd.Stdout = tmpbin
		if err := dockerRunCmd.Run(); err != nil {
			// TODO: include stderr
			return nil, err
		}

		if err := tmpbin.Chmod(0700); err != nil {
			return nil, err
		}

		if err := tmpbin.Close(); err != nil {
			return nil, err
		}

		// TODO: verify checksum?

		// if it exists, someone beat us to it, but that's cool
		// TODO: double check IsExist is checking the right syscall err
		if err := os.Rename(tmpbin.Name(), cloakBinPath); err != nil && !os.IsExist(err) {
			return nil, err
		}

	} else if err != nil {
		return nil, err
	}

	args := []string{
		"dial-stdio",
		"--image", c.ctrName,
	}
	if cfg.Workdir != "" {
		args = append(args, "--workdir", cfg.Workdir)
	}
	if cfg.ConfigPath != "" {
		args = append(args, "--project", cfg.ConfigPath)
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				// TODO: need to tell engine what image to use (but to spin it up itself)
				return newCommandConn(ctx, cfg.LogOutput, cloakBinPath, args...)
			},
		},
	}, nil
}

func (c *DockerExec) Close() error {
	return nil
}

// TODO: this is all copied from github.com/docker/cli/cli/connhelper/commandconn
// except now stderr is settable.
// Don't merge this without proper attribution and all that.

// New returns net.Conn
func newCommandConn(ctx context.Context, stderr io.Writer, cmd string, args ...string) (net.Conn, error) {
	var (
		c   commandConn
		err error
	)
	c.cmd = exec.CommandContext(ctx, cmd, args...)
	// we assume that args never contains sensitive information
	c.cmd.Env = os.Environ()
	c.cmd.SysProcAttr = &syscall.SysProcAttr{}
	setPdeathsig(c.cmd)
	createSession(c.cmd)
	c.stdin, err = c.cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	c.stdout, err = c.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c.cmd.Stderr = stderr
	c.localAddr = dummyAddr{network: "dummy", s: "dummy-0"}
	c.remoteAddr = dummyAddr{network: "dummy", s: "dummy-1"}
	return &c, c.cmd.Start()
}

// commandConn implements net.Conn
type commandConn struct {
	cmd           *exec.Cmd
	cmdExited     bool
	cmdWaitErr    error
	cmdMutex      sync.Mutex
	stdin         io.WriteCloser
	stdout        io.ReadCloser
	stderrMu      sync.Mutex
	stderr        bytes.Buffer
	stdioClosedMu sync.Mutex // for stdinClosed and stdoutClosed
	stdinClosed   bool
	stdoutClosed  bool
	localAddr     net.Addr
	remoteAddr    net.Addr
}

// killIfStdioClosed kills the cmd if both stdin and stdout are closed.
func (c *commandConn) killIfStdioClosed() error {
	c.stdioClosedMu.Lock()
	stdioClosed := c.stdoutClosed && c.stdinClosed
	c.stdioClosedMu.Unlock()
	if !stdioClosed {
		return nil
	}
	return c.kill()
}

// killAndWait tries sending SIGTERM to the process before sending SIGKILL.
func killAndWait(cmd *exec.Cmd) error {
	var werr error
	if runtime.GOOS != "windows" {
		werrCh := make(chan error)
		go func() { werrCh <- cmd.Wait() }()
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case werr = <-werrCh:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			werr = <-werrCh
		}
	} else {
		cmd.Process.Kill()
		werr = cmd.Wait()
	}
	return werr
}

// kill returns nil if the command terminated, regardless to the exit status.
func (c *commandConn) kill() error {
	var werr error
	c.cmdMutex.Lock()
	if c.cmdExited {
		werr = c.cmdWaitErr
	} else {
		werr = killAndWait(c.cmd)
		c.cmdWaitErr = werr
		c.cmdExited = true
	}
	c.cmdMutex.Unlock()
	if werr == nil {
		return nil
	}
	wExitErr, ok := werr.(*exec.ExitError)
	if ok {
		if wExitErr.ProcessState.Exited() {
			return nil
		}
	}
	return errors.Wrapf(werr, "commandconn: failed to wait")
}

func (c *commandConn) onEOF(eof error) error {
	// when we got EOF, the command is going to be terminated
	var werr error
	c.cmdMutex.Lock()
	if c.cmdExited {
		werr = c.cmdWaitErr
	} else {
		werrCh := make(chan error)
		go func() { werrCh <- c.cmd.Wait() }()
		select {
		case werr = <-werrCh:
			c.cmdWaitErr = werr
			c.cmdExited = true
		case <-time.After(10 * time.Second):
			c.cmdMutex.Unlock()
			c.stderrMu.Lock()
			stderr := c.stderr.String()
			c.stderrMu.Unlock()
			return errors.Errorf("command %v did not exit after %v: stderr=%q", c.cmd.Args, eof, stderr)
		}
	}
	c.cmdMutex.Unlock()
	if werr == nil {
		return eof
	}
	c.stderrMu.Lock()
	stderr := c.stderr.String()
	c.stderrMu.Unlock()
	return errors.Errorf("command %v has exited with %v, please make sure the URL is valid, and Docker 18.09 or later is installed on the remote host: stderr=%s", c.cmd.Args, werr, stderr)
}

func ignorableCloseError(err error) bool {
	errS := err.Error()
	ss := []string{
		os.ErrClosed.Error(),
	}
	for _, s := range ss {
		if strings.Contains(errS, s) {
			return true
		}
	}
	return false
}

func (c *commandConn) CloseRead() error {
	// NOTE: maybe already closed here
	if err := c.stdout.Close(); err != nil && !ignorableCloseError(err) {
		logrus.Warnf("commandConn.CloseRead: %v", err)
	}
	c.stdioClosedMu.Lock()
	c.stdoutClosed = true
	c.stdioClosedMu.Unlock()
	if err := c.killIfStdioClosed(); err != nil {
		logrus.Warnf("commandConn.CloseRead: %v", err)
	}
	return nil
}

func (c *commandConn) Read(p []byte) (int, error) {
	n, err := c.stdout.Read(p)
	if err == io.EOF {
		err = c.onEOF(err)
	}
	return n, err
}

func (c *commandConn) CloseWrite() error {
	// NOTE: maybe already closed here
	if err := c.stdin.Close(); err != nil && !ignorableCloseError(err) {
		logrus.Warnf("commandConn.CloseWrite: %v", err)
	}
	c.stdioClosedMu.Lock()
	c.stdinClosed = true
	c.stdioClosedMu.Unlock()
	if err := c.killIfStdioClosed(); err != nil {
		logrus.Warnf("commandConn.CloseWrite: %v", err)
	}
	return nil
}

func (c *commandConn) Write(p []byte) (int, error) {
	n, err := c.stdin.Write(p)
	if err == io.EOF {
		err = c.onEOF(err)
	}
	return n, err
}

func (c *commandConn) Close() error {
	var err error
	if err = c.CloseRead(); err != nil {
		logrus.Warnf("commandConn.Close: CloseRead: %v", err)
	}
	if err = c.CloseWrite(); err != nil {
		logrus.Warnf("commandConn.Close: CloseWrite: %v", err)
	}
	return err
}

func (c *commandConn) LocalAddr() net.Addr {
	return c.localAddr
}
func (c *commandConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}
func (c *commandConn) SetDeadline(t time.Time) error {
	logrus.Debugf("unimplemented call: SetDeadline(%v)", t)
	return nil
}
func (c *commandConn) SetReadDeadline(t time.Time) error {
	logrus.Debugf("unimplemented call: SetReadDeadline(%v)", t)
	return nil
}
func (c *commandConn) SetWriteDeadline(t time.Time) error {
	logrus.Debugf("unimplemented call: SetWriteDeadline(%v)", t)
	return nil
}

type dummyAddr struct {
	network string
	s       string
}

func (d dummyAddr) Network() string {
	return d.network
}

func (d dummyAddr) String() string {
	return d.s
}

type stderrWriter struct {
	stderrMu    *sync.Mutex
	stderr      *bytes.Buffer
	debugPrefix string
}

func (w *stderrWriter) Write(p []byte) (int, error) {
	logrus.Debugf("%s%s", w.debugPrefix, string(p))
	w.stderrMu.Lock()
	if w.stderr.Len() > 4096 {
		w.stderr.Reset()
	}
	n, err := w.stderr.Write(p)
	w.stderrMu.Unlock()
	return n, err
}

func createSession(cmd *exec.Cmd) {
	// for supporting ssh connection helper with ProxyCommand
	// https://github.com/docker/cli/issues/1707
	cmd.SysProcAttr.Setsid = true
}
