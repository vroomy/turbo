package turbo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vroomy/turbo/internal/storagestudy"
	"github.com/vroomy/turbo/request"
)

type storageStudyConn struct {
	http11ProcessConn
	source storagestudy.Reader
}

func (c *storageStudyConn) Read(p []byte) (int, error) { return c.source.Read(p) }

// Keep both SmallGET handler Header.Set calls. Large field sets additionally
// exercise a response map with 129 fields and serialization beyond 16 KiB.
func storageStudyHandler() http.HandlerFunc {
	base := http11Workloads()[0].handler()
	large := responseHeaderBenchmarkHandler(129)
	value := strings.Repeat("v", 20<<10)
	return func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header) > 64 {
			w.Header().Set("X-Large", value)
			large(w, r)
		} else {
			base(w, r)
		}
	}
}

func BenchmarkProcessStorage(b *testing.B) {
	cases := storagestudy.Sizes()
	for _, method := range []string{"GET", "HEAD"} {
		for _, c := range storagestudy.Fragments(method) {
			c.Name = method + "/" + c.Name
			cases = append(cases, c)
		}
	}
	for _, c := range cases {
		b.Run(c.Name, func(b *testing.B) {
			conn := &storageStudyConn{}
			server := Turbo{h: storageStudyHandler()}
			writer := makeResponseWriter()
			var parser request.Parser
			g := newBenchmarkGoroutines(b)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				conn.source = storagestudy.Reader{Wire: c.Wires[i%len(c.Wires)], Cut: c.Cut}
				if err := server.process(conn, &writer, &parser); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			g.report(b)
		})
	}
}

// Packing candidates use exactly the existing round-trip client, watchdog,
// response validation, and handler. Fragmentation is isolated above because
// socket writes cannot reliably dictate server-side read boundaries.
func BenchmarkRoundTripStorage(b *testing.B) {
	for _, transport := range []string{"Pipe", "TCP"} {
		b.Run(transport, func(b *testing.B) {
			for _, size := range []int{68, 257, 704} {
				b.Run(fmt.Sprintf("Bytes%d", size), func(b *testing.B) {
					for _, workers := range []int{1, 8, 32} {
						b.Run(fmt.Sprintf("Workers%d", workers), func(b *testing.B) {
							for _, clients := range []int{1, 8} {
								b.Run(fmt.Sprintf("Clients%d", clients), func(b *testing.B) {
									w := http11Workloads()[0]
									w.requestWire = []byte(storagestudy.Sized(size))
									benchmarkHTTP11Config(b, transport, "Turbo", w, http11BenchmarkConfig{workers, 1024, clients}, false)
								})
							}
						})
					}
				})
			}
		})
	}
}

// Each wave occupies every actual server worker before it is released. There
// are no clients, sleeps, or per-request goroutines. Connections discard writes
// so profiles isolate server storage from client/Pipe/response-parser storage.
func TestStorageWorkerHeap(t *testing.T) {
	dir := os.Getenv("TURBO_STORAGE_DIR")
	if dir == "" {
		t.Skip("set TURBO_STORAGE_DIR")
	}
	workers, err := strconv.Atoi(os.Getenv("TURBO_STORAGE_WORKERS"))
	if err != nil || workers < 1 {
		t.Fatal("invalid worker count")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	g := newBenchmarkGoroutines(t)
	clones := make([]*http.Request, 0, workers*16)
	snapshot := func(stage string) {
		m := http11StudyMemory()
		data, err := json.MarshalIndent(struct {
			http11StudyHeap
			CreatedSincePrevious uint64
			Clones               int
		}{m, g.delta(), len(clones)}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, stage+".json"), data, 0644); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, stage+".pprof"))
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.WriteHeapProfile(f); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		runtime.KeepAlive(clones)
	}
	snapshot("initial")
	started := make(chan *http.Request, workers)
	resume, stop := make(chan struct{}), make(chan struct{})
	listener := newTurboTestListener(workers)
	base := storageStudyHandler()
	server := newTurbo(listener, workers, 1024, func(w http.ResponseWriter, r *http.Request) {
		base(w, r)
		started <- r.Clone(r.Context())
		select {
		case <-resume:
		case <-stop:
		}
	})
	defer func() { close(stop); closeTurbo(t, server) }()
	awaitIdle := func() {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		stacks := make([]byte, 1<<20)
		for {
			n := runtime.Stack(stacks, true)
			idle := 0
			for _, stack := range strings.Split(string(stacks[:n]), "\n\n") {
				if strings.Contains(stack, "(*Turbo).work(") && strings.Contains(strings.SplitN(stack, "\n", 2)[0], "[chan receive]") {
					idle++
				}
			}
			if idle == workers {
				return
			}
			if time.Now().After(until) {
				t.Fatal("workers did not become idle")
			}
			runtime.Gosched()
		}
	}
	awaitIdle()
	snapshot("cold")
	for wave := range 16 {
		wire, cut := storagestudy.Sized(68), 0
		if wave%2 == 1 {
			wire = storagestudy.Fields(129)
		}
		if wave%4 == 2 {
			cut = len(wire) - 1
		}
		for range workers {
			listener.pending <- turboAcceptResult{conn: &storageStudyConn{source: storagestudy.Reader{Wire: wire, Cut: cut}}}
		}
		for range workers {
			<-listener.accepted
		}
		for range workers {
			select {
			case clone := <-started:
				clones = append(clones, clone)
			case <-time.After(5 * time.Second):
				t.Fatal("worker did not reach barrier")
			}
		}
		if wave == 15 {
			snapshot("warm-active")
		}
		for range workers {
			resume <- struct{}{}
		}
		awaitIdle()
	}
	snapshot("large-idle")
	// A final small request clears the large response map in place; the large
	// idle snapshot above intentionally exposes its last-response retention.
	for range workers {
		listener.pending <- turboAcceptResult{conn: &storageStudyConn{source: storagestudy.Reader{Wire: storagestudy.Sized(68)}}}
	}
	for range workers {
		<-listener.accepted
	}
	for range workers {
		<-started
	}
	for range workers {
		resume <- struct{}{}
	}
	awaitIdle()
	snapshot("small-idle")
	if err := closeTurbo(t, server); err != nil {
		t.Fatal(err)
	}
	snapshot("stopped-clones")
	for _, clone := range clones {
		if clone.RequestURI != "/bench" || clone.Host != "benchmark.local" {
			t.Fatal("clone changed after shutdown")
		}
		if len(clone.Header) > 64 && clone.Header.Get("X-128") != "v" {
			t.Fatal("large clone changed")
		}
	}
	clones = nil
	snapshot("released")
}
