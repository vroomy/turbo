package turbo

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Reuse the original SmallGET workload with the sustained OpenRTB measurement
// protocol: one continuous B.Loop window and GC counters at its boundaries.
func BenchmarkSmallGETHTTP11(b *testing.B) {
	for _, transport := range []string{"Pipe", "TCP"} {
		b.Run(transport, func(b *testing.B) {
			for _, implementation := range []string{"Turbo", "Stdlib"} {
				b.Run(implementation, func(b *testing.B) {
					w := http11Workloads()[0]
					benchmarkHTTP11Sustained(b, transport, implementation, w, w.handler())
				})
			}
		})
	}
}

func benchmarkHTTP11Sustained(b *testing.B, transport, implementation string, w http11Workload, handler http.HandlerFunc) {
	config := defaultHTTP11BenchmarkConfig()
	dial := startHTTP11BenchmarkServer(b, transport, implementation, handler, config)
	client := newHTTP11BenchmarkClient(dial, w.request(), w)
	if err := client.roundTrip(true); err != nil {
		b.Fatal(err)
	}
	g := newBenchmarkGoroutines(b)
	// Establish the same clean baseline; exclude this forced GC.
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	g.delta()
	b.ReportAllocs()
	b.SetBytes(int64(len(w.requestBody) + len(w.expectedBody())))
	for b.Loop() {
		if err := client.roundTrip(false); err != nil {
			b.Fatal(err)
		}
	}
	// Loop has stopped the timer; no per-request instrumentation.
	runtime.ReadMemStats(&after)
	g.report(b)
	gc := after.NumGC - before.NumGC
	forced := after.NumForcedGC - before.NumForcedGC
	pause := after.PauseTotalNs - before.PauseTotalNs
	b.ReportMetric(b.Elapsed().Seconds(), "seconds")
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "req/s")
	b.ReportMetric(float64(gc), "gc-cycles")
	b.ReportMetric(float64(forced), "forced-gc")
	b.ReportMetric(float64(gc)*1e6/float64(b.N), "gc/1M-req")
	b.ReportMetric(float64(pause)/1e6, "gc-pause-ms")
	b.ReportMetric(float64(pause)/float64(b.N), "gc-pause-ns/op")
	b.ReportMetric(float64(pause)/float64(b.Elapsed())*100, "gc-pause-%")
	if dir := os.Getenv("TURBO_OPENRTB_RESULT_DIR"); dir != "" {
		result := struct {
			Transport, Implementation                 string
			Requests                                  int
			Seconds, NsPerOp, BytesPerOp, AllocsPerOp float64
			GCCycles, ForcedGC                        uint32
			GCPauseNs                                 uint64
			RequestBodyBytes, ResponseBodyBytes       int
		}{transport, implementation, b.N, b.Elapsed().Seconds(), float64(b.Elapsed()) / float64(b.N),
			float64(after.TotalAlloc-before.TotalAlloc) / float64(b.N), float64(after.Mallocs-before.Mallocs) / float64(b.N),
			gc, forced, pause, len(w.requestBody), len(w.expectedBody())}
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			b.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, transport+"-"+implementation+".json"), append(data, '\n'), 0644); err != nil {
			b.Fatal(err)
		}
	}
}
