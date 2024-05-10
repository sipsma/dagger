package newserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/Khan/genqlient/graphql"
	"github.com/moby/buildkit/util/bklog"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// a standard http.Server augmented with a ServeConn method and passthrough of
// context from those ServeConn calls to the http.Handler
type httpServer struct {
	*http.Server
	listener *chanListener
}

type httpHandlerWithError func(http.ResponseWriter, *http.Request) error

type httpError struct {
	error
	code int
}

func httpErr(code int, err error) httpError {
	return httpError{err, code}
}

func newHTTPServer(ctx context.Context, handler httpHandlerWithError) *httpServer {
	listenerCtx, listenerClose := context.WithCancel(ctx)

	srv := &httpServer{
		Server: &http.Server{
			ReadHeaderTimeout: 30 * time.Second,
			ConnContext: func(baseCtx context.Context, netConn net.Conn) context.Context {
				conn, ok := netConn.(clientConn)
				if !ok {
					return baseCtx
				}
				return conn.OriginalContext()
			},
			ConnState: func(netConn net.Conn, state http.ConnState) {
				conn, ok := netConn.(clientConn)
				if !ok {
					return
				}

				// NOTE: we don't handle http.StateHijacked here because either:
				// A) the server-side hijacker will close the conn when done
				// B) the client-side hijacker will close the conn when done which will cancel
				//		the context passed to the server and unblock ServeConn
				//
				// We also close on StateIdle because we don't currently support keep-alive on
				// the client side.
				switch state {
				case http.StateClosed, http.StateIdle:
					conn.Close()
				}
			},
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer func() {
					if v := recover(); v != nil {
						bklog.G(context.TODO()).Errorf("panic serving schema: %v %s", v, string(debug.Stack()))
						// check whether this is a hijacked connection, if so we can't write any http errors to it
						_, err := w.Write(nil)
						if err == http.ErrHijacked {
							return
						}
						gqlErr := &gqlerror.Error{
							Message: "Internal Server Error",
						}
						code := http.StatusInternalServerError
						switch v := v.(type) {
						case error:
							gqlErr.Err = v
							gqlErr.Message = v.Error()
						case string:
							gqlErr.Message = v
						}
						res := graphql.Response{
							Errors: gqlerror.List{gqlErr},
						}
						bytes, err := json.Marshal(res)
						if err != nil {
							panic(err)
						}
						http.Error(w, string(bytes), code)
					}
				}()

				if err := handler(w, r); err != nil {
					var httpErr httpError
					if errors.As(err, &httpErr) {
						w.WriteHeader(httpErr.code)
					} else {
						w.WriteHeader(http.StatusInternalServerError)
					}
				}
			}),
		},
		listener: &chanListener{
			ctx:           listenerCtx,
			close:         listenerClose,
			incomingConns: make(chan net.Conn),
		},
	}

	go srv.Serve(srv.listener)

	return srv
}

// ServeConn accepts a net.Conn and serves it with the http.Handler, blocking until
// the request is done or the context is canceled.
func (srv *httpServer) ServeConn(ctx context.Context, conn clientConn) error {
	select {
	case srv.listener.incomingConns <- conn:
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case <-conn.Closed():
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

type chanListener struct {
	ctx           context.Context
	close         context.CancelFunc
	incomingConns chan net.Conn
}

var _ net.Listener = &chanListener{}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.incomingConns:
		return conn, nil
	case <-l.ctx.Done():
		return nil, io.ErrClosedPipe
	}
}

func (l *chanListener) Close() error {
	l.close()
	return nil
}

func (l *chanListener) Addr() net.Addr {
	return nil
}

type clientConn interface {
	net.Conn
	Closed() <-chan struct{}
	OriginalContext() context.Context
}

type networkClientConn struct {
	net.Conn
	origCtx   context.Context
	closed    chan struct{}
	closeOnce sync.Once
}

var _ clientConn = &networkClientConn{}

func newNetworkClientConn(ctx context.Context, conn net.Conn) *networkClientConn {
	return &networkClientConn{
		Conn:    conn,
		origCtx: ctx,
		closed:  make(chan struct{}),
	}
}

func (conn *networkClientConn) Closed() <-chan struct{} {
	return conn.closed
}

func (conn *networkClientConn) OriginalContext() context.Context {
	return conn.origCtx
}

func (conn *networkClientConn) Close() error {
	conn.closeOnce.Do(func() {
		close(conn.closed)
	})
	return conn.Conn.Close()
}

// wraps the underlying conn returned by buildkit's grpchijack.Hijack with:
// * actually implemented deadline methods (since some libraries rely on those working)
// * chunked writes to avoid grpc max message sizes
// * a Close that just signals callers it's done rather than actually closing anything yet
// * tracking of "origCtx" so we can pass through the grpc stream context to http handlers
type grpcClientConn struct {
	inner   net.Conn
	origCtx context.Context

	readDeadline  time.Time
	readers       []func()
	readBuf       *bytes.Buffer
	readEOF       bool
	readCond      *sync.Cond
	writeDeadline time.Time
	writers       []func()
	writersL      sync.Mutex

	maxMsgSize int

	closed    chan struct{}
	closeOnce sync.Once
}

var _ clientConn = &grpcClientConn{}

func newGRPCClientConn(ctx context.Context, inner net.Conn, maxMsgSize int) *grpcClientConn {
	conn := &grpcClientConn{
		inner:      inner,
		origCtx:    ctx,
		readBuf:    new(bytes.Buffer),
		readCond:   sync.NewCond(new(sync.Mutex)),
		maxMsgSize: maxMsgSize,
		closed:     make(chan struct{}),
	}

	go func() {
		for {
			buf := make([]byte, 32*1024)
			n, err := inner.Read(buf)
			if err != nil {
				conn.readCond.L.Lock()
				conn.readEOF = true
				conn.readCond.L.Unlock()
				conn.readCond.Broadcast()
				return
			}

			conn.readCond.L.Lock()
			conn.readBuf.Write(buf[0:n])
			conn.readCond.Broadcast()
			conn.readCond.L.Unlock()
		}
	}()

	return conn
}

func (conn *grpcClientConn) Closed() <-chan struct{} {
	return conn.closed
}

func (conn *grpcClientConn) OriginalContext() context.Context {
	return conn.origCtx
}

// We don't want to actually close it, we just want the Controller.Session method to return.
// So we just signal that close has been called.
func (conn *grpcClientConn) Close() error {
	conn.closeOnce.Do(func() {
		close(conn.closed)
	})
	return nil
}

func (conn *grpcClientConn) Read(b []byte) (n int, err error) {
	conn.readCond.L.Lock()

	if conn.readEOF {
		conn.readCond.L.Unlock()
		return 0, io.EOF
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !conn.readDeadline.IsZero() {
		if time.Now().After(conn.readDeadline) {
			conn.readCond.L.Unlock()
			// return early without calling inner Read
			return 0, os.ErrDeadlineExceeded
		}

		go func() {
			dt := time.Until(conn.readDeadline)
			if dt > 0 {
				time.Sleep(dt)
			}

			cancel()
		}()
	}

	// Keep track of the reader so a future SetReadDeadline can interrupt it.
	conn.readers = append(conn.readers, cancel)

	conn.readCond.L.Unlock()

	// Start a goroutine for the actual Read operation
	read := make(chan struct{})
	var rN int
	var rerr error
	go func() {
		defer close(read)

		conn.readCond.L.Lock()
		defer conn.readCond.L.Unlock()

		for ctx.Err() == nil {
			if conn.readEOF {
				rerr = io.EOF
				break
			}

			n, _ := conn.readBuf.Read(b) // ignore EOF here
			if n > 0 {
				rN = n
				break
			}

			conn.readCond.Wait()
		}
	}()

	// Wait for either Read to complete or the timeout
	select {
	case <-read:
		return rN, rerr
	case <-ctx.Done():
		return 0, os.ErrDeadlineExceeded
	}
}

func (conn *grpcClientConn) Write(b []byte) (n int, err error) {
	conn.writersL.Lock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !conn.writeDeadline.IsZero() {
		if time.Now().After(conn.writeDeadline) {
			conn.writersL.Unlock()
			// return early without calling inner Write
			return 0, os.ErrDeadlineExceeded
		}

		go func() {
			dt := time.Until(conn.writeDeadline)
			if dt > 0 {
				time.Sleep(dt)
			}

			cancel()
		}()
	}

	// Keep track of the writer so a future SetWriteDeadline can interrupt it.
	conn.writers = append(conn.writers, cancel)
	conn.writersL.Unlock()

	// Start a goroutine for the actual Write operation
	write := make(chan struct{})
	go func() {
		defer close(write)
		// split write into chunks of conn.maxMsgSize
		for {
			if len(b) == 0 {
				return
			}

			var bnext []byte
			if len(b) > conn.maxMsgSize {
				b, bnext = b[:conn.maxMsgSize], b[conn.maxMsgSize:]
			}

			n2, err2 := conn.inner.Write(b)
			n += n2
			err = err2
			if err != nil {
				return
			}

			b = bnext
		}
	}()

	// Wait for either Write to complete or the timeout
	select {
	case <-write:
		return n, err
	case <-ctx.Done():
		return 0, os.ErrDeadlineExceeded
	}
}

func (conn *grpcClientConn) LocalAddr() net.Addr {
	return conn.inner.LocalAddr()
}

func (conn *grpcClientConn) RemoteAddr() net.Addr {
	return conn.inner.RemoteAddr()
}

func (conn *grpcClientConn) SetDeadline(t time.Time) error {
	return errors.Join(
		conn.SetReadDeadline(t),
		conn.SetWriteDeadline(t),
	)
}

func (conn *grpcClientConn) SetReadDeadline(t time.Time) error {
	conn.readCond.L.Lock()
	conn.readDeadline = t
	readers := conn.readers
	conn.readCond.L.Unlock()

	if len(readers) > 0 && !t.IsZero() {
		go func() {
			dt := time.Until(conn.readDeadline)
			if dt > 0 {
				time.Sleep(dt)
			}

			for _, cancel := range readers {
				cancel()
			}
		}()
	}

	return nil
}

func (conn *grpcClientConn) SetWriteDeadline(t time.Time) error {
	conn.writersL.Lock()
	conn.writeDeadline = t
	writers := conn.writers
	conn.writersL.Unlock()

	if len(writers) > 0 && !t.IsZero() {
		go func() {
			dt := time.Until(conn.writeDeadline)
			if dt > 0 {
				time.Sleep(dt)
			}

			for _, cancel := range writers {
				cancel()
			}
		}()
	}

	return nil
}

// flushAfterNBytes creates a middleware that flushes the response
// after every N bytes.
//
// This is used to support streaming requests over gRPC, which may need
// chunking in order to not exceed the max gRPC message size.
func flushAfterNBytes(n int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w,
					"Streaming unsupported: ResponseWriter does not implement http.Flusher",
					http.StatusInternalServerError)
				return
			}

			hijacker, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w,
					"Streaming unsupported: ResponseWriter does not implement http.Hijacker",
					http.StatusInternalServerError)
				return
			}

			flushWriter := &flushAfterNBytesWriter{
				ResponseWriter: w,
				Flusher:        flusher,
				Hijacker:       hijacker,
				limit:          n,
				mu:             &sync.Mutex{},
			}

			next.ServeHTTP(flushWriter, r)
		})
	}
}

type flushAfterNBytesWriter struct {
	http.ResponseWriter
	http.Flusher
	http.Hijacker

	limit   int
	written int
	mu      *sync.Mutex
}

func (w *flushAfterNBytesWriter) Write(bytes []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err := w.ResponseWriter.Write(bytes)
	if err != nil {
		return n, err
	}

	w.written += n
	if w.written >= w.limit {
		w.Flusher.Flush()
		w.written = 0
	}

	return n, nil
}
