package turbo

import (
	"runtime/metrics"
	"sync"
	"testing"
)

// Count creation, not the difference in live goroutines: a goroutine which
// starts and exits during a request still puts pressure on the scheduler.
// Read only at benchmark boundaries, outside the timed/allocation interval.
// The counter is process-wide, so clients and runtime activity are included.
type benchmarkGoroutines struct {
	samples [1]metrics.Sample
	start   uint64
}

func newBenchmarkGoroutines(tb testing.TB) *benchmarkGoroutines {
	tb.Helper()
	g := &benchmarkGoroutines{}
	g.samples[0].Name = "/sched/goroutines-created:goroutines"
	metrics.Read(g.samples[:])
	if g.samples[0].Value.Kind() != metrics.KindUint64 {
		tb.Fatal("runtime does not expose the cumulative goroutine creation counter")
	}
	g.start = g.samples[0].Value.Uint64()
	return g
}

// delta starts the next measurement interval without allocating a new sample.
func (g *benchmarkGoroutines) delta() uint64 {
	metrics.Read(g.samples[:])
	end := g.samples[0].Value.Uint64()
	delta := end - g.start
	g.start = end
	return delta
}

func (g *benchmarkGoroutines) report(b *testing.B) {
	created := g.delta()
	b.ReportMetric(float64(created), "goroutines")
	b.ReportMetric(float64(created)/float64(b.N), "goroutines/op")
}

func TestBenchmarkGoroutinesCountsExited(t *testing.T) {
	g := newBenchmarkGoroutines(t)
	const count = 100
	var done sync.WaitGroup
	for range count {
		done.Go(func() {})
	}
	done.Wait()
	// Background runtime goroutines may also start; they must not hide the
	// short-lived goroutines which have already finished by this sample.
	if got := g.delta(); got < count {
		t.Fatalf("counted %d creations, want at least %d", got, count)
	}
}
