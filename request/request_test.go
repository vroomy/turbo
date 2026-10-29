package request

import (
	"bufio"
	"context"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func TestParse(t *testing.T) {
	tests := []struct{ name, wire string }{
		{"get", "GET /bench HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"},
		{"no headers", "GET / HTTP/1.1\r\n\r\n"},
		{"head", "HEAD / HTTP/1.1\r\nHost: example.com\r\n\r\n"},
		{"custom method", "M-SEARCH / HTTP/1.1\r\nHost: example.com\r\n\r\n"},
		{"lowercase headers", "GET / HTTP/1.1\r\nhost: example.com\r\nx-custom: hello\r\nconnection: CLOSE\r\n\r\n"},
		{"mixed common headers", "HEAD / HTTP/1.1\r\nhOsT: example.com\r\ncOnNeCtIoN: ClOsE\r\ncOnTeNt-LeNgTh: 0\r\n\r\n"},
		{"lowercase common headers", "GET / HTTP/1.1\r\nhost: example.com\r\nconnection: close\r\ncontent-length: 0\r\n\r\n"},
		{"host last", "GET / HTTP/1.1\r\nConnection: close\r\nX-Test: value\r\nHost: example.com\r\n\r\n"},
		{"header whitespace", "GET / HTTP/1.1\r\nHost:\texample.com \t\r\nX-Empty:\t \r\nX-Tab: a\tb\r\n\r\n"},
		{"connection tokens", "GET / HTTP/1.1\r\nConnection: keep-alive, \tClOsE, upgrade\r\n\r\n"},
		{"connection substring", "GET / HTTP/1.1\r\nConnection: disclose\r\n\r\n"},
		{"query", "GET /a/b?q=a%20b&x=1 HTTP/1.1\r\n\r\n"},
		{"empty query", "GET /? HTTP/1.1\r\n\r\n"},
		{"query punctuation", "GET /?a=?#%zz HTTP/1.1\r\n\r\n"},
		{"double slash path", "GET //example.com/a HTTP/1.1\r\nHost: other.com\r\n\r\n"},
		{"escaped path", "GET /a%2fb%20c HTTP/1.1\r\n\r\n"},
		{"invalid escape", "GET /%zz HTTP/1.1\r\n\r\n"},
		{"unicode path", "GET /café HTTP/1.1\r\n\r\n"},
		{"absolute uri", "GET http://example.com/a?b HTTP/1.1\r\nHost: other.com\r\n\r\n"},
		{"connect", "CONNECT example.com:443 HTTP/1.1\r\nHost: other.com\r\n\r\n"},
		{"connect path", "CONNECT /rpc HTTP/1.1\r\n\r\n"},
		{"options star", "OPTIONS * HTTP/1.1\r\n\r\n"},
		{"http 1.0", "GET / HTTP/1.0\r\n\r\n"},
		{"http 1.0 keep alive", "GET / HTTP/1.0\r\nConnection: keep-alive\r\n\r\n"},
		{"http 1.2", "GET / HTTP/1.2\r\n\r\n"},
		{"post", "POST / HTTP/1.1\r\nContent-Length: 4\r\n\r\nbody"},
		{"get body", "GET / HTTP/1.1\r\nContent-Length: 4\r\n\r\nbody"},
		{"truncated body", "POST / HTTP/1.1\r\nContent-Length: 4\r\n\r\nbo"},
		{"leading zero length", "POST / HTTP/1.1\r\nContent-Length: 00\r\n\r\n"},
		{"duplicate length", "POST / HTTP/1.1\r\nContent-Length: 4\r\ncontent-length: 4\r\n\r\nbody"},
		{"conflicting length", "POST / HTTP/1.1\r\nContent-Length: 4\r\nContent-Length: 5\r\n\r\nbody"},
		{"negative length", "POST / HTTP/1.1\r\nContent-Length: -1\r\n\r\n"},
		{"signed length", "POST / HTTP/1.1\r\nContent-Length: +4\r\n\r\nbody"},
		{"empty length", "POST / HTTP/1.1\r\nContent-Length:\r\n\r\n"},
		{"length overflow", "POST / HTTP/1.1\r\nContent-Length: 99999999999999999999\r\n\r\n"},
		{"chunked", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nbody\r\n0\r\n\r\n"},
		{"chunked trailer", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\nTrailer: X-Sum\r\n\r\n4\r\nbody\r\n0\r\nX-Sum: ok\r\n\r\n"},
		{"undeclared chunked trailer", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nbody\r\n0\r\nX-Sum: ok\r\n\r\n"},
		{"length and encoding", "POST / HTTP/1.1\r\nContent-Length: 0\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n"},
		{"unsupported encoding", "POST / HTTP/1.1\r\nTransfer-Encoding: gzip\r\n\r\n"},
		{"duplicate encoding", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n"},
		{"nonchunked trailer", "GET / HTTP/1.1\r\nTrailer: X-Sum\r\n\r\n"},
		{"h2 upgrade", "GET / HTTP/1.1\r\nConnection: Upgrade, HTTP2-Settings\r\nUpgrade: h2c\r\nHTTP2-Settings: AAMAAABk\r\n\r\n"},
		{"pragma", "GET / HTTP/1.1\r\nPragma: no-cache\r\n\r\n"},
		{"duplicate fields", "GET / HTTP/1.1\r\nX-Test: one\r\nX-Test: two\r\n\r\n"},
		{"duplicate host", "GET / HTTP/1.1\r\nHost: a\r\nhost: a\r\n\r\n"},
		{"empty duplicate host", "GET / HTTP/1.1\r\nHost:\r\nHost: a\r\n\r\n"},
		{"late duplicate host", "GET / HTTP/1.1\r\nHost: a\r\nX-Test: value\r\nhOsT: a\r\n\r\n"},
		{"get duplicate zero length", "GET / HTTP/1.1\r\nX-Test: value\r\nContent-Length: 0\r\ncOnTeNt-LeNgTh: 0\r\n\r\n"},
		{"get conflicting length", "GET / HTTP/1.1\r\nHost: a\r\nX-Test: value\r\nContent-Length: 0\r\ncontent-length: 4\r\n\r\nbody"},
		{"late body fallback", "GET / HTTP/1.1\r\nHost: a\r\nX-Test: value\r\nContent-Length: 4\r\n\r\nbody"},
		{"late chunked fallback", "GET / HTTP/1.1\r\nHost: a\r\nX-Test: value\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nbody\r\n0\r\n\r\n"},
		{"late duplicate connection", "GET / HTTP/1.1\r\nHost: a\r\nX-Test: value\r\nConnection: keep-alive\r\ncOnNeCtIoN: close\r\n\r\n"},
		{"late mixed duplicate field", "GET / HTTP/1.1\r\nHost: a\r\nX-Test: first\r\nx-tEsT: second\r\n\r\n"},
		{"folded field", "GET / HTTP/1.1\r\nX-Test: one\r\n two\r\n\r\n"},
		{"lf lines", "GET / HTTP/1.1\nHost: example.com\n\n"},
		{"whitespace before colon", "GET / HTTP/1.1\r\nHost : example.com\r\n\r\n"},
		{"invalid field", "GET / HTTP/1.1\r\nBad(Name: value\r\n\r\n"},
		{"control value", "GET / HTTP/1.1\r\nX-Test: \x00\r\n\r\n"},
		{"bare lf value", "GET / HTTP/1.1\r\nX-Test: a\nb\r\n\r\n"},
		{"del value", "GET / HTTP/1.1\r\nX-Test: \x7f\r\n\r\n"},
		{"unicode value", "GET / HTTP/1.1\r\nX-Test: café\r\n\r\n"},
		{"empty input", ""},
		{"invalid method", "G(ET / HTTP/1.1\r\n\r\n"},
		{"invalid version", "GET / HTTP/1.x\r\n\r\n"},
		{"extra request space", "GET  / HTTP/1.1\r\n\r\n"},
		{"truncated headers", "GET / HTTP/1.1\r\nHost: example.com\r\n"},
		{"large header", "GET / HTTP/1.1\r\nX-Large: " + strings.Repeat("x", 8192) + "\r\n\r\n"},
	}
	var parser Parser
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, mode := range []string{"buffered", "unbuffered", "fragmented", "small buffer"} {
				t.Run(mode, func(t *testing.T) {
					compareParse(t, requestReader(tt.wire, mode), tt.wire)
					got, err := parser.Parse(requestReader(tt.wire, mode))
					compareRequest(t, tt.wire, got, err)
				})
			}
		})
	}
}

func requestReader(wire, mode string) io.Reader {
	var reader io.Reader = strings.NewReader(wire)
	switch mode {
	case "buffered":
		return bufio.NewReader(reader)
	case "fragmented":
		return iotest.OneByteReader(reader)
	case "small buffer":
		return bufio.NewReaderSize(reader, 16)
	default:
		return reader
	}
}

func compareParse(t testing.TB, reader io.Reader, wire string) {
	t.Helper()
	got, gotErr := Parse(reader)
	compareRequest(t, wire, got, gotErr)
}

func compareRequest(t testing.TB, wire string, got *http.Request, gotErr error) {
	t.Helper()
	want, wantErr := http.ReadRequest(bufio.NewReader(strings.NewReader(wire)))
	if errorText(gotErr) != errorText(wantErr) {
		t.Fatalf("parse error = %v, stdlib = %v; input %q", gotErr, wantErr, wire)
	}
	if wantErr != nil {
		return
	}
	wantBody, wantReadErr := io.ReadAll(want.Body)
	gotBody, gotReadErr := io.ReadAll(got.Body)
	wantCloseErr, gotCloseErr := want.Body.Close(), got.Body.Close()
	if string(gotBody) != string(wantBody) || errorText(gotReadErr) != errorText(wantReadErr) || errorText(gotCloseErr) != errorText(wantCloseErr) {
		t.Fatalf("body = (%q, %v, %v), stdlib = (%q, %v, %v)", gotBody, gotReadErr, gotCloseErr, wantBody, wantReadErr, wantCloseErr)
	}
	want.Body, got.Body = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request = %+v, URL = %+v; stdlib = %+v, URL = %+v; input %q", got, got.URL, want, want.URL, wire)
	}
}

func TestParserReuse(t *testing.T) {
	const first = "GET /first?q=one HTTP/1.1\r\nHost: first.local\r\nX-One: one\r\nX-Two: two\r\n\r\n"
	const second = "HEAD /second HTTP/1.1\r\nHost: second.local\r\nX-One: new\r\n\r\n"
	var parser Parser
	req, err := parser.Parse(requestReader(first, "buffered"))
	if err != nil {
		t.Fatal(err)
	}
	clone := req.Clone(req.Context())
	previousURL, previousHeaders := req.URL, req.Header
	previousValues := &parser.values[0]
	req.Header.Add("X-One", "handler")
	if req.Header.Get("X-Two") != "two" {
		t.Fatal("appending one header overwrote another")
	}
	// Handlers can replace fields. Reset must use the parser's own storage.
	req.Header = http.Header{"X-Replaced": {"handler"}}
	req.URL = &url.URL{Path: "/replaced"}
	req.Form = url.Values{"field": {"handler"}}
	req.Trailer = http.Header{"X-Trailer": {"handler"}}
	req.RemoteAddr, req.RequestURI = "handler", "/handler"
	type contextKey struct{}
	*req = *req.WithContext(context.WithValue(req.Context(), contextKey{}, "handler"))
	parser.Reset()
	if !reflect.DeepEqual(parser.req, http.Request{}) || !reflect.DeepEqual(parser.url, url.URL{}) || len(parser.headers) != 0 || len(parser.values) != 0 {
		t.Fatal("Reset retained request fields")
	}
	for _, value := range parser.values[:cap(parser.values)] {
		if value != "" {
			t.Fatal("Reset retained a header value")
		}
	}
	next, err := parser.Parse(requestReader(second, "buffered"))
	if err != nil {
		t.Fatal(err)
	}
	if next != req || next.URL != previousURL || &parser.values[0] != previousValues {
		t.Fatal("request, URL, or header value storage was replaced")
	}
	previousHeaders.Set("X-Shared", "same map")
	if next.Header.Get("X-Shared") != "same map" {
		t.Fatal("header map was replaced")
	}
	if next.Method != "HEAD" || next.Host != "second.local" || next.Header.Get("X-Two") != "" || next.Form != nil || next.Trailer != nil || next.Context().Value(contextKey{}) != nil {
		t.Fatalf("next request retained handler data: %+v", next)
	}
	if clone.RequestURI != "/first?q=one" || clone.URL.Path != "/first" || clone.Host != "first.local" || !reflect.DeepEqual(clone.Header["X-One"], []string{"one"}) || clone.Header.Get("X-Two") != "two" {
		t.Fatalf("reuse changed cloned metadata: %+v", clone)
	}
}

func TestParserRetainedCapacity(t *testing.T) {
	for _, count := range []int{2, maxRetainedHeaders + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var wire strings.Builder
			wire.WriteString("GET / HTTP/1.1\r\nHost: example.com\r\n")
			for i := range count {
				fmt.Fprintf(&wire, "X-%d: value\r\n", i)
			}
			wire.WriteString("\r\n")
			var parser Parser
			if _, err := parser.Parse(requestReader(wire.String(), "buffered")); err != nil {
				t.Fatal(err)
			}
			parser.Reset()
			wantRetained := count <= maxRetainedHeaders
			if (parser.headers != nil) != wantRetained || (cap(parser.values) > 0) != wantRetained {
				t.Fatalf("retained headers = %v, value capacity = %d, want retained = %v", parser.headers != nil, cap(parser.values), wantRetained)
			}
		})
	}
}

func TestOwnHeaderStorage(t *testing.T) {
	var parser Parser
	var retained, expected []string
	var rollovers int
	for i := range 100 {
		target := []byte(fmt.Sprintf("/request/%03d", i))
		fields := []byte(fmt.Sprintf("Host: host-%03d.local\r\nX-Request: %03d\r\n", i, i))
		want := string(target) + string(fields)
		previousLen := parser.headerText.Len()
		got := ownHeader(target, fields, &parser)
		if got != want {
			t.Fatalf("header = %q, want %q", got, want)
		}
		if parser.headerText.Len() < previousLen {
			rollovers++
		}
		if parser.headerText.Cap() > headerTextBlockSize {
			t.Fatalf("retained header text capacity = %d", parser.headerText.Cap())
		}
		// Readers reuse their buffers, and Reset must preserve retained strings.
		for i := range target {
			target[i] = 'x'
		}
		for i := range fields {
			fields[i] = 'x'
		}
		retained, expected = append(retained, got), append(expected, want)
		parser.Reset()
	}
	if rollovers == 0 {
		t.Fatal("test did not exhaust a shared block")
	}
	for i := range retained {
		if retained[i] != expected[i] {
			t.Fatalf("retained header %d = %q, want %q", i, retained[i], expected[i])
		}
	}
	before := parser.headerText.Len()
	large := []byte(strings.Repeat("L", maxSharedHeaderSize+1))
	got := ownHeader(nil, large, &parser)
	large[0] = 'x'
	if got[0] != 'L' || parser.headerText.Len() != before {
		t.Fatal("large header aliased its input or displaced the shared block")
	}
}

func TestParserCloneAcrossHeaderBlocks(t *testing.T) {
	var parser Parser
	var clones []*http.Request
	for i := range 100 {
		wire := fmt.Sprintf("GET /request/%03d?q=%03d HTTP/1.1\r\nHost: host-%03d.local\r\nX-Request: %03d\r\n\r\n", i, i, i, i)
		req, err := parser.Parse(requestReader(wire, "buffered"))
		if err != nil {
			t.Fatal(err)
		}
		clones = append(clones, req.Clone(req.Context()))
		parser.Reset()
	}
	for i, req := range clones {
		if req.URL.Path != fmt.Sprintf("/request/%03d", i) || req.URL.RawQuery != fmt.Sprintf("q=%03d", i) || req.Host != fmt.Sprintf("host-%03d.local", i) || req.Header.Get("X-Request") != fmt.Sprintf("%03d", i) {
			t.Fatalf("cloned request %d changed: %+v", i, req)
		}
	}
}

func TestParserConcurrentClone(t *testing.T) {
	var parser Parser
	req, err := parser.Parse(requestReader("GET /retained?q=one HTTP/1.1\r\nHost: first.local\r\nX-Request: first\r\n\r\n", "buffered"))
	if err != nil {
		t.Fatal(err)
	}
	clone := req.Clone(req.Context())
	started, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
				if clone.URL.Path != "/retained" || clone.URL.RawQuery != "q=one" || clone.Host != "first.local" || clone.Header.Get("X-Request") != "first" {
					t.Error("concurrent parsing changed cloned metadata")
					return
				}
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
	}()
	<-started
	for i := range 100 {
		wire := fmt.Sprintf("GET /next/%d HTTP/1.1\r\nHost: next.local\r\nX-Request: %d\r\n\r\n", i, i)
		if _, err := parser.Parse(requestReader(wire, "buffered")); err != nil {
			t.Fatal(err)
		}
		parser.Reset()
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestParseURLBytes(t *testing.T) {
	for c := 0; c < 256; c++ {
		t.Run(fmt.Sprintf("%02x", c), func(t *testing.T) {
			for _, target := range []string{"/a" + string(byte(c)) + "b", "/?a=" + string(byte(c))} {
				wire := "GET " + target + " HTTP/1.1\r\n\r\n"
				compareParse(t, bufio.NewReader(strings.NewReader(wire)), wire)
			}
		})
	}
}

func TestParseStorage(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("GET /first?q=one HTTP/1.1\r\nHost: first.local\r\nX-One: one\r\nX-Two: two\r\n\r\n"))
	req, err := Parse(reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Add("X-One", "extra")
	if got := req.Header.Get("X-Two"); got != "two" {
		t.Fatalf("appending a field overwrote another field: %q", got)
	}
	reader.Reset(strings.NewReader("HEAD /second HTTP/1.1\r\nHost: second.local\r\n\r\n"))
	if _, err := Parse(reader); err != nil {
		t.Fatal(err)
	}
	if req.Method != "GET" || req.RequestURI != "/first?q=one" || req.URL.Path != "/first" || req.URL.RawQuery != "q=one" || req.Host != "first.local" || req.Header.Get("X-Two") != "two" {
		t.Fatalf("reader reuse changed retained request: %+v", req)
	}
}

func TestParseBuffered(t *testing.T) {
	tests := []struct {
		name, wire string
		fast       bool
	}{
		{"get", "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n", true},
		{"head", "HEAD / HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n", true},
		{"no headers", "GET / HTTP/1.1\r\n\r\n", true},
		{"lowercase", "GET / HTTP/1.1\r\nhost: example.com\r\nconnection: close\r\n\r\n", true},
		{"body", "GET / HTTP/1.1\r\nContent-Length: 4\r\n\r\n", false},
		{"chunked", "GET / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n", false},
		{"post", "POST / HTTP/1.1\r\n\r\n", false},
		{"escaped path", "GET /a%2fb HTTP/1.1\r\n\r\n", true},
		{"duplicate host", "GET / HTTP/1.1\r\nHost:\r\nHost: example.com\r\n\r\n", false},
		{"folded header", "GET / HTTP/1.1\r\nX-Test: a\r\n b\r\n\r\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, ok := parseBuffered([]byte(tt.wire[:len(tt.wire)-2]), nil)
			if ok != tt.fast || (req != nil) != tt.fast {
				t.Fatalf("fast path = %v, request = %v, want %v", ok, req, tt.fast)
			}
		})
	}
}

func TestParseReadBoundaries(t *testing.T) {
	const wire = "GET /first?q=one HTTP/1.1\r\nHost: first.local\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"
	for split := 1; split < len(wire); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			reader := io.MultiReader(strings.NewReader(wire[:split]), strings.NewReader(wire[split:]))
			compareParse(t, bufio.NewReader(reader), wire)
		})
	}
}

func TestParseLeavesNextRequest(t *testing.T) {
	const first = "GET /first HTTP/1.1\r\nContent-Length: 0\r\n\r\n"
	const second = "POST /second HTTP/1.1\r\nContent-Length: 4\r\n\r\nbody"
	reader := bufio.NewReader(strings.NewReader(first + second))
	for _, wire := range []string{first, second} {
		compareParse(t, reader, wire)
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		t.Fatalf("after requests: %v, want EOF", err)
	}
}

func FuzzParse(f *testing.F) {
	for _, wire := range []string{
		"GET / HTTP/1.1\r\nHost: example.com\r\n\r\n",
		"GET /a?b HTTP/1.1\r\nConnection: close\r\nContent-Length: 0\r\n\r\n",
		"POST / HTTP/1.1\r\nContent-Length: 4\r\n\r\nbody",
		"POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n",
		"GET /a%2fb HTTP/1.0\nHost: example.com\n\n",
		"GET / HTTP/1.1\r\nhost: example.com\r\nconnection: close\r\ncontent-length: 0\r\n\r\n",
		"HEAD /a%2fb HTTP/1.1\r\nhOsT: example.com\r\ncOnNeCtIoN: close\r\ncOnTeNt-LeNgTh: 0\r\n\r\n",
		"GET / HTTP/1.1\r\n" + browserHeaders + "\r\n",
		"GET / HTTP/1.1\r\n" + mapHeaderNames(browserHeaders, strings.ToLower) + "\r\n",
		"GET / HTTP/1.1\r\n" + mapHeaderNames(browserHeaders, mixedHeaderCase) + "\r\n",
	} {
		f.Add(wire)
	}
	f.Fuzz(func(t *testing.T, wire string) {
		if len(wire) > 64<<10 {
			t.Skip()
		}
		compareParse(t, bufio.NewReader(strings.NewReader(wire)), wire)
	})
}

func FuzzParserReuse(f *testing.F) {
	const get = "GET /first?q=one HTTP/1.1\r\nHost: first.local\r\nX-Test: first\r\n\r\n"
	for _, wire := range []string{
		"HEAD /second HTTP/1.1\r\nHost: second.local\r\n\r\n",
		"GET /a%2fb HTTP/1.1\r\nHost: other.local\r\n\r\n",
		"GET / HTTP/1.1\r\nX-Test: one\r\nX-Test: two\r\n\r\n",
		"POST / HTTP/1.1\r\nContent-Length: 4\r\n\r\nbody",
		"POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n0\r\nX-Trailer: value\r\n\r\n",
		"GET /%zz HTTP/1.1\r\nX-Test: invalid\r\n\r\n",
		"GET / HTTP/1.1\r\n" + browserHeaders + "\r\n",
		"GET / HTTP/1.1\r\n" + mapHeaderNames(browserHeaders, strings.ToLower) + "\r\n",
		"GET / HTTP/1.1\r\n" + mapHeaderNames(browserHeaders, mixedHeaderCase) + "\r\n",
		"HEAD / HTTP/1.1\r\nhOsT: a\r\ncOnNeCtIoN: close\r\ncOnTeNt-LeNgTh: 0\r\n\r\n",
		"",
	} {
		f.Add(get, wire)
	}
	f.Fuzz(func(t *testing.T, first, second string) {
		if len(first)+len(second) > 64<<10 {
			t.Skip()
		}
		var parser Parser
		for _, wire := range []string{first, second, get} {
			req, err := parser.Parse(requestReader(wire, "buffered"))
			compareRequest(t, wire, req, err)
		}
		parser.Reset()
	})
}

func BenchmarkParse(b *testing.B) {
	for _, workload := range []struct{ name, wire string }{
		{"SmallGET", "GET /bench HTTP/1.1\r\nHost: benchmark.local\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"},
		{"LowercaseSmallGET", "GET /bench HTTP/1.1\r\nhost: benchmark.local\r\nconnection: close\r\ncontent-length: 0\r\n\r\n"},
		{"MixedCaseSmallGET", "GET /bench HTTP/1.1\r\nhOsT: benchmark.local\r\ncOnNeCtIoN: close\r\ncOnTeNt-LeNgTh: 0\r\n\r\n"},
		{"Headers", "GET /a?b=c HTTP/1.1\r\nHost: benchmark.local\r\nUser-Agent: benchmark/1.0\r\nAccept: */*\r\nAccept-Encoding: gzip\r\nConnection: close\r\nX-Request-Id: 12345\r\n\r\n"},
		{"BrowserHeaders", "GET /products/items?page=2 HTTP/1.1\r\n" + browserHeaders + "\r\n"},
		{"LowercaseHeaders", "GET /products/items?page=2 HTTP/1.1\r\n" + mapHeaderNames(browserHeaders, strings.ToLower) + "\r\n"},
		{"MixedCaseHeaders", "GET /products/items?page=2 HTTP/1.1\r\n" + mapHeaderNames(browserHeaders, mixedHeaderCase) + "\r\n"},
		{"EscapedHeaders", "HEAD /products/a%2fb%20c?page=2 HTTP/1.1\r\n" + browserHeaders + "\r\n"},
		{"POST", "POST /bench HTTP/1.1\r\nHost: benchmark.local\r\nConnection: close\r\nContent-Length: 1024\r\n\r\n" + strings.Repeat("x", 1024)},
		{"EscapedPath", "GET /a%2fb HTTP/1.1\r\nHost: benchmark.local\r\nConnection: close\r\n\r\n"},
		{"Chunked", "POST /bench HTTP/1.1\r\nHost: benchmark.local\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nbody\r\n0\r\n\r\n"},
	} {
		b.Run(workload.name, func(b *testing.B) {
			for _, implementation := range []string{"Turbo", "Reused", "Stdlib"} {
				b.Run(implementation, func(b *testing.B) {
					var source strings.Reader
					reader := bufio.NewReader(&source)
					parse := Parse
					var parser Parser
					if implementation == "Reused" {
						parse = parser.Parse
					}
					if implementation == "Stdlib" {
						parse = func(r io.Reader) (*http.Request, error) { return http.ReadRequest(r.(*bufio.Reader)) }
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						source.Reset(workload.wire)
						reader.Reset(&source)
						req, err := parse(reader)
						if err != nil {
							b.Fatal(err)
						}
						if _, err := io.Copy(io.Discard, req.Body); err != nil {
							b.Fatal(err)
						}
						if err := req.Body.Close(); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}

// A larger, realistic header set exercises canonical names beyond the common
// three, and exceeds the shared immutable block's per-request size threshold.
//
//go:embed testdata/browser-headers.txt
var browserHeaderText string

var browserHeaders = strings.ReplaceAll(browserHeaderText, "\n", "\r\n")

func mapHeaderNames(fields string, transform func(string) string) string {
	var result strings.Builder
	for _, line := range strings.SplitAfter(fields, "\r\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			result.WriteString(transform(key))
			result.WriteByte(':')
			result.WriteString(value)
		}
	}
	return result.String()
}

func mixedHeaderCase(key string) string {
	key = strings.ToLower(key)
	result := []byte(key)
	for i := 1; i < len(result); i += 2 {
		if 'a' <= result[i] && result[i] <= 'z' {
			result[i] -= 'a' - 'A'
		}
	}
	return string(result)
}

var ownedHeaderResult string

func BenchmarkOwnHeader(b *testing.B) {
	for _, workload := range []struct {
		name, target, fields string
	}{
		{"SmallGET", "/bench", "Host: benchmark.local\r\nConnection: close\r\nContent-Length: 0\r\n"},
		{"Headers", "/a?b=c", "Host: benchmark.local\r\nUser-Agent: benchmark/1.0\r\nAccept: */*\r\nAccept-Encoding: gzip\r\nConnection: close\r\nX-Request-Id: 12345\r\n"},
		{"Large", "/bench", "X-Large: " + strings.Repeat("x", 2048) + "\r\n"},
	} {
		b.Run(workload.name, func(b *testing.B) {
			for _, mode := range []string{"Fresh", "Reused"} {
				b.Run(mode, func(b *testing.B) {
					target, fields := []byte(workload.target), []byte(workload.fields)
					var parser Parser
					var storage *Parser
					if mode == "Reused" {
						storage = &parser
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						ownedHeaderResult = ownHeader(target, fields, storage)
					}
				})
			}
		})
	}
}
