package turbo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"
)

// Opt-in diagnostics, separate from benchmark timing. Run each implementation
// in its own process so the old harness's pending deadlines cannot overlap.
func TestHTTP11PipeHeap(t *testing.T) {
	dir := os.Getenv("TURBO_PIPE_PROFILE_DIR")
	if dir == "" {
		t.Skip("set TURBO_PIPE_PROFILE_DIR and TURBO_PIPE_PROFILE_SERVER to collect heaps")
	}
	implementation := os.Getenv("TURBO_PIPE_PROFILE_SERVER")
	if implementation != "Turbo" && implementation != "Stdlib" {
		t.Fatal("TURBO_PIPE_PROFILE_SERVER must be Turbo or Stdlib")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	defer func(rate int) { runtime.MemProfileRate = rate }(runtime.MemProfileRate)
	runtime.MemProfileRate = 1
	snapshot := func(t *testing.T, name string) {
		t.Helper()
		// Two collections also settle the heap profile's delayed free accounting.
		runtime.GC()
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		metadata, err := json.MarshalIndent(struct {
			HeapAlloc   uint64
			HeapObjects uint64
			Goroutines  int
		}{stats.HeapAlloc, stats.HeapObjects, runtime.NumGoroutine()}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".json"), append(metadata, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(filepath.Join(dir, name+".pprof"))
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.WriteHeapProfile(file); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	snapshot(t, "initial")
	var completed time.Time
	t.Run(implementation, func(t *testing.T) {
		workload := http11Workloads()[0]
		dial := startHTTP11BenchmarkServer(t, "Pipe", implementation, workload.handler())
		client := newHTTP11BenchmarkClient(dial, workload.request(), workload)
		if err := client.roundTrip(true); err != nil {
			t.Fatal(err)
		}
		snapshot(t, "idle")
		for range 20000 {
			if err := client.roundTrip(false); err != nil {
				t.Fatal(err)
			}
		}
		// Exclude the client's last reader reference on both harness versions.
		client.response.Reset(nil)
		completed = time.Now()
		snapshot(t, "completed")
	}) // testing cleanup stops the server and its workers before this returns.
	if t.Failed() {
		return
	}
	snapshot(t, "stopped")
	// Include expiry of the original client's 10-second SetDeadline timers.
	time.Sleep(time.Until(completed.Add(10500 * time.Millisecond)))
	snapshot(t, "expired")
}
