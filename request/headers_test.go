package request

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// Exercise every byte at key/casing boundaries and in common-header values.
// Invalid fast-path inputs must reach stdlib without consuming any bytes.
func TestParseHeaderBytes(t *testing.T) {
	var parser Parser
	for c := range 256 {
		t.Run(fmt.Sprintf("%02x", c), func(t *testing.T) {
			for _, fields := range []string{
				"X" + string(byte(c)) + "-Test: value\r\n",
				string(byte(c)) + "-Test: value\r\n",
				"x-Mixed" + string(byte(c)) + ": value\r\n",
				"Host" + string(byte(c)) + ": value\r\n",
				"Host: a" + string(byte(c)) + "b\r\n",
				"Connection: a" + string(byte(c)) + "b\r\n",
				"Content-Length: " + string(byte(c)) + "\r\n",
			} {
				wire := "GET / HTTP/1.1\r\nX-First: first\r\n" + fields + "\r\n"
				compareParse(t, requestReader(wire, "buffered"), wire)
				req, err := parser.Parse(requestReader(wire, "buffered"))
				compareRequest(t, wire, req, err)
			}
		})
	}
}

func TestParserHeaderGrowth(t *testing.T) {
	var parser Parser
	var source strings.Reader
	reader := bufio.NewReader(&source)
	var clones []*http.Request
	var expected []http.Header
	// Start with little capacity, grow within and past the retention bound,
	// then return to small requests. Host comes last to exercise that ordering.
	for _, count := range []int{1, 2, 8, 64, 128, 129, 3, 0, 1} {
		var wire strings.Builder
		wire.WriteString("GET /growth HTTP/1.1\r\n")
		want := make(http.Header)
		for i := range count {
			key, value := fmt.Sprintf("X-%d", i), fmt.Sprintf("value-%d", i)
			fmt.Fprintf(&wire, "%s: %s\r\n", key, value)
			want[key] = []string{value}
		}
		wire.WriteString("Host: example.com\r\n\r\n")
		source.Reset(wire.String())
		reader.Reset(&source)
		req, err := parser.Parse(reader)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(req.Header, want) {
			t.Fatalf("%d fields: headers = %v, want %v", count, req.Header, want)
		}
		clones = append(clones, req.Clone(req.Context()))
		expected = append(expected, want)
		for key, values := range req.Header {
			if cap(values) != 1 {
				t.Fatalf("%s value slice has capacity %d", key, cap(values))
			}
			req.Header.Add(key, "handler")
		}
		for key, values := range want {
			if !reflect.DeepEqual(req.Header[key], []string{values[0], "handler"}) {
				t.Fatalf("handler mutation corrupted %s: %v", key, req.Header[key])
			}
		}
		parser.Reset()
		if cap(parser.values) > maxRetainedHeaders || len(parser.headers) != 0 {
			t.Fatal("Reset retained excessive storage or header entries")
		}
		if count > 0 && count <= maxRetainedHeaders && cap(parser.values) < count {
			t.Fatalf("Reset discarded reusable storage for %d fields", count)
		}
		for _, value := range parser.values[:cap(parser.values)] {
			if value != "" {
				t.Fatal("Reset retained a header string")
			}
		}
	}
	for i, clone := range clones {
		if clone.Host != "example.com" || clone.URL.Path != "/growth" || !reflect.DeepEqual(clone.Header, expected[i]) {
			t.Fatalf("reuse changed clone %d: %+v", i, clone)
		}
	}
}

func TestParserLateFallback(t *testing.T) {
	var parser Parser
	first := "GET /retained HTTP/1.1\r\nHost: retained.local\r\nX-Retained: first\r\n\r\n"
	req, err := parser.Parse(requestReader(first, "buffered"))
	if err != nil {
		t.Fatal(err)
	}
	clone := req.Clone(req.Context())
	// Grow value storage before deciding to fall back. Neither partial maps
	// nor values may survive into the following request, even on an error.
	for _, tail := range []string{
		"X-First: duplicate\r\n\r\n",
		"Host: duplicate.local\r\n\r\n",
		"Content-Length: 4\r\n\r\nbody",
		"Bad(Name: invalid\r\n\r\n",
		"Connection: bad\x00value\r\n\r\n",
	} {
		wire := "GET /fallback HTTP/1.1\r\nHost: fallback.local\r\nX-First: first\r\nX-Second: second\r\nX-Third: third\r\n" + tail
		for split := 1; split <= len(wire); split++ {
			source := io.MultiReader(strings.NewReader(wire[:split]), strings.NewReader(wire[split:]))
			got, err := parser.Parse(bufio.NewReader(source))
			compareRequest(t, wire, got, err)
			if len(parser.headers) != 0 || len(parser.values) != 0 {
				t.Fatal("fallback retained partial header storage")
			}
			got, err = parser.Parse(requestReader("HEAD /next HTTP/1.1\r\n\r\n", "buffered"))
			compareRequest(t, "HEAD /next HTTP/1.1\r\n\r\n", got, err)
		}
	}
	if clone.Host != "retained.local" || clone.RequestURI != "/retained" || clone.Header.Get("X-Retained") != "first" {
		t.Fatal("fallback or reuse changed retained clone")
	}
}
