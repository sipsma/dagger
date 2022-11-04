package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Khan/genqlient/graphql"
	"github.com/dagger/dagger/engine"
	"github.com/dagger/dagger/router"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

var dialStdioCmd = &cobra.Command{
	Use:    "dial-stdio",
	Run:    DialStdio,
	Hidden: true,
}

func DialStdio(cmd *cobra.Command, args []string) {
	startOpts := &engine.Config{
		Workdir:    workdir,
		ConfigPath: configPath,
		LogOutput:  os.Stderr,
	}

	err := engine.Start(context.Background(), startOpts, func(ctx context.Context, r *router.Router) error {
		/*
			srv := http.Server{
				Handler:           r,
				ReadHeaderTimeout: 30 * time.Second,
			}

			closeCh := make(chan struct{})

			l := &stdioConnListener{
				closeCh: closeCh,
			}

			go func() {
				err := srv.Serve(l)
				if err != nil && err != http.ErrServerClosed {
					fmt.Fprintf(os.Stderr, "http serve: error: %v\n", err)
				}
			}()

			<-closeCh
			return srv.Shutdown(ctx)
		*/

		/*
			// TODO: don't think closech needed now
			closeCh := make(chan struct{})
			(&http2.Server{}).ServeConn(&stdioConn{
				closeCh: closeCh,
			}, &http2.ServeConnOpts{
				Context: ctx,
				Handler: r,
			})
			<-closeCh
			return nil
		*/

		closeCh := make(chan struct{})
		f := os.NewFile(3, "")
		conn, err := net.FileConn(f)
		if err != nil {
			return err
		}
		l := &singleConnListener{
			conn: conn,
		}

		srv := http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, httpReq *http.Request) {
				ws, err := (&websocket.Upgrader{}).Upgrade(w, httpReq, nil)
				if err != nil {
					panic(err)
				}
				defer ws.Close()
				for {
					var req ReqID
					err := ws.ReadJSON(&req)
					if err != nil {
						panic(err)
					}
					go func() {
						id := req.ID
						vars := make(map[string]interface{})
						bytes, err := json.Marshal(req.Variables)
						if err != nil {
							panic(err)
						}
						if err := json.Unmarshal(bytes, &vars); err != nil {
							panic(err)
						}
						data := make(map[string]interface{})
						res, err := r.Do(ctx, req.Query, vars, &data)
						if err != nil {
							panic(err)
						}
						resp := RespID{
							ID: id,
							Response: &graphql.Response{
								Data:       data,
								Extensions: res.Extensions,
							},
						}
						if res.HasErrors() {
							for _, err := range res.Errors {
								resp.Response.Errors = append(resp.Response.Errors, &gqlerror.Error{
									Message: err.Error(),
								})
							}
						}
						err = ws.WriteJSON(resp)
						if err != nil {
							panic(err)
						}
					}()
				}
			}),
			ReadHeaderTimeout: 30 * time.Second,
		}

		go func() {
			err := srv.Serve(l)
			if err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "http serve: error: %v\n", err)
			}
		}()

		<-closeCh
		return srv.Shutdown(ctx)
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
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

// converts a pre-existing net.Conn into a net.Listener that returns the conn
type singleConnListener struct {
	conn net.Conn
	l    sync.Mutex
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	l.l.Lock()
	defer l.l.Unlock()

	if l.conn == nil {
		return nil, io.ErrClosedPipe
	}
	c := l.conn
	l.conn = nil
	return c, nil
}

func (l *singleConnListener) Addr() net.Addr {
	return nil
}

func (l *singleConnListener) Close() error {
	return nil
}

var _ net.Listener = &stdioConnListener{}

type stdioConnListener struct {
	once    sync.Once
	closeCh chan struct{}
}

func (l *stdioConnListener) Accept() (net.Conn, error) {
	var conn net.Conn
	l.once.Do(func() {
		conn = &stdioConn{
			closeCh: l.closeCh,
		}
	})
	// Return stdio connection ONLY on the first `Accept()`
	// since we can't multiplex on stdio.
	if conn != nil {
		return conn, nil
	}

	// For the other `Accept()`, block until the server is closed
	<-l.closeCh
	return nil, net.ErrClosed
}

func (l *stdioConnListener) Addr() net.Addr {
	return nil
}

func (l *stdioConnListener) Close() error {
	return nil
}

var _ net.Conn = &stdioConn{}

type stdioConn struct {
	closeCh chan struct{}
}

func (c *stdioConn) Read(b []byte) (n int, err error) {
	return os.Stdin.Read(b)
}

func (c *stdioConn) Write(b []byte) (n int, err error) {
	return os.Stdout.Write(b)
}

func (c *stdioConn) Close() error {
	close(c.closeCh)
	return nil
}

func (c *stdioConn) LocalAddr() net.Addr {
	return &net.IPAddr{}
}

func (c *stdioConn) RemoteAddr() net.Addr {
	return &net.IPAddr{}
}

func (c *stdioConn) SetDeadline(t time.Time) error {
	return nil
}

func (c *stdioConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (c *stdioConn) SetWriteDeadline(t time.Time) error {
	return nil
}
