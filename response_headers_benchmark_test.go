package turbo

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
)

// Keep the original workload handlers intact, including their Header.Set calls.
// These additional cases measure sorting and reuse beyond the tiny default map.
func responseHeaderBenchmarkHandler(count int) http.HandlerFunc {
	base := http11Workloads()[0].handler()
	keys := make([]string, count)
	for i := range keys {
		keys[i] = fmt.Sprintf("X-Response-%03d", i)
	}
	return func(w http.ResponseWriter, req *http.Request) {
		for _, key := range keys {
			w.Header().Set(key, "response header value")
		}
		base(w, req)
	}
}

func BenchmarkTurboProcessResponseHeaders(b *testing.B) {
	for _, count := range []int{0, 8, 32, 128} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			wire := http11Workloads()[0].request()
			conn := new(http11ProcessConn)
			server := Turbo{h: responseHeaderBenchmarkHandler(count)}
			writer := makeResponseWriter()
			goroutines := newBenchmarkGoroutines(b)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				conn.request.Reset(wire)
				if err := server.process(conn, &writer); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			goroutines.report(b)
		})
	}
}

func BenchmarkHTTP11ResponseHeaders(b *testing.B) {
	for _, transport := range []string{"Pipe", "TCP"} {
		b.Run(transport, func(b *testing.B) {
			for _, count := range []int{8, 32, 128} {
				b.Run(strconv.Itoa(count), func(b *testing.B) {
					for _, parallel := range []bool{false, true} {
						name := "Serial"
						if parallel {
							name = "Parallel"
						}
						b.Run(name, func(b *testing.B) {
							b.StopTimer()
							goroutines := newBenchmarkGoroutines(b)
							workload := http11Workloads()[0]
							config := defaultHTTP11BenchmarkConfig()
							if parallel {
								config.clients = 8
							}
							dial := startHTTP11BenchmarkServer(b, transport, "Turbo", responseHeaderBenchmarkHandler(count), config)
							client := newHTTP11BenchmarkClient(dial, workload.request(), workload)
							if err := client.roundTrip(true); err != nil {
								b.Fatal(err)
							}
							// Use the same client, body validation, timeout and EOF check
							// as the original end-to-end benchmarks.
							b.ReportAllocs()
							setupGoroutines := goroutines.delta()
							b.ResetTimer()
							b.StartTimer()
							if parallel {
								runHTTP11Clients(b.N, config.clients, func(_ int, count int) {
									client := newHTTP11BenchmarkClient(dial, workload.request(), workload)
									for range count {
										if err := client.roundTrip(false); err != nil {
											b.Error(err)
											return
										}
									}
								})
							} else {
								for i := 0; i < b.N; i++ {
									if err := client.roundTrip(false); err != nil {
										b.Fatal(err)
									}
								}
							}
							b.StopTimer()
							goroutines.report(b)
							b.ReportMetric(float64(setupGoroutines), "setup-goroutines")
						})
					}
				})
			}
		})
	}
}
