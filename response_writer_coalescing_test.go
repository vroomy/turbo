package turbo

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vroomy/turbo/request"
)

const coalescingTestHeader = "HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nConnection: close\r\n\r\n"

func coalescingTestWriter(c *responseConn) responseWriter {
	r := makeResponseWriter()
	r.c = c
	r.Header()["Date"] = nil
	r.Header().Set("Content-Type", "application/octet-stream")
	return r
}

func TestResponseWriterCoalescing(t *testing.T) {
	for _, tt := range []struct {
		name   string
		size   int
		header string
		calls  int
	}{
		{name: "small", size: 128, calls: 1},
		{name: "at limit", size: maxCoalescedResponseSize - len(coalescingTestHeader), calls: 1},
		{name: "over limit", size: maxCoalescedResponseSize - len(coalescingTestHeader) + 1, calls: 2},
		{name: "large body", size: 64 << 10, calls: 2},
		{name: "large header", size: 128, header: strings.Repeat("x", 32<<10), calls: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := new(responseConn)
			r := coalescingTestWriter(c)
			wantHeader := coalescingTestHeader
			if tt.header != "" {
				r.Header().Set("X-Large", tt.header)
				wantHeader = strings.Replace(wantHeader, "Connection: close", "X-Large: "+tt.header+"\r\nConnection: close", 1)
			}
			body := bytes.Repeat([]byte("x"), tt.size)
			if n, err := r.Write(body); n != len(body) || err != nil {
				t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(body))
			}
			if c.calls != tt.calls || c.data.String() != wantHeader+string(body) {
				t.Fatalf("got %d writes and %d wire bytes, want %d writes and %d exact bytes", c.calls, c.data.Len(), tt.calls, len(wantHeader)+len(body))
			}
			if r.written != int64(len(body)) {
				t.Fatalf("written = %d, want %d body bytes", r.written, len(body))
			}
			if tt.calls == 2 && r.headerBuffer.Len() != len(wantHeader) {
				t.Fatal("copied an oversized response into the header buffer")
			}
			if n, err := r.writeHeader(); n != 0 || err != nil || c.calls != tt.calls {
				t.Fatalf("finalizing repeated the write: (%d, %v), calls=%d", n, err, c.calls)
			}
			r.reset()
			if r.c != nil || r.err != nil || r.sentHeader || r.written != 0 || r.headerBuffer.Len() != 0 || r.headerBuffer.Cap() > maxRetainedHeaderCapacity {
				t.Fatalf("response state or oversized storage retained after reset: %+v", r)
			}
			r.populateHeaders()
			r.c = c
			c.data.Reset()
			if _, err := r.Write([]byte("next")); err != nil {
				t.Fatal(err)
			}
			assertResponseBody(t, readResponse(t, &c.data, http.MethodGet), "next")
		})
	}
}

func TestResponseWriterCoalescedPartialWrites(t *testing.T) {
	wire := coalescingTestHeader + "body"
	sentinel := errors.New("combined write failed")
	for _, wireN := range []int{0, 1, len(coalescingTestHeader) - 1, len(coalescingTestHeader), len(coalescingTestHeader) + 1, len(wire) - 1, len(wire)} {
		for _, writeErr := range []error{nil, sentinel} {
			t.Run(fmt.Sprintf("%d/%v", wireN, writeErr), func(t *testing.T) {
				c := &responseConn{failAt: 1, n: wireN, err: writeErr}
				r := coalescingTestWriter(c)
				wantN := max(0, wireN-len(coalescingTestHeader))
				wantErr := writeErr
				if wireN < len(wire) && wantErr == nil {
					wantErr = io.ErrShortWrite
				}
				if n, err := r.Write([]byte("body")); n != wantN || !errors.Is(err, wantErr) {
					t.Fatalf("Write = (%d, %v), want (%d, %v)", n, err, wantN, wantErr)
				}
				if r.written != int64(wantN) || c.data.String() != wire[:wireN] || c.calls != 1 {
					t.Fatalf("written=%d, wire=%q, calls=%d", r.written, c.data.String(), c.calls)
				}
				if wantErr != nil {
					if n, err := r.Write([]byte("again")); n != 0 || !errors.Is(err, wantErr) {
						t.Fatalf("sticky Write = (%d, %v)", n, err)
					}
					if n, err := r.writeHeader(); n != 0 || !errors.Is(err, wantErr) || c.calls != 1 {
						t.Fatalf("sticky finalization = (%d, %v), calls=%d", n, err, c.calls)
					}
				}
				r.reset()
				if r.err != nil || r.written != 0 || r.c != nil || r.headerBuffer.Len() != 0 {
					t.Fatalf("failed response retained after reset: %+v", r)
				}
			})
		}
	}
}

func TestResponseWriterRejectedFirstBody(t *testing.T) {
	c := new(responseConn)
	r := coalescingTestWriter(c)
	r.Header().Set("Content-Length", "4")
	if n, err := r.Write([]byte("oversize")); n != 0 || !errors.Is(err, http.ErrContentLength) {
		t.Fatalf("oversized Write = (%d, %v)", n, err)
	}
	if c.calls != 1 || !strings.HasSuffix(c.data.String(), "\r\n\r\n") {
		t.Fatal("rejected first body did not send only its committed header")
	}
	if n, err := r.Write([]byte("body")); n != 4 || err != nil {
		t.Fatalf("valid retry = (%d, %v)", n, err)
	}
	assertResponseBody(t, readResponse(t, &c.data, http.MethodGet), "body")
}

func TestTurboProcessWriteCounts(t *testing.T) {
	wantCalls := map[string]int{"SmallGET": 1, "ImplicitGET": 1, "LargeGET": 2, "POST": 1, "HEAD": 1, "Empty": 1, "StreamGET": 4}
	writer := makeResponseWriter()
	var parser request.Parser
	for _, workload := range http11Workloads() {
		t.Run(workload.name, func(t *testing.T) {
			conn := newTurboTestConn(string(workload.request()))
			server := Turbo{h: workload.handler()}
			if err := server.process(conn, &writer, &parser); err != nil {
				t.Fatal(err)
			}
			if conn.calls != wantCalls[workload.name] {
				t.Fatalf("underlying writes = %d, want %d", conn.calls, wantCalls[workload.name])
			}
			res := readResponse(t, &conn.data, workload.method)
			assertResponseBody(t, res, string(workload.expectedBody()))
			if conn.data.Len() != 0 || conn.closeCount.Load() != 1 {
				t.Fatal("extra bytes or incorrect connection cleanup")
			}
		})
	}
}

func TestTurboFirstBodyStreaming(t *testing.T) {
	for _, transport := range []string{"Pipe", "TCP"} {
		t.Run(transport, func(t *testing.T) {
			release := make(chan struct{})
			resume := sync.OnceFunc(func() { close(release) })
			wrote := make(chan error, 2)
			dial := startHTTP11BenchmarkServer(t, transport, "Turbo", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", "6")
				_, err := w.Write([]byte("one"))
				wrote <- err
				<-release
				_, err = w.Write([]byte("two"))
				wrote <- err
			})
			t.Cleanup(resume)
			conn, err := dial()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.Close() })
			if transport == "TCP" {
				if err := conn.SetDeadline(time.Now().Add(http11BenchmarkTimeout)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: test.local\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			res := readResponse(t, conn, http.MethodGet)
			first := make([]byte, 3)
			if _, err := io.ReadFull(res.Body, first); err != nil || string(first) != "one" {
				t.Fatalf("first chunk before handler return = %q, %v", first, err)
			}
			select {
			case err := <-wrote:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(http11BenchmarkTimeout):
				t.Fatal("first Write did not return before handler release")
			}
			resume()
			assertResponseBody(t, res, "two")
			select {
			case err := <-wrote:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(http11BenchmarkTimeout):
				t.Fatal("second Write did not complete")
			}
		})
	}
}
