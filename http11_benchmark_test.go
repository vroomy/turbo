package turbo

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vroomy/turbo/request"
)

const http11BenchmarkTimeout = 10 * time.Second

type http11Workload struct {
	name         string
	requestWire  []byte
	method       string
	requestBody  []byte
	responseBody []byte
	implicit     bool
	writeSize    int
}

func http11Workloads() []http11Workload {
	small := bytes.Repeat([]byte("x"), 128)
	return []http11Workload{
		{name: "SmallGET", method: http.MethodGet, responseBody: small},
		{name: "ImplicitGET", method: http.MethodGet, responseBody: small, implicit: true},
		{name: "LargeGET", method: http.MethodGet, responseBody: bytes.Repeat([]byte("x"), 64<<10)},
		{name: "POST", method: http.MethodPost, requestBody: bytes.Repeat([]byte("x"), 1<<10), responseBody: small},
		{name: "HEAD", method: http.MethodHead, responseBody: small},
		{name: "Empty", method: http.MethodGet},
		{name: "StreamGET", method: http.MethodGet, responseBody: bytes.Repeat([]byte("x"), 512), writeSize: 128},
	}
}

func (w http11Workload) handler() http.HandlerFunc {
	contentLength := strconv.Itoa(len(w.responseBody))
	return func(out http.ResponseWriter, req *http.Request) {
		if len(w.requestBody) > 0 {
			n, err := io.Copy(io.Discard, req.Body)
			if err != nil || n != int64(len(w.requestBody)) {
				out.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		if !w.implicit {
			out.Header().Set("Content-Length", contentLength)
			if len(w.responseBody) > 0 {
				out.Header().Set("Content-Type", "application/octet-stream")
			}
		}
		if w.writeSize > 0 {
			for start := 0; start < len(w.responseBody); start += w.writeSize {
				if _, err := out.Write(w.responseBody[start:min(start+w.writeSize, len(w.responseBody))]); err != nil {
					return
				}
			}
			return
		}
		_, _ = out.Write(w.responseBody)
	}
}

func (w http11Workload) request() []byte {
	if w.requestWire != nil {
		return w.requestWire
	}
	header := fmt.Sprintf("%s /bench HTTP/1.1\r\nHost: benchmark.local\r\nConnection: close\r\nContent-Length: %d\r\n\r\n", w.method, len(w.requestBody))
	return append([]byte(header), w.requestBody...)
}

func (w http11Workload) expectedBody() []byte {
	if w.method == http.MethodHead {
		return nil
	}
	return w.responseBody
}

func BenchmarkHTTP11NoKeepAlive(b *testing.B) {
	for _, transport := range []string{"TCP", "Pipe"} {
		b.Run(transport, func(b *testing.B) {
			for _, workload := range http11Workloads() {
				b.Run(workload.name, func(b *testing.B) {
					for _, mode := range []string{"Serial", "Parallel"} {
						b.Run(mode, func(b *testing.B) {
							for _, implementation := range []string{"Turbo", "Stdlib"} {
								b.Run(implementation, func(b *testing.B) {
									benchmarkHTTP11(b, transport, implementation, workload, mode == "Parallel")
								})
							}
						})
					}
				})
			}
		})
	}
}

func BenchmarkHTTP11Accept(b *testing.B) {
	workload := http11Workloads()[0]
	for _, transport := range []string{"TCP", "Pipe"} {
		b.Run(transport, func(b *testing.B) {
			for _, workers := range []int{1, 8, 32} {
				b.Run(fmt.Sprintf("Workers%d", workers), func(b *testing.B) {
					for _, queue := range []int{0, 8, 32, 1024} {
						b.Run(fmt.Sprintf("Queue%d", queue), func(b *testing.B) {
							for _, clients := range []int{1, 8, 32, 128} {
								b.Run(fmt.Sprintf("Clients%d", clients), func(b *testing.B) {
									benchmarkHTTP11Config(b, transport, "Turbo", workload,
										http11BenchmarkConfig{workers, queue, clients}, false)
								})
							}
						})
					}
				})
			}
		})
	}
}

type http11BenchmarkConfig struct {
	workers, queue, clients int
}

func defaultHTTP11BenchmarkConfig() http11BenchmarkConfig {
	return http11BenchmarkConfig{workers: 32, queue: 1024, clients: 1}
}

// Isolate request parsing and response writing from clients and connections.
func BenchmarkTurboProcess(b *testing.B) {
	for _, workload := range http11Workloads() {
		b.Run(workload.name, func(b *testing.B) {
			wire := workload.request()
			conn := &http11ProcessConn{}
			server := Turbo{h: workload.handler()}
			writer := makeResponseWriter()
			var parser request.Parser
			goroutines := newBenchmarkGoroutines(b)
			b.ReportAllocs()
			b.SetBytes(int64(len(workload.requestBody) + len(workload.expectedBody())))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				conn.request.Reset(wire)
				if err := server.process(conn, &writer, &parser); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			goroutines.report(b)
		})
	}
}

type http11ProcessConn struct {
	request bytes.Reader
}

func (c *http11ProcessConn) Read(p []byte) (int, error)     { return c.request.Read(p) }
func (*http11ProcessConn) Write(p []byte) (int, error)      { return len(p), nil }
func (*http11ProcessConn) Close() error                     { return nil }
func (*http11ProcessConn) LocalAddr() net.Addr              { return nil }
func (*http11ProcessConn) RemoteAddr() net.Addr             { return nil }
func (*http11ProcessConn) SetDeadline(time.Time) error      { return nil }
func (*http11ProcessConn) SetReadDeadline(time.Time) error  { return nil }
func (*http11ProcessConn) SetWriteDeadline(time.Time) error { return nil }

func benchmarkHTTP11(b *testing.B, transport, implementation string, workload http11Workload, parallel bool) {
	config := defaultHTTP11BenchmarkConfig()
	if parallel {
		config.clients = runtime.GOMAXPROCS(0)
	}
	benchmarkHTTP11Config(b, transport, implementation, workload, config, parallel)
}

func benchmarkHTTP11Config(b *testing.B, transport, implementation string, workload http11Workload, config http11BenchmarkConfig, legacyParallel bool) {
	b.StopTimer()
	goroutines := newBenchmarkGoroutines(b)
	dial := startHTTP11BenchmarkServer(b, transport, implementation, workload.handler(), config)
	request := workload.request()
	client := newHTTP11BenchmarkClient(dial, request, workload)
	if err := client.roundTrip(true); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(workload.requestBody) + len(workload.expectedBody())))
	setupGoroutines := goroutines.delta()
	b.ResetTimer()
	b.StartTimer()
	if legacyParallel {
		b.RunParallel(func(pb *testing.PB) {
			client := newHTTP11BenchmarkClient(dial, request, workload)
			for pb.Next() {
				if err := client.roundTrip(false); err != nil {
					b.Error(err)
					return
				}
			}
		})
	} else if config.clients == 1 {
		for i := 0; i < b.N; i++ {
			if err := client.roundTrip(false); err != nil {
				b.Fatal(err)
			}
		}
	} else {
		runHTTP11Clients(b.N, config.clients, func(_ int, count int) {
			client := newHTTP11BenchmarkClient(dial, request, workload)
			for range count {
				if err := client.roundTrip(false); err != nil {
					b.Error(err)
					return
				}
			}
		})
	}
	b.StopTimer()
	goroutines.report(b)
	b.ReportMetric(float64(setupGoroutines), "setup-goroutines")
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "req/s")
	b.ReportMetric(float64(config.workers), "workers")
	b.ReportMetric(float64(config.queue), "queue")
	b.ReportMetric(float64(config.clients), "clients")
}

// Partition requests without RunParallel's GOMAXPROCS-dependent client count or
// a shared per-request counter. Small calibration runs may use fewer clients.
func runHTTP11Clients(requests, clients int, run func(client, count int)) {
	var done sync.WaitGroup
	start := make(chan struct{})
	for client := range min(requests, clients) {
		count := requests / clients
		if client < requests%clients {
			count++
		}
		done.Go(func() {
			<-start
			run(client, count)
		})
	}
	close(start)
	done.Wait()
}

// The preflight check validates the payload as well as the timed checks for
// HTTP/1.1, status, body length, Connection: close, and EOF on each new connection.
func TestHTTP11BenchmarkContract(t *testing.T) {
	for _, transport := range []string{"TCP", "Pipe"} {
		for _, workload := range http11Workloads() {
			for _, implementation := range []string{"Turbo", "Stdlib"} {
				t.Run(transport+"/"+workload.name+"/"+implementation, func(t *testing.T) {
					dial := startHTTP11BenchmarkServer(t, transport, implementation, workload.handler())
					client := newHTTP11BenchmarkClient(dial, workload.request(), workload)
					for range 2 {
						if err := client.roundTrip(true); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}

func TestTurboConcurrentResponses(t *testing.T) {
	dial := startHTTP11BenchmarkServer(t, "Pipe", "Turbo", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Request", req.URL.Path)
		w.Header().Set("Content-Length", strconv.Itoa(len(req.URL.Path)))
		_, _ = io.WriteString(w, req.URL.Path)
	})
	for client := range 16 {
		t.Run(strconv.Itoa(client), func(t *testing.T) {
			t.Parallel()
			for request := range 16 {
				func() {
					path := fmt.Sprintf("/%d/%d", client, request)
					conn, err := dial()
					if err != nil {
						t.Fatal(err)
					}
					defer conn.Close()
					// Pipe dial installs a watchdog which is canceled by Close.
					if _, err := io.WriteString(conn, "GET "+path+" HTTP/1.1\r\nHost: test.local\r\nConnection: close\r\n\r\n"); err != nil {
						t.Fatal(err)
					}
					in := bufio.NewReader(conn)
					res := readResponse(t, in, http.MethodGet)
					if res.StatusCode != http.StatusOK || res.Header.Get("X-Request") != path {
						t.Fatalf("response for %s = %s, X-Request %q", path, res.Status, res.Header.Get("X-Request"))
					}
					assertResponseBody(t, res, path)
					if _, err := in.ReadByte(); err != io.EOF {
						t.Fatalf("expected connection EOF, got %v", err)
					}
				}()
			}
		})
	}
}

type http11BenchmarkClient struct {
	dial     func() (net.Conn, error)
	request  []byte
	response *bufio.Reader
	method   *http.Request
	expected []byte
	// Diagnostics only; invoked before error cleanup closes the connection.
	onError func(error, net.Conn)
}

func newHTTP11BenchmarkClient(dial func() (net.Conn, error), request []byte, workload http11Workload) *http11BenchmarkClient {
	return &http11BenchmarkClient{
		dial: dial, request: request, response: bufio.NewReader(nil),
		method: &http.Request{Method: workload.method}, expected: workload.expectedBody(),
	}
}

func (c *http11BenchmarkClient) roundTrip(verifyBody bool) (err error) {
	conn, err := c.dial()
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	var body io.ReadCloser
	defer func() {
		if err != nil && c.onError != nil {
			c.onError(err, conn)
		}
		// Close before Body.Close: an invalid response can leave an unread body
		// whose Close would otherwise drain from a stalled peer.
		_ = conn.Close()
		if body != nil {
			_ = body.Close()
		}
		c.response.Reset(nil)
		if pipe, ok := conn.(*http11PipeConn); ok && pipe.timedOut() {
			// A watchdog close must never count as the required server EOF,
			// even if peer closure races with expiry at the end of the response.
			err = fmt.Errorf("pipe round trip watchdog: %w", os.ErrDeadlineExceeded)
		}
	}()
	if _, pipe := conn.(*http11PipeConn); !pipe {
		// TCP keeps native socket deadlines; Pipe uses the dial-installed timer.
		if err := conn.SetDeadline(time.Now().Add(http11BenchmarkTimeout)); err != nil {
			return err
		}
	}
	if n, err := conn.Write(c.request); err != nil {
		return fmt.Errorf("write request: %w", err)
	} else if n != len(c.request) {
		return io.ErrShortWrite
	}
	c.response.Reset(conn)
	res, err := http.ReadResponse(c.response, c.method)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	body = res.Body
	if res.ProtoMajor != 1 || res.ProtoMinor != 1 || res.StatusCode != http.StatusOK || !res.Close {
		return fmt.Errorf("unexpected response: %s %s, Close=%v", res.Proto, res.Status, res.Close)
	}
	if err := c.readBody(res.Body, verifyBody); err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if _, err := c.response.ReadByte(); err != io.EOF {
		return fmt.Errorf("expected connection EOF after response, got %v", err)
	}
	return nil
}

func (c *http11BenchmarkClient) readBody(body io.Reader, verify bool) error {
	if verify {
		got, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, c.expected) {
			return fmt.Errorf("unexpected body: received %d bytes, want %d", len(got), len(c.expected))
		}
		return nil
	}
	n, err := io.Copy(io.Discard, body)
	if err != nil {
		return err
	}
	if n != int64(len(c.expected)) {
		return fmt.Errorf("response body length = %d, want %d", n, len(c.expected))
	}
	return nil
}

func startHTTP11BenchmarkServer(tb testing.TB, transport, implementation string, handler http.HandlerFunc, options ...http11BenchmarkConfig) func() (net.Conn, error) {
	tb.Helper()
	config := defaultHTTP11BenchmarkConfig()
	if len(options) > 0 {
		config = options[0]
	}
	if len(options) > 1 || config.workers < 1 || config.queue < 0 || config.clients < 1 {
		tb.Fatal("invalid HTTP/1.1 benchmark configuration")
	}
	listener, dial := newHTTP11BenchmarkListener(tb, transport)
	startHTTP11BenchmarkServerOnListener(tb, listener, implementation, handler, config)
	return dial
}

func startHTTP11BenchmarkServerOnListener(tb testing.TB, listener net.Listener, implementation string, handler http.HandlerFunc, config http11BenchmarkConfig) *Turbo {
	tb.Helper()
	gate := &http11StartListener{Listener: listener, ready: make(chan struct{})}
	var turbo *Turbo
	switch implementation {
	case "Turbo":
		server := newTurbo(gate, config.workers, config.queue, handler)
		turbo = server
		tb.Cleanup(func() {
			if err := server.Close(); err != nil {
				tb.Error(err)
			}
		})
	case "Stdlib":
		server := &http.Server{Handler: handler}
		server.SetKeepAlivesEnabled(false)
		done := make(chan error, 1)
		go func() { done <- server.Serve(gate) }()
		tb.Cleanup(func() {
			if err := server.Close(); err != nil {
				tb.Error(err)
			}
			if err := <-done; !errors.Is(err, http.ErrServerClosed) {
				tb.Errorf("stdlib Serve: %v", err)
			}
		})
	default:
		tb.Fatalf("unknown implementation %q", implementation)
	}
	// Publish handler initialization before either server can accept a connection.
	close(gate.ready)
	return turbo
}

func newHTTP11BenchmarkListener(tb testing.TB, transport string) (net.Listener, func() (net.Conn, error)) {
	tb.Helper()
	if transport == "Pipe" {
		listener := newHTTP11PipeListener(http11BenchmarkTimeout)
		return listener, listener.dial
	}
	config := net.ListenConfig{KeepAlive: -1}
	listener, err := config.Listen(tb.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: -1}
	addr := listener.Addr().String()
	return listener, func() (net.Conn, error) { return dialer.Dial("tcp", addr) }
}

type http11StartListener struct {
	net.Listener
	ready chan struct{}
}

func (l *http11StartListener) Accept() (net.Conn, error) {
	<-l.ready
	return l.Listener.Accept()
}

type http11PipeListener struct {
	pending chan net.Conn
	closed  chan struct{}
	timeout time.Duration
	once    sync.Once
}

func newHTTP11PipeListener(timeout time.Duration) *http11PipeListener {
	return &http11PipeListener{
		pending: make(chan net.Conn), closed: make(chan struct{}), timeout: timeout,
	}
}

func (l *http11PipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.pending:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *http11PipeListener) dial() (net.Conn, error) {
	rawClient, server := net.Pipe()
	client := newHTTP11PipeConn(rawClient, l.timeout)
	select {
	case l.pending <- server:
		return client, nil
	case <-client.expired:
		client.Close()
		server.Close()
		return nil, fmt.Errorf("pipe dial watchdog: %w", os.ErrDeadlineExceeded)
	case <-l.closed:
		client.Close()
		server.Close()
		return nil, net.ErrClosed
	}
}

func (l *http11PipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (*http11PipeListener) Addr() net.Addr { return &net.TCPAddr{} }

// Own the Pipe timeout rather than calling net.Pipe.SetDeadline: its internal
// AfterFunc timers survive Close and retain the pipe until their deadlines.
// This watchdog uses one timer and one completion channel per fresh connection,
// with no goroutine on successful requests. Only expiry starts a callback.
type http11PipeConn struct {
	net.Conn
	timer   *time.Timer
	expired chan struct{}
	once    sync.Once
}

func newHTTP11PipeConn(conn net.Conn, timeout time.Duration) *http11PipeConn {
	c := &http11PipeConn{Conn: conn, expired: make(chan struct{})}
	c.timer = time.AfterFunc(timeout, func() {
		// Closing one end releases reads and writes on both ends of net.Pipe.
		_ = c.Conn.Close()
		close(c.expired)
	})
	return c
}

func (c *http11PipeConn) Close() error {
	c.once.Do(func() {
		if !c.timer.Stop() {
			// Stop does not join an AfterFunc callback which has already started.
			<-c.expired
		}
	})
	return c.Conn.Close()
}

func (c *http11PipeConn) timedOut() bool {
	select {
	case <-c.expired:
		return true
	default:
		return false
	}
}
