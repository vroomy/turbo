package turbo

import (
	_ "embed"
	"strings"
	"testing"

	"github.com/vroomy/turbo/request"
)

//go:embed request/testdata/browser-headers.txt
var processHeaderText string

// Use the existing SmallGET handler, including both Header.Set allocations,
// while varying request parsing work. Escaped paths also exercise net/url.
func BenchmarkTurboProcessHeaders(b *testing.B) {
	for _, workload := range headerNameWorkloads() {
		b.Run(workload.name, func(b *testing.B) {
			wire := workload.request()
			conn := &http11ProcessConn{}
			server := Turbo{h: http11Workloads()[0].handler()}
			writer := makeResponseWriter()
			var parser request.Parser
			goroutines := newBenchmarkGoroutines(b)
			b.ReportAllocs()
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

// All casing variants use exactly the SmallGET response handler.
func headerNameWorkloads() []http11Workload {
	fields := strings.ReplaceAll(processHeaderText, "\n", "\r\n")
	var workloads []http11Workload
	for _, name := range []string{"BrowserHeaders", "LowercaseHeaders", "MixedCaseHeaders", "EscapedHeaders"} {
		headers := fields
		if name == "LowercaseHeaders" || name == "MixedCaseHeaders" {
			var result strings.Builder
			for _, line := range strings.SplitAfter(fields, "\r\n") {
				key, value, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				key = strings.ToLower(key)
				if name == "MixedCaseHeaders" {
					chars := []byte(key)
					for i := 1; i < len(chars); i += 2 {
						if chars[i] >= 'a' && chars[i] <= 'z' {
							chars[i] -= 'a' - 'A'
						}
					}
					key = string(chars)
				}
				result.WriteString(key + ":" + value)
			}
			headers = result.String()
		}
		w := http11Workloads()[0]
		w.name = name
		line := "GET /products/items?page=2 HTTP/1.1\r\n"
		if name == "EscapedHeaders" {
			line = "HEAD /products/a%2fb%20c?page=2 HTTP/1.1\r\n"
			w.method = "HEAD"
		}
		w.requestWire = []byte(line + headers + "\r\n")
		workloads = append(workloads, w)
	}
	return workloads
}

func BenchmarkHTTP11RequestHeaders(b *testing.B) {
	for _, transport := range []string{"Pipe", "TCP"} {
		b.Run(transport, func(b *testing.B) {
			for _, workload := range headerNameWorkloads()[:3] {
				b.Run(workload.name, func(b *testing.B) {
					for _, mode := range []string{"Serial", "Parallel"} {
						b.Run(mode, func(b *testing.B) {
							benchmarkHTTP11(b, transport, "Turbo", workload, mode == "Parallel")
						})
					}
				})
			}
		})
	}
}
