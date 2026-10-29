package request

import (
	"bufio"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestCanonicalHeaderNames(t *testing.T) {
	names := []string{"Host", "Connection", "Content-Length", "Sec-Ch-Ua", "Sec-Ch-Ua-Mobile", "Sec-Ch-Ua-Platform", "Sec-Fetch-Dest", "Sec-Fetch-Mode", "Sec-Fetch-Site", "Sec-Fetch-User", "Upgrade-Insecure-Requests", "X-Request-Id", "X-Correlation-Id", "Traceparent", "Tracestate", "Authorization", "X-Custom", strings.Repeat("Long-", 20) + "Name"}
	for _, name := range names {
		for _, key := range []string{name, strings.ToLower(name), strings.ToUpper(name), mixedHeaderCase(name)} {
			got, ok := canonicalHeaderKey(key)
			if !ok || got != http.CanonicalHeaderKey(key) {
				t.Fatalf("%q: got %q, %v", key, got, ok)
			}
			// Near misses must never skip token validation or match a constant.
			for i := range key {
				for c := range 256 {
					changed := key[:i] + string(byte(c)) + key[i+1:]
					got, ok := canonicalHeaderKey(changed)
					valid := true
					for j := range changed {
						valid = valid && tokenBytes[changed[j]]
					}
					if ok != valid || (ok && got != http.CanonicalHeaderKey(changed)) {
						t.Fatalf("%q: got %q, %v", changed, got, ok)
					}
				}
			}
		}
	}
}

func TestParserChangingHeaderNames(t *testing.T) {
	var p Parser
	var clones []*http.Request
	var expected []http.Header
	for i := range 300 {
		fields := mapHeaderNames(browserHeaders, mixedHeaderCase)
		if i%2 == 0 {
			fields = fmt.Sprintf("host: example.com\r\nx-changing-%d: value\r\n%s: long\r\n", i, strings.Repeat("long-", 50)+"name")
		}
		wire := "GET /retained HTTP/1.1\r\n" + fields + "\r\n"
		req, err := p.Parse(requestReader(wire, "buffered"))
		if err != nil {
			t.Fatal(err)
		}
		clone := req.Clone(req.Context())
		clones = append(clones, clone)
		want, err := http.ReadRequest(bufio.NewReader(strings.NewReader(wire)))
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, want.Header)
		req.Header.Set("Sec-Fetch-Site", "handler")
	}
	p.Reset()
	for i, clone := range clones {
		if !reflect.DeepEqual(clone.Header, expected[i]) {
			t.Fatalf("clone %d changed: %v", i, clone.Header)
		}
	}
}

// Changing arbitrary names must not accumulate parser-owned interned strings.
// Wires are prepared outside the timer; each parse sees one of 256 names.
func BenchmarkHeaderNames(b *testing.B) {
	for _, workload := range []struct {
		name string
		keys []string
	}{
		{"CustomCanonical", []string{"X-Custom-Name"}},
		{"CustomLowercase", []string{"x-custom-name"}},
		{"CustomMixed", []string{"x-CuStOm-NaMe"}},
		{"LongCanonical", []string{strings.Repeat("Long-", 50) + "Name"}},
		{"LongLowercase", []string{strings.Repeat("long-", 50) + "name"}},
		{"Changing", nil},
		{"Tracing", []string{"traceparent", "tracestate", "x-correlation-id"}},
	} {
		b.Run(workload.name, func(b *testing.B) {
			keys := workload.keys
			if keys == nil {
				for i := range 256 {
					keys = append(keys, fmt.Sprintf("x-changing-%03d", i))
				}
			}
			wires := make([]string, len(keys))
			for i, key := range keys {
				wires[i] = "GET / HTTP/1.1\r\nHost: example.com\r\n" + key + ": value\r\n\r\n"
			}
			var p Parser
			var source strings.Reader
			reader := bufio.NewReader(&source)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				source.Reset(wires[i%len(wires)])
				reader.Reset(&source)
				if _, err := p.Parse(reader); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
