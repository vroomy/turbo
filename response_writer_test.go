package turbo

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
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

func TestResponseWriterDefaultHeaderValues(t *testing.T) {
	for _, key := range []string{"Connection", "Date"} {
		t.Run(key, func(t *testing.T) {
			r := makeResponseWriter()
			before := r.Header().Clone()
			r.Header().Add(key, "extra")
			for other, values := range before {
				if other != key && !reflect.DeepEqual(r.Header()[other], values) {
					t.Errorf("appending %s changed %s: %q", key, other, r.Header()[other])
				}
			}
		})
	}
}

func TestResponseWriterHeaderReuse(t *testing.T) {
	r := makeResponseWriter()
	r.c = new(responseConn)
	retained := r.Header()
	retained.Set("X-Request", "first")
	if _, err := r.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	r.reset()
	r.populateHeaders()
	if retained.Get("X-Request") != "" {
		t.Fatal("previous response's headers were not cleared in place")
	}
	if r.Header().Get("X-Request") != "" || r.Header().Get("Connection") != "close" {
		t.Fatalf("unexpected headers after reuse: %v", r.Header())
	}
	if _, err := http.ParseTime(r.Header().Get("Date")); err != nil {
		t.Fatalf("invalid Date after reuse: %v", err)
	}
	retained.Set("X-Request", "second")
	if r.Header().Get("X-Request") != "second" {
		t.Fatal("response writer replaced its header map")
	}
}

func TestResponseWriterPopulateHeaders(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		deleted bool
	}{
		{name: "deleted defaults", deleted: true},
		{name: "nil defaults", values: nil},
		{name: "empty defaults", values: []string{}},
		{name: "replaced defaults", values: []string{"changed"}},
		{name: "multiple defaults", values: []string{"changed", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := makeResponseWriter()
			headers := r.Header()
			headers.Set("Content-Length", "100")
			headers.Set("Set-Cookie", "previous=request")
			if tt.deleted {
				headers.Del("Connection")
				headers.Del("Date")
			} else {
				// Handlers can replace both entries with the same backing array.
				headers["Connection"] = tt.values
				headers["Date"] = tt.values
			}
			r.reset()
			r.populateHeaders()
			if len(headers) != 2 {
				t.Fatalf("headers = %v, want only Connection and Date", headers)
			}
			if !reflect.DeepEqual(headers.Values("Connection"), []string{"close"}) {
				t.Errorf("Connection = %q, want [close]", headers.Values("Connection"))
			}
			if len(headers.Values("Date")) != 1 {
				t.Fatalf("Date = %q, want exactly one value", headers.Values("Date"))
			}
			date := headers.Get("Date")
			if _, err := http.ParseTime(date); err != nil {
				t.Fatal(err)
			}
			headers["Connection"][0] = "changed"
			if headers.Get("Date") != date {
				t.Error("Connection and Date share a value")
			}
		})
	}
}

func TestResponseWriterPopulateHeader(t *testing.T) {
	tests := []struct {
		name   string
		values []string
	}{
		{name: "matching value", values: []string{"expected"}},
		{name: "changed value", values: []string{"changed"}},
		{name: "missing value"},
		{name: "empty value", values: []string{}},
		{name: "extra value", values: []string{"expected", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := makeResponseWriter()
			r.Header()["Connection"] = tt.values
			r.populateHeader("Connection", "expected")
			got := r.Header().Values("Connection")
			if !reflect.DeepEqual(got, []string{"expected"}) {
				t.Fatalf("Connection = %q, want [expected]", got)
			}
			if len(tt.values) == 1 && tt.values[0] == "expected" && &got[0] != &tt.values[0] {
				t.Error("matching value was replaced")
			}
		})
	}
}

func TestResponseWriterStatusLine(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{http.StatusOK, "200 OK"},
		{http.StatusInternalServerError, "500 Internal Server Error"},
		{299, "299 status code 299"},
		{999, "999 status code 999"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			c := new(responseConn)
			r := makeResponseWriter()
			r.c = c
			r.WriteHeader(tt.code)
			if _, err := r.writeHeader(); err != nil {
				t.Fatal(err)
			}
			res := readResponse(t, &c.data, http.MethodHead)
			res.Body.Close()
			if res.Status != tt.want || res.Proto != "HTTP/1.1" {
				t.Fatalf("status line = %s %s, want HTTP/1.1 %s", res.Proto, res.Status, tt.want)
			}
		})
	}
}

func TestResponseWriterHeaderSerialization(t *testing.T) {
	for _, size := range []int{128, 32 << 10} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			c := new(responseConn)
			r := makeResponseWriter()
			r.c = c
			value := strings.Repeat("x", size)
			r.Header().Set("X-Large", value)
			r.Header().Set("X-Newline", "before\r\nafter")
			r.Header()["Invalid Header"] = []string{"ignored"}
			r.WriteHeader(http.StatusOK)
			r.Header().Set("X-Large", "changed after commit")
			if _, err := r.Write([]byte("body")); err != nil {
				t.Fatal(err)
			}
			res := readResponse(t, &c.data, http.MethodGet)
			if got := res.Header.Get("X-Large"); got != value {
				t.Errorf("X-Large = %d bytes, want %d committed bytes", len(got), size)
			}
			if got := res.Header.Get("X-Newline"); got != "before  after" {
				t.Errorf("X-Newline = %q", got)
			}
			assertResponseBody(t, res, "body")
		})
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
			r := makeResponseWriter()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.data.Reset()
				r.populateHeaders()
				r.c = c
				r.Header().Set("X-Test", "value")
				if informational {
					r.WriteHeader(http.StatusEarlyHints)
				}
				r.WriteHeader(http.StatusCreated)
				if _, err := r.Write(body); err != nil {
					b.Fatal(err)
				}
				r.reset()
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
