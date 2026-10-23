package turbo

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestResponseWriterHeader(t *testing.T) {
	r := makeResponseWriter()
	h := r.Header()
	h.Set("X-Test", "value")
	if got := r.Header().Get("X-Test"); got != "value" {
		t.Fatalf("Header did not preserve changes: %q", got)
	}
}

func BenchmarkResponseWriter(b *testing.B) {
	for _, informational := range []bool{false, true} {
		name := "final"
		if informational {
			name = "informational"
		}
		b.Run(name, func(b *testing.B) {
			c := new(responseConn)
			body := []byte("hello")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.data.Reset()
				r := makeResponseWriter()
				r.c = c
				r.Header().Set("X-Test", "value")
				if informational {
					r.WriteHeader(http.StatusEarlyHints)
				}
				r.WriteHeader(http.StatusCreated)
				if _, err := r.Write(body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestResponseWriterWrite(t *testing.T) {
	c := new(responseConn)
	r := makeResponseWriter()
	r.c = c
	r.Header().Add("Set-Cookie", "a=1")
	r.Header().Add("Set-Cookie", "b=2")
	for _, body := range []string{"hello", " world"} {
		if n, err := r.Write([]byte(body)); n != len(body) || err != nil {
			t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(body))
		}
	}
	res := readResponse(t, &c.data, http.MethodGet)
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := res.Header.Values("Set-Cookie"); !reflect.DeepEqual(got, []string{"a=1", "b=2"}) {
		t.Errorf("Set-Cookie = %q", got)
	}
	if !res.Close {
		t.Error("response is not delimited by connection close")
	}
	if _, err := http.ParseTime(res.Header.Get("Date")); err != nil {
		t.Errorf("invalid Date: %v", err)
	}
	assertResponseBody(t, res, "hello world")
}

func TestResponseWriterWriteHeader(t *testing.T) {
	c := new(responseConn)
	r := makeResponseWriter()
	r.c = c
	r.Header().Set("X-Test", "before")
	r.Header().Set("Content-Type", "application/custom")
	r.WriteHeader(http.StatusCreated)
	r.Header()["X-Test"][0] = "after"
	r.WriteHeader(http.StatusInternalServerError)
	if _, err := r.Write([]byte("created")); err != nil {
		t.Fatal(err)
	}
	res := readResponse(t, &c.data, http.MethodGet)
	if res.Status != "201 Created" || res.Header.Get("X-Test") != "before" {
		t.Fatalf("unexpected response: %#v", res)
	}
	if got := res.Header.Get("Content-Type"); got != "application/custom" {
		t.Errorf("Content-Type = %q", got)
	}
	assertResponseBody(t, res, "created")
}

func TestResponseWriterDetectContentTypeAfterWriteHeader(t *testing.T) {
	c := new(responseConn)
	r := makeResponseWriter()
	r.c = c
	r.WriteHeader(http.StatusCreated)
	r.Header().Set("Content-Type", "ignored/after-commit")
	if _, err := r.Write([]byte("<html><body>created</body></html>")); err != nil {
		t.Fatal(err)
	}
	res := readResponse(t, &c.data, http.MethodGet)
	if got := res.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestResponseWriterInformationalHeaders(t *testing.T) {
	c := new(responseConn)
	r := makeResponseWriter()
	r.c = c
	r.Header().Set("Link", "</style.css>; rel=preload")
	r.Header().Set("Content-Length", "4")
	r.Header().Set("Transfer-Encoding", "chunked")
	r.Header().Set("Trailer", "X-Checksum")
	r.WriteHeader(http.StatusEarlyHints)
	r.WriteHeader(http.StatusProcessing)
	if r.Header().Get("Content-Length") != "4" || r.Header().Get("Transfer-Encoding") != "chunked" || r.Header().Get("Trailer") != "X-Checksum" {
		t.Fatalf("informational response changed handler fields: %v", r.Header())
	}
	r.Header().Set("X-Final", "yes")
	if _, err := r.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	in := bufio.NewReader(&c.data)
	for _, code := range []int{http.StatusEarlyHints, http.StatusProcessing, http.StatusOK} {
		res, err := http.ReadResponse(in, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != code || res.Header.Get("Link") == "" {
			t.Fatalf("unexpected response: %#v", res)
		}
		if code < 200 {
			if res.Header.Get("Content-Length") != "" || res.Header.Get("X-Final") != "" || res.Close || len(res.TransferEncoding) != 0 || len(res.Trailer) != 0 {
				t.Fatalf("unexpected informational fields: %v", res.Header)
			}
		} else {
			if res.Header.Get("X-Final") != "yes" {
				t.Fatal("missing final header")
			}
			assertResponseBody(t, res, "body")
		}
	}
}

func TestResponseWriterBodyNotAllowed(t *testing.T) {
	for _, code := range []int{http.StatusSwitchingProtocols, http.StatusNoContent, http.StatusNotModified} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			c := new(responseConn)
			r := makeResponseWriter()
			r.c = c
			r.WriteHeader(code)
			if n, err := r.Write([]byte("body")); n != 0 || !errors.Is(err, http.ErrBodyNotAllowed) {
				t.Fatalf("Write = (%d, %v), want (0, ErrBodyNotAllowed)", n, err)
			}
			res := readResponse(t, &c.data, http.MethodGet)
			assertResponseBody(t, res, "")
			if c.data.Len() != 0 {
				t.Fatal("unexpected body bytes")
			}
		})
	}
}

func TestResponseWriterContentLength(t *testing.T) {
	c := new(responseConn)
	r := makeResponseWriter()
	r.c = c
	r.Header().Set("Content-Length", "4")
	if n, err := r.Write([]byte("bo")); n != 2 || err != nil {
		t.Fatalf("Write = (%d, %v)", n, err)
	}
	r.Header().Set("Content-Length", "100")
	if n, err := r.Write([]byte("too much")); n != 0 || !errors.Is(err, http.ErrContentLength) {
		t.Fatalf("Write = (%d, %v), want (0, ErrContentLength)", n, err)
	}
	if n, err := r.Write([]byte("dy")); n != 2 || err != nil {
		t.Fatalf("Write = (%d, %v)", n, err)
	}
	res := readResponse(t, &c.data, http.MethodGet)
	if res.ContentLength != 4 {
		t.Errorf("Content-Length = %d", res.ContentLength)
	}
	assertResponseBody(t, res, "body")
}

func TestResponseWriterInvalidContentLength(t *testing.T) {
	for _, values := range [][]string{{"invalid"}, {"-1"}, {"1", "2"}} {
		c := new(responseConn)
		r := makeResponseWriter()
		r.c = c
		r.Header()["Content-Length"] = values
		if n, err := r.Write([]byte("body")); n != 0 || err == nil {
			t.Fatalf("Content-Length %q: Write = (%d, %v), want (0, error)", values, n, err)
		}
		if c.calls != 0 {
			t.Fatal("invalid header reached connection")
		}
	}
}

func TestResponseWriterCanonicalContentLength(t *testing.T) {
	c := new(responseConn)
	r := makeResponseWriter()
	r.c = c
	r.Header().Set("Content-Length", "+4")
	if _, err := r.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	res := readResponse(t, &c.data, http.MethodGet)
	if res.Header.Get("Content-Length") != "4" {
		t.Errorf("Content-Length = %q", res.Header.Get("Content-Length"))
	}
	assertResponseBody(t, res, "body")
}

func TestResponseWriterInvalidStatus(t *testing.T) {
	for _, code := range []int{0, 99, 1000} {
		c := new(responseConn)
		r := makeResponseWriter()
		r.c = c
		r.WriteHeader(code)
		if n, err := r.Write([]byte("body")); n != 4 || err != nil {
			t.Fatalf("Write after invalid status %d = (%d, %v)", code, n, err)
		}
		res := readResponse(t, &c.data, http.MethodGet)
		if res.StatusCode != http.StatusOK {
			t.Errorf("status after invalid code %d = %d", code, res.StatusCode)
		}
		assertResponseBody(t, res, "body")
	}
}

func TestResponseWriterEmptyWrite(t *testing.T) {
	c := new(responseConn)
	r := makeResponseWriter()
	r.c = c
	if n, err := r.Write(nil); n != 0 || err != nil {
		t.Fatalf("Write(nil) = (%d, %v)", n, err)
	}
	if _, err := r.writeHeader(); err != nil {
		t.Fatal(err)
	}
	res := readResponse(t, &c.data, http.MethodGet)
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d", res.StatusCode)
	}
	assertResponseBody(t, res, "")
}

func TestResponseWriterSuppressAutomaticHeaders(t *testing.T) {
	c := new(responseConn)
	r := makeResponseWriter()
	r.c = c
	r.Header()["Content-Type"] = nil
	r.Header()["Date"] = nil
	if _, err := r.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	res := readResponse(t, &c.data, http.MethodGet)
	if res.Header.Get("Content-Type") != "" || res.Header.Get("Date") != "" {
		t.Fatalf("unexpected automatic headers: %v", res.Header)
	}
}

func TestResponseWriterConnectionErrors(t *testing.T) {
	sentinel := errors.New("write failed")
	tests := []struct {
		name      string
		failAt, n int
		err       error
		wantN     int
		wantErr   error
	}{
		{"header error", 1, 5, sentinel, 0, sentinel},
		{"short header", 1, 5, nil, 0, io.ErrShortWrite},
		{"body error", 2, 2, sentinel, 2, sentinel},
		{"short body", 2, 2, nil, 2, io.ErrShortWrite},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &responseConn{failAt: tt.failAt, n: tt.n, err: tt.err}
			r := makeResponseWriter()
			r.c = c
			if n, err := r.Write([]byte("body")); n != tt.wantN || !errors.Is(err, tt.wantErr) {
				t.Fatalf("Write = (%d, %v), want (%d, %v)", n, err, tt.wantN, tt.wantErr)
			}
			calls := c.calls
			if n, err := r.Write([]byte("again")); n != 0 || !errors.Is(err, tt.wantErr) {
				t.Fatalf("second Write = (%d, %v)", n, err)
			}
			if c.calls != calls {
				t.Fatal("retried a failed connection")
			}
		})
	}
	c := &responseConn{failAt: 1, err: sentinel}
	r := makeResponseWriter()
	r.c = c
	r.WriteHeader(http.StatusCreated)
	if n, err := r.Write([]byte("body")); n != 0 || !errors.Is(err, sentinel) {
		t.Fatalf("Write after WriteHeader failure = (%d, %v)", n, err)
	}
}

func TestProcessConnResponse(t *testing.T) {
	tests := []struct {
		name, method, body string
		status             int
		handler            http.HandlerFunc
	}{
		{"empty handler", http.MethodGet, "", http.StatusOK, func(http.ResponseWriter, *http.Request) {}},
		{"headers only", http.MethodGet, "", http.StatusAccepted, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}},
		{"body", http.MethodGet, "hello", http.StatusCreated, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("hello"))
		}},
		{"head", http.MethodHead, "", http.StatusOK, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("hello"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			turbo := Turbo{h: tt.handler}
			done := make(chan error, 1)
			go func() {
				err := turbo.process(server)
				server.Close()
				done <- err
			}()
			if _, err := io.WriteString(client, tt.method+" / HTTP/1.1\r\nHost: example.com\r\n\r\n"); err != nil {
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
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func readResponse(t *testing.T, in io.Reader, method string) *http.Response {
	t.Helper()
	r, ok := in.(*bufio.Reader)
	if !ok {
		r = bufio.NewReader(in)
	}
	res, err := http.ReadResponse(r, &http.Request{Method: method})
	if err != nil {
		t.Fatalf("invalid response: %v", err)
	}
	return res
}

func assertResponseBody(t *testing.T, res *http.Response, want string) {
	t.Helper()
	defer res.Body.Close()
	got, err := io.ReadAll(res.Body)
	if err != nil || string(got) != want {
		t.Fatalf("body = %q, error = %v, want %q", got, err, want)
	}
}

type responseConn struct {
	data             bytes.Buffer
	calls, failAt, n int
	err              error
}

func (c *responseConn) Read([]byte) (int, error) { return 0, io.EOF }
func (c *responseConn) Write(p []byte) (int, error) {
	c.calls++
	if c.calls == c.failAt {
		c.data.Write(p[:c.n])
		return c.n, c.err
	}
	return c.data.Write(p)
}
func (*responseConn) Close() error                     { return nil }
func (*responseConn) LocalAddr() net.Addr              { return nil }
func (*responseConn) RemoteAddr() net.Addr             { return nil }
func (*responseConn) SetDeadline(time.Time) error      { return nil }
func (*responseConn) SetReadDeadline(time.Time) error  { return nil }
func (*responseConn) SetWriteDeadline(time.Time) error { return nil }

var _ net.Conn = (*responseConn)(nil)
