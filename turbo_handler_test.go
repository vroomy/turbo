package turbo

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestNewWithHandler(t *testing.T) {
	server, err := NewWithHandler(0, 4, 8, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Length", "6")
		w.Header().Set("X-Path", req.URL.Path)
		_, _ = w.Write([]byte("hello!"))
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTurbo(t, server) })
	for i := range 8 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			conn, err := net.DialTimeout("tcp", server.l.Addr().String(), 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/%d", i)
			if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: test.local\r\nConnection: keep-alive\r\n\r\n", path); err != nil {
				t.Fatal(err)
			}
			in := bufio.NewReader(conn)
			res := readResponse(t, in, http.MethodGet)
			if res.Header.Get("X-Path") != path || !res.Close {
				t.Fatalf("unexpected response: %v", res.Header)
			}
			assertResponseBody(t, res, "hello!")
			if _, err := in.ReadByte(); err != io.EOF {
				t.Fatalf("expected connection EOF, got %v", err)
			}
		})
	}
}

func TestNewWithHandlerInvalidOptions(t *testing.T) {
	for _, tt := range []struct {
		name           string
		workers, queue int
		handler        http.HandlerFunc
	}{
		{name: "nil handler", workers: 1},
		{name: "zero workers", handler: func(http.ResponseWriter, *http.Request) {}},
		{name: "negative workers", workers: -1, handler: func(http.ResponseWriter, *http.Request) {}},
		{name: "negative queue", workers: 1, queue: -1, handler: func(http.ResponseWriter, *http.Request) {}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if server, err := NewWithHandler(0, tt.workers, tt.queue, tt.handler); server != nil || err == nil {
				if server != nil {
					server.Close()
				}
				t.Fatalf("NewWithHandler = (%v, %v), want (nil, error)", server, err)
			}
		})
	}
}
