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
	for _, workload := range []struct{ name, line string }{
		{"BrowserHeaders", "GET /products/items?page=2 HTTP/1.1\r\n"},
		{"EscapedHeaders", "HEAD /products/a%2fb%20c?page=2 HTTP/1.1\r\n"},
	} {
		b.Run(workload.name, func(b *testing.B) {
			wire := []byte(workload.line + strings.ReplaceAll(processHeaderText, "\n", "\r\n") + "\r\n")
			conn := &http11ProcessConn{}
			server := Turbo{h: http11Workloads()[0].handler()}
			writer := makeResponseWriter()
			var parser request.Parser
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				conn.request.Reset(wire)
				if err := server.process(conn, &writer, &parser); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
