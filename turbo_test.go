package turbo

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name         string
		workers      int
		queue        int
		occupiedPort bool
	}{
		{name: "unbuffered queue", workers: 1},
		{name: "buffered queue", workers: 3, queue: 4},
		{name: "occupied port", workers: 1, occupiedPort: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var port uint16
			if tt.occupiedPort {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { listener.Close() })
				port = uint16(listener.Addr().(*net.TCPAddr).Port)
			}

			server, err := New(port, tt.workers, tt.queue)
			if server != nil {
				t.Cleanup(func() { closeTurbo(t, server) })
			}
			if tt.occupiedPort {
				if server != nil || err == nil {
					t.Fatalf("New = (%v, %v), want (nil, listen error)", server, err)
				}
				return
			}
			if err != nil || server == nil {
				t.Fatalf("New = (%v, %v), want a running server", server, err)
			}
			addr := server.l.Addr().(*net.TCPAddr)
			if !addr.IP.IsLoopback() || addr.Port == 0 {
				t.Fatalf("unexpected listener address: %s", addr)
			}
			if err := closeTurbo(t, server); err != nil {
				t.Fatal(err)
			}
			if _, err := server.l.Accept(); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("Accept after Close = %v, want net.ErrClosed", err)
			}
		})
	}
}

func TestTurboProcess(t *testing.T) {
	tests := []struct {
		name, method, target, protocol string
		requestBody, body              string
		status                         int
		handler                        http.HandlerFunc
	}{
		{name: "empty handler", method: http.MethodGet, target: "/", status: http.StatusOK, handler: func(http.ResponseWriter, *http.Request) {}},
		{name: "headers only", method: http.MethodGet, target: "/accepted", status: http.StatusAccepted, handler: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}},
		{name: "multiple body writes", method: http.MethodGet, target: "/hello?name=world", body: "hello world", status: http.StatusCreated, handler: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("hello"))
			_, _ = w.Write([]byte(" world"))
		}},
		{name: "head", method: http.MethodHead, target: "/hello", status: http.StatusOK, handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("hello"))
		}},
		{name: "post body", method: http.MethodPost, target: "/items", requestBody: "payload", body: "created", status: http.StatusCreated, handler: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("created"))
		}},
		{name: "http 1.0", method: http.MethodGet, target: "/legacy", protocol: "HTTP/1.0", body: "legacy", status: http.StatusOK, handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("legacy"))
		}},
		{name: "no content", method: http.MethodGet, target: "/empty", status: http.StatusNoContent, handler: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, server := net.Pipe()
			t.Cleanup(func() {
				client.Close()
				server.Close()
			})
			deadline := time.Now().Add(5 * time.Second)
			if err := client.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			if err := server.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			turbo := Turbo{h: func(w http.ResponseWriter, req *http.Request) {
				if req.Method != tt.method || req.RequestURI != tt.target || req.Host != "example.com" {
					t.Errorf("unexpected request: %s %s, Host %q", req.Method, req.RequestURI, req.Host)
				}
				body, err := io.ReadAll(req.Body)
				if err != nil || string(body) != tt.requestBody {
					t.Errorf("request body = %q, error = %v, want %q", body, err, tt.requestBody)
				}
				tt.handler(w, req)
			}}
			done := make(chan error, 1)
			go func() { done <- turbo.process(server) }()
			protocol := tt.protocol
			if protocol == "" {
				protocol = "HTTP/1.1"
			}
			request := fmt.Sprintf("%s %s %s\r\nHost: example.com\r\nContent-Length: %d\r\n\r\n%s", tt.method, tt.target, protocol, len(tt.requestBody), tt.requestBody)
			if _, err := io.WriteString(client, request); err != nil {
				t.Fatal(err)
			}
			in := bufio.NewReader(client)
			res := readResponse(t, in, tt.method)
			if res.StatusCode != tt.status {
				t.Errorf("status = %d, want %d", res.StatusCode, tt.status)
			}
			assertResponseBody(t, res, tt.body)
			if extra, err := io.ReadAll(in); err != nil || len(extra) != 0 {
				t.Fatalf("bytes after response = %q, error = %v", extra, err)
			}
			if err := waitForTurboClose(t, done); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTurboProcessErrors(t *testing.T) {
	const request = "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"
	readFailure := errors.New("read failed")
	writeFailure := errors.New("write failed")
	closeFailure := errors.New("close failed")
	tests := []struct {
		name, request, wantMessage  string
		readErr, writeErr, closeErr error
		failWrite                   int
		wantErr                     error
		wantHandler                 bool
	}{
		{name: "empty request", wantErr: io.EOF},
		{name: "truncated headers", request: "GET / HTTP/1.1\r\nHost: example.com\r\n", wantErr: io.ErrUnexpectedEOF},
		{name: "malformed request", request: "not an HTTP request\r\n\r\n", wantMessage: "error reading request"},
		{name: "read failure", request: request, readErr: readFailure, wantErr: readFailure},
		{name: "header write failure", request: request, failWrite: 1, writeErr: writeFailure, wantErr: writeFailure, wantHandler: true},
		{name: "body write failure", request: request, failWrite: 2, writeErr: writeFailure, wantErr: writeFailure, wantHandler: true},
		{name: "short header write", request: request, failWrite: 1, wantErr: io.ErrShortWrite, wantHandler: true},
		{name: "close failure", request: request, closeErr: closeFailure, wantErr: closeFailure, wantHandler: true},
		{name: "read error survives close failure", request: request, readErr: readFailure, closeErr: closeFailure, wantErr: readFailure},
		{name: "write error survives close failure", request: request, failWrite: 2, writeErr: writeFailure, closeErr: closeFailure, wantErr: writeFailure, wantHandler: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := newTurboTestConn(tt.request)
			conn.readErr = tt.readErr
			conn.failAt = tt.failWrite
			conn.err = tt.writeErr
			conn.closeErr = tt.closeErr
			called := false
			server := Turbo{h: func(w http.ResponseWriter, _ *http.Request) {
				called = true
				_, _ = w.Write([]byte("response"))
			}}
			err := server.process(conn)
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("process error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantMessage != "" && (err == nil || !strings.Contains(err.Error(), tt.wantMessage)) {
				t.Errorf("process error = %v, want message containing %q", err, tt.wantMessage)
			}
			if called != tt.wantHandler {
				t.Errorf("handler called = %v, want %v", called, tt.wantHandler)
			}
			if got := conn.closeCount.Load(); got != 1 {
				t.Errorf("connection closed %d times, want once", got)
			}
		})
	}
}

func TestTurboRequestBodyLifecycle(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		consume bool
		replace bool
	}{
		{name: "unread body", body: "body"},
		{name: "consumed body", body: "body", consume: true},
		{name: "handler replaces body", body: "body", replace: true},
		{name: "client never sends declared body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, conn := net.Pipe()
			t.Cleanup(func() {
				client.Close()
				conn.Close()
			})
			deadline := time.Now().Add(5 * time.Second)
			if err := client.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			if err := conn.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			var body io.ReadCloser
			server := Turbo{h: func(_ http.ResponseWriter, req *http.Request) {
				body = req.Body
				if tt.consume {
					if _, err := io.Copy(io.Discard, req.Body); err != nil {
						t.Error(err)
					}
				}
				if tt.replace {
					req.Body = http.NoBody
				}
			}}
			done := make(chan error, 1)
			go func() { done <- server.process(conn) }()
			request := "POST / HTTP/1.1\r\nHost: example.com\r\nContent-Length: 4\r\n\r\n" + tt.body
			if _, err := io.WriteString(client, request); err != nil {
				t.Fatal(err)
			}
			res := readResponse(t, client, http.MethodPost)
			assertResponseBody(t, res, "")
			if err := waitForTurboClose(t, done); err != nil {
				t.Fatal(err)
			}
			if _, err := body.Read(make([]byte, 1)); !errors.Is(err, http.ErrBodyReadAfterClose) {
				t.Fatalf("read after handler returned = %v, want ErrBodyReadAfterClose", err)
			}
		})
	}
}

func TestTurboWorkers(t *testing.T) {
	tests := []struct {
		name           string
		workers, queue int
		acceptError    bool
		badRequest     bool
	}{
		{name: "one worker without buffering", workers: 1},
		{name: "one worker drains queued connections", workers: 1, queue: 3},
		{name: "concurrent workers", workers: 3},
		{name: "concurrent workers drain queue", workers: 3, queue: 4},
		{name: "accept error does not stop serving", workers: 2, queue: 1, acceptError: true},
		{name: "bad request does not stop worker", workers: 1, queue: 2, badRequest: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := tt.workers + tt.queue
			listener := newTurboTestListener(count + 2)
			server := newTurbo(listener, tt.workers, tt.queue)
			if server == nil {
				t.Fatal("newTurbo returned nil")
			}
			release := make(chan struct{})
			var releaseOnce sync.Once
			finishHandlers := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(func() {
				finishHandlers()
				listener.Close()
				if err := closeTurbo(t, server); err != nil {
					t.Error(err)
				}
			})
			started := make(chan struct{}, count)
			server.h = func(w http.ResponseWriter, req *http.Request) {
				started <- struct{}{}
				<-release
				_, _ = io.WriteString(w, req.RequestURI)
			}
			if tt.acceptError {
				listener.pending <- turboAcceptResult{err: errors.New("temporary accept failure")}
			}
			var badConn *turboTestConn
			if tt.badRequest {
				badConn = newTurboTestConn("invalid request\r\n\r\n")
				listener.pending <- turboAcceptResult{conn: badConn}
			}
			connections := make([]*turboTestConn, count)
			for i := range connections {
				connections[i] = newTurboTestConn(fmt.Sprintf("GET /request/%d HTTP/1.1\r\nHost: example.com\r\n\r\n", i))
				listener.pending <- turboAcceptResult{conn: connections[i]}
			}
			for range tt.workers {
				waitForTurboSignal(t, started, "worker to enter handler")
			}
			accepted := count
			if tt.badRequest {
				accepted++
			}
			for range accepted {
				waitForTurboSignal(t, listener.accepted, "listener to accept connection")
			}

			done := make(chan error, 1)
			go func() { done <- server.Close() }()
			waitForTurboSignal(t, listener.closed, "Close to stop listener")
			select {
			case err := <-done:
				t.Fatalf("Close returned before active handlers finished: %v", err)
			default:
			}
			finishHandlers()
			if err := waitForTurboClose(t, done); err != nil {
				t.Fatal(err)
			}
			for i, conn := range connections {
				if got := conn.closeCount.Load(); got != 1 {
					t.Errorf("connection %d closed %d times, want once", i, got)
				}
				res := readResponse(t, &conn.data, http.MethodGet)
				assertResponseBody(t, res, fmt.Sprintf("/request/%d", i))
			}
			if badConn != nil && badConn.closeCount.Load() != 1 {
				t.Error("failed request connection was not closed exactly once")
			}
			if err := server.Close(); err != nil {
				t.Errorf("second Close = %v", err)
			}
		})
	}
}

func TestTurboClose(t *testing.T) {
	closeFailure := errors.New("listener close failed")
	tests := []struct {
		name          string
		alreadyClosed bool
		closeErr      error
	}{
		{name: "idle server"},
		{name: "listener already closed", alreadyClosed: true},
		{name: "listener close failure", closeErr: closeFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listener := newTurboTestListener(1)
			listener.closeErr = tt.closeErr
			server := newTurbo(listener, 2, 0)
			if server == nil {
				t.Fatal("newTurbo returned nil")
			}
			t.Cleanup(func() { closeTurbo(t, server) })
			if tt.alreadyClosed {
				listener.Close()
			}
			if err := closeTurbo(t, server); !errors.Is(err, tt.closeErr) {
				t.Errorf("Close = %v, want %v", err, tt.closeErr)
			}
			if err := server.Close(); err != nil {
				t.Errorf("second Close = %v, want nil", err)
			}
		})
	}
}

func closeTurbo(t *testing.T, server *Turbo) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- server.Close() }()
	return waitForTurboClose(t, done)
}

func waitForTurboClose(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Turbo to finish")
		return nil
	}
}

func waitForTurboSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

type turboTestConn struct {
	responseConn
	input      io.Reader
	readErr    error
	closeErr   error
	closeCount atomic.Int32
}

func newTurboTestConn(request string) *turboTestConn {
	return &turboTestConn{input: strings.NewReader(request)}
}

func (c *turboTestConn) Read(p []byte) (int, error) {
	if c.readErr != nil {
		return 0, c.readErr
	}
	return c.input.Read(p)
}

func (c *turboTestConn) Close() error {
	c.closeCount.Add(1)
	return c.closeErr
}

type turboAcceptResult struct {
	conn net.Conn
	err  error
}

type turboTestListener struct {
	pending    chan turboAcceptResult
	accepted   chan struct{}
	closed     chan struct{}
	closeErr   error
	closeCount atomic.Int32
}

func newTurboTestListener(capacity int) *turboTestListener {
	return &turboTestListener{
		pending:  make(chan turboAcceptResult, capacity),
		accepted: make(chan struct{}, capacity),
		closed:   make(chan struct{}),
	}
}

func (l *turboTestListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case result := <-l.pending:
		if result.err == nil {
			l.accepted <- struct{}{}
		}
		return result.conn, result.err
	}
}

func (l *turboTestListener) Close() error {
	if l.closeCount.Add(1) != 1 {
		return net.ErrClosed
	}
	close(l.closed)
	return l.closeErr
}

func (*turboTestListener) Addr() net.Addr { return &net.TCPAddr{} }

var _ net.Conn = (*turboTestConn)(nil)
var _ net.Listener = (*turboTestListener)(nil)
