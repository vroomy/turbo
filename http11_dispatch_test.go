package turbo

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTP11ClientPartition(t *testing.T) {
	for _, clients := range []int{1, 8, 32, 128} {
		for _, requests := range []int{0, 1, 7, 128, 259} {
			counts := make([]int, clients)
			runHTTP11Clients(requests, clients, func(client, count int) { counts[client] = count })
			total := 0
			for _, count := range counts {
				total += count
				if count < requests/clients || count > requests/clients+1 {
					t.Fatalf("unbalanced partition: %v", counts)
				}
			}
			if total != requests {
				t.Fatalf("%d clients completed %d requests, want %d", clients, total, requests)
			}
		}
	}
}

// Close must also drain an accepted connection blocked on a full queue. The
// third connection is already accepted; the fourth remains in the listener.
func TestTurboFullQueueClose(t *testing.T) {
	listener := newTurboTestListener(4)
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	server := newTurbo(listener, 1, 1, func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte("ok"))
	})
	t.Cleanup(func() {
		finish()
		closeTurbo(t, server)
	})
	connections := make([]*turboTestConn, 4)
	for i := range connections {
		connections[i] = newTurboTestConn("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
		if i == 2 {
			connections[i] = newTurboTestConn("malformed request\r\n\r\n")
		}
		listener.pending <- turboAcceptResult{conn: connections[i]}
	}
	waitForTurboSignal(t, started, "first handler")
	for range 3 {
		waitForTurboSignal(t, listener.accepted, "full queue accept")
	}
	if len(server.conns) != 1 {
		t.Fatalf("queue length = %d, want 1", len(server.conns))
	}
	select {
	case <-listener.accepted:
		t.Fatal("listener accepted beyond the blocked send")
	default:
	}
	done := make(chan error, 1)
	go func() { done <- server.Close() }()
	waitForTurboSignal(t, listener.closed, "listener close")
	select {
	case err := <-done:
		t.Fatalf("Close returned with blocked work: %v", err)
	default:
	}
	finish()
	if err := waitForTurboClose(t, done); err != nil {
		t.Fatal(err)
	}
	for i, conn := range connections[:3] {
		if got := conn.closeCount.Load(); got != 1 {
			t.Errorf("accepted connection %d closed %d times, want once", i, got)
		}
	}
	if got := connections[3].closeCount.Load(); got != 0 {
		t.Errorf("unaccepted connection closed %d times", got)
	}
}

// This opt-in study deliberately adds clocks, connection accounting, latency
// storage and periodic sampling. Use BenchmarkHTTP11Accept for timing claims.
// Each invocation writes result.json, and the first error captures live stacks
// and sockets before the client closes its connection. Deadlines are unchanged.
type http11StudyConfig struct {
	Transport, Implementation string
	Workers, Queue, Clients   int
	Requests                  int
	Duration                  time.Duration
	Scenario                  string
	Delay                     time.Duration
}

type http11StudyHeap struct {
	HeapBytes, HeapObjects, StackBytes uint64
	Goroutines                         int
}

func http11StudyMemory() http11StudyHeap {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return http11StudyHeap{m.HeapAlloc, m.HeapObjects, m.StackInuse, runtime.NumGoroutine()}
}

type http11StudyListener struct {
	net.Listener
	accepted, closed, closeCalls atomic.Int64
}

func (l *http11StudyListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.accepted.Add(1)
	return &http11StudyConn{Conn: c, listener: l}, nil
}

type http11StudyConn struct {
	net.Conn
	listener *http11StudyListener
	closed   atomic.Bool
}

func (c *http11StudyConn) Close() error {
	c.listener.closeCalls.Add(1)
	if c.closed.CompareAndSwap(false, true) {
		c.listener.closed.Add(1)
	}
	return c.Conn.Close()
}

func TestHTTP11DispatchStudy(t *testing.T) {
	dir := os.Getenv("TURBO_DISPATCH_DIR")
	if dir == "" {
		t.Skip("set TURBO_DISPATCH_DIR and TURBO_DISPATCH_CONFIG for the dispatch study")
	}
	var config http11StudyConfig
	if err := json.Unmarshal([]byte(os.Getenv("TURBO_DISPATCH_CONFIG")), &config); err != nil {
		t.Fatal(err)
	}
	if (config.Transport != "TCP" && config.Transport != "Pipe") ||
		(config.Implementation != "Turbo" && config.Implementation != "Stdlib") ||
		config.Workers < 1 || config.Queue < 0 || config.Clients < 1 ||
		config.Duration < 0 || (config.Duration == 0 && config.Requests < 1) || config.Delay < 0 {
		t.Fatal("invalid study configuration")
	}
	if config.Scenario != "normal" && config.Scenario != "slow-handler" && config.Scenario != "slow-client" {
		t.Fatal("scenario must be normal, slow-handler or slow-client")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	result := struct {
		Config                                http11StudyConfig
		Attempts, Completed, Dialed, Accepted int64
		Closed, CloseCalls                    int64
		Errors                                []string
		Seconds, RequestsPerSecond            float64
		P50NS, P95NS, P99NS, MaxNS            int64
		MaxQueue, MaxGoroutines               int
		Initial, Idle, CompletedHeap, Stopped http11StudyHeap
		Listener                              string
	}{Config: config, Initial: http11StudyMemory()}
	var attempts, completed, dialed atomic.Int64
	var stop atomic.Bool
	var errorsMu sync.Mutex
	var firstError sync.Once
	var listener *http11StudyListener
	t.Run("load", func(t *testing.T) {
		workload := http11Workloads()[0]
		rawListener, rawDial := newHTTP11BenchmarkListener(t, config.Transport)
		listener = &http11StudyListener{Listener: rawListener}
		result.Listener = listener.Addr().String()
		handler := workload.handler()
		if config.Scenario == "slow-handler" {
			next := handler
			handler = func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(config.Delay)
				next(w, r)
			}
		}
		server := startHTTP11BenchmarkServerOnListener(t, listener, config.Implementation, handler,
			http11BenchmarkConfig{config.Workers, config.Queue, config.Clients})
		captureFailure := func(err error, conn net.Conn) {
			firstError.Do(func() { writeHTTP11StudyFailure(t, dir, listener, server, conn, err) })
		}
		dial := func() (net.Conn, error) {
			conn, err := rawDial()
			if err == nil {
				dialed.Add(1)
				if config.Scenario == "slow-client" {
					// Hold the connection before sending any request bytes.
					time.Sleep(config.Delay)
				}
			}
			return conn, err
		}
		preflight := newHTTP11BenchmarkClient(dial, workload.request(), workload)
		preflight.onError = captureFailure
		if err := preflight.roundTrip(true); err != nil {
			captureFailure(err, nil)
			result.Errors = append(result.Errors, "preflight: "+err.Error())
			return
		}
		result.Idle = http11StudyMemory()
		// The preflight connection is recorded separately from load attempts.
		latencies := make([][]int64, config.Clients)
		for i := range latencies {
			latencies[i] = make([]int64, 0, max(config.Requests/config.Clients, 1024))
		}
		sampleDone := make(chan struct{})
		var sampler sync.WaitGroup
		sampler.Go(func() {
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-sampleDone:
					return
				case <-ticker.C:
					if server != nil {
						result.MaxQueue = max(result.MaxQueue, len(server.conns))
					}
					result.MaxGoroutines = max(result.MaxGoroutines, runtime.NumGoroutine())
				}
			}
		})
		start := time.Now()
		until := start.Add(config.Duration)
		ctx, task := trace.NewTask(t.Context(), "HTTP/1.1 dispatch load")
		trace.WithRegion(ctx, "clients", func() {
			requests := config.Requests
			if config.Duration > 0 {
				requests = config.Clients // one long-running loop per client
			}
			runHTTP11Clients(requests, config.Clients, func(id, count int) {
				client := newHTTP11BenchmarkClient(dial, workload.request(), workload)
				client.onError = captureFailure
				for n := 0; !stop.Load(); n++ {
					if config.Duration == 0 && n == count || config.Duration > 0 && time.Now().After(until) {
						break
					}
					attempts.Add(1)
					began := time.Now()
					err := client.roundTrip(false)
					latencies[id] = append(latencies[id], time.Since(began).Nanoseconds())
					if err != nil {
						stop.Store(true)
						errorsMu.Lock()
						result.Errors = append(result.Errors, err.Error())
						errorsMu.Unlock()
						// Dial failures occur before the client's onError hook.
						captureFailure(err, nil)
						break
					}
					completed.Add(1)
				}
			})
		})
		task.End()
		result.Seconds = time.Since(start).Seconds()
		close(sampleDone)
		sampler.Wait()
		var all []int64
		for _, samples := range latencies {
			all = append(all, samples...)
		}
		slices.Sort(all)
		if len(all) > 0 {
			result.P50NS = all[(len(all)-1)*50/100]
			result.P95NS = all[(len(all)-1)*95/100]
			result.P99NS = all[(len(all)-1)*99/100]
			result.MaxNS = all[len(all)-1]
		}
		result.CompletedHeap = http11StudyMemory()
	}) // close the server before collecting stopped counters and memory
	result.Stopped = http11StudyMemory()
	result.Attempts, result.Completed, result.Dialed = attempts.Load(), completed.Load(), dialed.Load()
	if listener != nil {
		result.Accepted, result.Closed, result.CloseCalls = listener.accepted.Load(), listener.closed.Load(), listener.closeCalls.Load()
	}
	if result.Seconds > 0 {
		result.RequestsPerSecond = float64(result.Completed) / result.Seconds
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) > 0 {
		t.Errorf("%d requests failed; evidence in %s", len(result.Errors), dir)
	}
	if result.Accepted != result.Closed || (config.Implementation == "Turbo" && result.CloseCalls != result.Accepted) {
		t.Errorf("connection accounting: accepted=%d closed=%d close calls=%d", result.Accepted, result.Closed, result.CloseCalls)
	}
}

func writeHTTP11StudyFailure(t *testing.T, dir string, listener *http11StudyListener, server *Turbo, conn net.Conn, failure error) {
	t.Helper()
	file, err := os.Create(filepath.Join(dir, "failure-goroutines.txt"))
	if err != nil {
		t.Error(err)
		return
	}
	fmt.Fprintf(file, "error=%v accepted=%d closed=%d goroutines=%d\n", failure,
		listener.accepted.Load(), listener.closed.Load(), runtime.NumGoroutine())
	if server != nil {
		fmt.Fprintf(file, "queue=%d/%d\n", len(server.conns), cap(server.conns))
	}
	if conn != nil {
		fmt.Fprintf(file, "client=%s server=%s\n", conn.LocalAddr(), conn.RemoteAddr())
	}
	_ = pprof.Lookup("goroutine").WriteTo(file, 2)
	_ = file.Close()
	// Capture socket states while the failed connection is still open. Keep a
	// summary of global states plus rows for this listener to bound artifact size.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	args := []string{"-ant"}
	if runtime.GOOS == "darwin" {
		args = []string{"-anv", "-p", "tcp"}
	}
	output, err := exec.CommandContext(ctx, "netstat", args...).CombinedOutput()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	states := make(map[string]int)
	listenerStates := make(map[string]int)
	var matching []string
	waitSamples := 0
	for line := range strings.SplitSeq(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || !strings.HasPrefix(fields[0], "tcp") {
			continue
		}
		states[fields[5]]++
		if strings.HasSuffix(fields[3], "."+port) || strings.HasSuffix(fields[4], "."+port) ||
			strings.HasSuffix(fields[3], ":"+port) || strings.HasSuffix(fields[4], ":"+port) {
			listenerStates[fields[5]]++
			if fields[5] != "TIME_WAIT" || waitSamples < 8 {
				matching = append(matching, line)
			}
			if fields[5] == "TIME_WAIT" {
				waitSamples++
			}
		}
	}
	summary := fmt.Sprintf("netstat error=%v\nglobal states=%v\nlistener=%s\nlistener states=%v\nTIME_WAIT rows limited to eight\n%s\n", err, states, listener.Addr(), listenerStates, strings.Join(matching, "\n"))
	if err := os.WriteFile(filepath.Join(dir, "failure-sockets.txt"), []byte(summary), 0644); err != nil {
		t.Error(err)
	}
}
