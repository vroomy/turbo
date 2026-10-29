package turbo

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

func TestHTTP11PipeWatchdogRoundTrip(t *testing.T) {
	const header = "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Length: 4\r\n\r\n"
	for _, test := range []struct {
		name        string
		readRequest bool
		response    string
		closePeer   bool
		wantError   bool
		wantTimeout bool
	}{
		{name: "completed", readRequest: true, response: header + "body", closePeer: true},
		{name: "write-error", closePeer: true, wantError: true},
		{name: "malformed-response", readRequest: true, response: "invalid\r\n", closePeer: true, wantError: true},
		// The rejected status has an unread body on an open connection. Cleanup
		// must close the connection before attempting to drain that body.
		{name: "rejected-status-open-body", readRequest: true, response: "HTTP/1.1 500 Error\r\nConnection: close\r\nContent-Length: 4\r\n\r\n", wantError: true},
		{name: "truncated-body", readRequest: true, response: header + "b", closePeer: true, wantError: true},
		{name: "wrong-body", readRequest: true, response: header + "oops", closePeer: true, wantError: true},
		{name: "extra-data-before-EOF", readRequest: true, response: header + "bodyx", closePeer: true, wantError: true},
		{name: "stalled-write", wantError: true, wantTimeout: true},
		{name: "stalled-headers", readRequest: true, wantError: true, wantTimeout: true},
		{name: "stalled-body", readRequest: true, response: header + "b", wantError: true, wantTimeout: true},
		{name: "stalled-EOF", readRequest: true, response: header + "body", wantError: true, wantTimeout: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			timeout := time.Second
			if test.wantTimeout {
				timeout = 50 * time.Millisecond
			}
			raw, peer := net.Pipe()
			conn := newHTTP11PipeConn(raw, timeout)
			release := make(chan struct{})
			peerDone := make(chan struct{})
			tripDone := make(chan struct{})
			t.Cleanup(func() {
				close(release)
				conn.timer.Stop()
				raw.Close()
				peer.Close()
				for _, done := range []<-chan struct{}{peerDone, tripDone} {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("benchmark client or peer did not stop")
					}
				}
			})
			go func() {
				defer close(peerDone)
				defer peer.Close()
				if test.readRequest {
					req, err := http.ReadRequest(bufio.NewReader(peer))
					if err != nil {
						return
					}
					req.Body.Close()
				}
				if test.response != "" {
					if _, err := io.WriteString(peer, test.response); err != nil {
						return
					}
				}
				if !test.closePeer {
					<-release
				}
			}()
			workload := http11Workload{method: http.MethodGet, responseBody: []byte("body")}
			client := newHTTP11BenchmarkClient(func() (net.Conn, error) { return conn, nil }, workload.request(), workload)
			result := make(chan error, 1)
			go func() {
				defer close(tripDone)
				result <- client.roundTrip(true)
			}()
			select {
			case err := <-result:
				if (err != nil) != test.wantError {
					t.Fatalf("roundTrip error = %v, wantError %v", err, test.wantError)
				}
				if errors.Is(err, os.ErrDeadlineExceeded) != test.wantTimeout {
					t.Fatalf("roundTrip error = %v, wantTimeout %v", err, test.wantTimeout)
				}
				if test.wantTimeout {
					var timeoutError net.Error
					if !errors.As(err, &timeoutError) || !timeoutError.Timeout() {
						t.Fatalf("expected a network timeout, got %v", err)
					}
				}
			case <-time.After(3 * time.Second):
				t.Fatal("roundTrip did not finish within its bound")
			}
			if conn.timer.Stop() {
				t.Fatal("roundTrip left the watchdog timer running")
			}
			if conn.timedOut() != test.wantTimeout {
				t.Fatalf("watchdog expired = %v, want %v", conn.timedOut(), test.wantTimeout)
			}
		})
	}
}

func TestHTTP11PipeWatchdogConcurrentClose(t *testing.T) {
	for range 100 {
		for _, timeout := range []time.Duration{0, time.Hour} {
			raw, peer := net.Pipe()
			conn := newHTTP11PipeConn(raw, timeout)
			var closers sync.WaitGroup
			for range 4 {
				closers.Go(func() { _ = conn.Close() })
			}
			done := make(chan struct{})
			go func() { closers.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("concurrent Close did not join the watchdog")
			}
			if conn.timer.Stop() {
				t.Fatal("Close left the timer running")
			}
			// A broken Close must fail this assertion rather than hang the test.
			_ = peer.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("peer read after Close = %v, want EOF", err)
			}
			peer.Close()
		}
	}
}

func TestHTTP11PipeDial(t *testing.T) {
	for _, mode := range []string{"unaccepted", "closed", "close-while-dialing", "accepted"} {
		t.Run(mode, func(t *testing.T) {
			listener := newHTTP11PipeListener(50 * time.Millisecond)
			t.Cleanup(func() { listener.Close() })
			if mode == "closed" {
				listener.Close()
			}
			result := make(chan error, 1)
			go func() {
				conn, err := listener.dial()
				if conn != nil {
					_ = conn.Close()
				}
				result <- err
			}()
			if mode == "close-while-dialing" {
				listener.Close()
			}
			if mode == "accepted" {
				// Bound Accept independently so a broken dial cannot hang the test.
				accepted := make(chan error, 1)
				go func() {
					conn, err := listener.Accept()
					if conn != nil {
						conn.Close()
					}
					accepted <- err
				}()
				select {
				case err := <-accepted:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("Accept did not finish")
				}
			}
			select {
			case err := <-result:
				switch mode {
				case "unaccepted":
					if !errors.Is(err, os.ErrDeadlineExceeded) {
						t.Fatalf("dial = %v, want deadline exceeded", err)
					}
				case "accepted":
					if err != nil {
						t.Fatal(err)
					}
				default:
					if !errors.Is(err, net.ErrClosed) {
						t.Fatalf("dial = %v, want closed listener", err)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("dial did not finish")
			}
		})
	}
}
