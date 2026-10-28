package request

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"unsafe"
)

// Opt-in full-allocation heap snapshots separate live clones from parser reuse.
func TestHeaderNameRetention(t *testing.T) {
	dir := os.Getenv("TURBO_HEADER_NAMES_PROFILE_DIR")
	if dir == "" {
		t.Skip("set TURBO_HEADER_NAMES_PROFILE_DIR for heap snapshots")
	}
	var parser Parser
	var clones []*http.Request
	snapshot := func(name string) {
		t.Helper()
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		t.Logf("%s: heap=%d objects=%d parser-size=%d header-block-cap=%d value-cap=%d clones=%d", name, m.HeapAlloc, m.HeapObjects, unsafe.Sizeof(parser), parser.headerText.Cap(), cap(parser.values), len(clones))
		f, err := os.Create(filepath.Join(dir, name+".pprof"))
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.WriteHeapProfile(f); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		runtime.KeepAlive(&parser)
		runtime.KeepAlive(clones)
	}
	snapshot("initial")
	for i := range 128 {
		fields := mapHeaderNames(browserHeaders, strings.ToLower)
		if i%2 == 0 {
			fields = fmt.Sprintf("host: example.com\r\nx-changing-%d: value\r\n", i)
		}
		req, err := parser.Parse(requestReader("GET /retained HTTP/1.1\r\n"+fields+"\r\n", "buffered"))
		if err != nil {
			t.Fatal(err)
		}
		clones = append(clones, req.Clone(req.Context()))
	}
	parser.Reset()
	snapshot("clones")
	clones = nil
	snapshot("idle")
	parser = Parser{}
	snapshot("released")
}
