package request

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"github.com/vroomy/turbo/internal/storagestudy"
)

func BenchmarkParseStorage(b *testing.B) {
	for _, c := range storagestudy.Sizes() {
		b.Run(c.Name, func(b *testing.B) { benchmarkStorageParse(b, c) })
	}
}

func BenchmarkParseFragments(b *testing.B) {
	for _, method := range []string{"GET", "HEAD"} {
		b.Run(method, func(b *testing.B) {
			for _, c := range storagestudy.Fragments(method) {
				b.Run(c.Name, func(b *testing.B) { benchmarkStorageParse(b, c) })
			}
		})
	}
}

func benchmarkStorageParse(b *testing.B, c storagestudy.Case) {
	var p Parser
	var source storagestudy.Reader
	r := bufio.NewReader(&source)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		source = storagestudy.Reader{Wire: c.Wires[i%len(c.Wires)], Cut: c.Cut}
		r.Reset(&source)
		req, err := p.Parse(r)
		if err != nil {
			b.Fatal(err)
		}
		if err := req.Body.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

// Validate the measurement fixtures against stdlib and check clones across
// fragmentation, late fallback, changing sizes, and dropped map storage.
func TestStorageStudyFixtures(t *testing.T) {
	var p Parser
	var clones []*http.Request
	var wires []string
	cases := append(storagestudy.Sizes(), storagestudy.Fragments("GET")...)
	cases = append(cases, storagestudy.Fragments("HEAD")...)
	for _, c := range cases {
		for _, wire := range c.Wires {
			source := &storagestudy.Reader{Wire: wire, Cut: c.Cut}
			req, err := p.Parse(bufio.NewReader(source))
			compareRequest(t, wire, req, err)
			if err != nil {
				t.Fatal(err)
			}
			req.Body = http.NoBody // compareRequest clears Body after checking it.
			clones = append(clones, req.Clone(req.Context()))
			wires = append(wires, wire)
			p.Reset()
			if p.headerText.Cap() > headerTextBlockSize || cap(p.values) > maxRetainedHeaders || len(p.headers) != 0 {
				t.Fatal("reset storage bounds exceeded")
			}
		}
	}
	for i, clone := range clones {
		want, err := http.ReadRequest(bufio.NewReader(strings.NewReader(wires[i])))
		if err != nil {
			t.Fatal(err)
		}
		want = want.Clone(want.Context())
		if !reflect.DeepEqual(clone, want) {
			t.Fatalf("clone %d changed after reuse", i)
		}
	}
}

// This opt-in diagnostic attributes exact text bytes using full heap profiles;
// total runtime heap deltas also include maps, clones and profiling overhead.
// Each parser models one independently owned worker. Actual worker stacks and
// response storage are measured separately by TestStorageWorkerHeap.
func TestStoragePackingHeap(t *testing.T) {
	dir := os.Getenv("TURBO_STORAGE_DIR")
	if dir == "" {
		t.Skip("set TURBO_STORAGE_DIR")
	}
	n, err := strconv.Atoi(os.Getenv("TURBO_STORAGE_WORKERS"))
	if err != nil || n < 1 {
		t.Fatal("invalid worker count")
	}
	mode := os.Getenv("TURBO_STORAGE_MODE")
	if mode != "dense" && mode != "sparse" && mode != "alternating" {
		t.Fatal("invalid clone mode")
	}
	size, err := strconv.Atoi(os.Getenv("TURBO_STORAGE_SIZE"))
	if err != nil || size < 68 {
		t.Fatal("invalid request size")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	parsers := make([]Parser, n)
	clones := make([]*http.Request, 0, n*32)
	wire := storagestudy.Sized(size)
	large := storagestudy.Sized(2048)
	var source strings.Reader
	r := bufio.NewReader(&source)
	snapshot := func(stage string) {
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		var blockBytes, valueBytes int
		for i := range parsers {
			blockBytes += parsers[i].headerText.Cap()
			valueBytes += cap(parsers[i].values) * int(unsafe.Sizeof(""))
		}
		data, err := json.MarshalIndent(map[string]any{"heap_bytes": m.HeapAlloc, "heap_objects": m.HeapObjects, "parser_bytes": len(parsers) * int(unsafe.Sizeof(Parser{})), "block_bytes": blockBytes, "value_bytes": valueBytes, "clones": len(clones)}, "", "  ")
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
		runtime.KeepAlive(parsers)
		runtime.KeepAlive(clones)
	}
	snapshot("initial")
	for worker := range parsers {
		p := &parsers[worker]
		for range 32 {
			source.Reset(wire)
			r.Reset(&source)
			req, err := p.Parse(r)
			if err != nil {
				t.Fatal(err)
			}
			clones = append(clones, req.Clone(req.Context()))
			p.Reset()
			if mode == "sparse" {
				// Force even the largest experimental block to roll over before
				// retaining another clone; sparse clones pin whole blocks.
				for range 64 {
					source.Reset(wire)
					r.Reset(&source)
					if _, err := p.Parse(r); err != nil {
						t.Fatal(err)
					}
					p.Reset()
				}
			} else if mode == "alternating" {
				source.Reset(large)
				r.Reset(&source)
				if _, err := p.Parse(r); err != nil {
					t.Fatal(err)
				}
				p.Reset()
			}
		}
	}
	r.Reset(nil)
	snapshot("clones-idle")
	parsers = nil
	snapshot("clones-stopped")
	for i, clone := range clones {
		if clone.RequestURI != "/bench" || clone.Host != "benchmark.local" || clone.Header.Get("X-Pad") != strings.TrimSuffix(strings.Split(wire, "X-Pad: ")[1], "\r\n\r\n") {
			t.Fatalf("clone %d changed: %s", i, fmt.Sprint(clone.Header))
		}
	}
	clones = nil
	snapshot("released")
}
