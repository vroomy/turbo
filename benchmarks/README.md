# HTTP/1.1 without keep-alives

See [recorded results](RESULTS.md) for the full comparison, raw samples, and
the allocation improvement from reader reuse.
See [allocation profiling](ALLOCATIONS.md) for the subsequent response writer
improvements, server-only measurements, and remaining allocation sources.

`BenchmarkHTTP11NoKeepAlive` compares Turbo with `net/http.Server` using the
same handlers, prebuilt requests, payloads, and client code. Every operation
opens a new connection, sends `Connection: close`, reads the entire response,
and verifies HTTP/1.1, status 200, body length, and connection EOF. The stdlib
server also has HTTP keep-alives explicitly disabled. No TLS or HTTP/2 is used.

The benchmark validates payload contents before starting the timer. The
`TestHTTP11BenchmarkContract` tests run these checks against both servers and
both transports, including repeated requests on fresh connections.

## Cases

| Workload | Request body | Response body | Response framing |
| --- | ---: | ---: | --- |
| SmallGET | 0 | 128 B | Explicit Content-Length and Content-Type |
| ImplicitGET | 0 | 128 B | Handler only calls Write |
| LargeGET | 0 | 64 KiB | Explicit Content-Length and Content-Type |
| POST | 1 KiB, fully consumed | 128 B | Explicit Content-Length and Content-Type |
| HEAD | 0 | 0 on the wire | Headers describe a 128 B representation |
| Empty | 0 | 0 | Explicit Content-Length: 0 |
| StreamGET | 0 | 512 B in four writes | Explicit Content-Length and Content-Type |

`TCP` uses loopback sockets and includes connection setup and teardown. `Pipe`
uses a fresh `net.Pipe` connection and the full accept/worker/handler paths,
avoiding kernel TCP costs so server allocations and CPU work are easier to
compare. Both include client parsing, connection allocation, timeout handling,
and response validation; their allocation counts are not server-only figures.
TCP uses a 5 s dial timeout and a native 10 s I/O deadline. Pipe uses one owned
10 s watchdog starting before listener handoff, canceled on completion or
failure. See the [timeout design and revised baseline](../testdata/pipe-timeouts/README.md)
for costs, retained heaps, and the full before/after matrix.

`Serial` has one client. `Parallel` uses `GOMAXPROCS` clients via `RunParallel`.
Turbo has 32 workers and a 1,024-slot queue in both modes; stdlib
uses its normal goroutine-per-connection implementation. Both servers use the
same listener configuration. Server setup, warmup, and cleanup are outside the
timer. Parallel ns/op is elapsed time divided by completed requests, not an
individual request's latency. MB/s counts application request and response
body bytes, excluding headers.

For ImplicitGET, stdlib may automatically supply Content-Length while Turbo
uses connection-close framing. All other cases explicitly match framing.

## Run

Run without the race detector, coverage, or profiling when comparing timing:

```sh
go test -race ./...
go test -run '^$' -bench '^BenchmarkHTTP11NoKeepAlive$' -benchmem -benchtime=200ms -count=6 -timeout=5m . > before.txt
# Make a change, then repeat the identical command into after.txt.
benchstat before.txt after.txt
```

To compare the two servers within one run, normalize their names into separate
inputs without changing the measurements:

```sh
sed -n 's@/Stdlib-@-@p' after.txt > /tmp/http11-stdlib.txt
sed -n 's@/Turbo-@-@p' after.txt > /tmp/http11-turbo.txt
benchstat /tmp/http11-stdlib.txt /tmp/http11-turbo.txt
```

For a focused TCP run with a bounded number of new connections (the command
used for the recorded focused comparison):

```sh
go test -run '^$' -bench '^BenchmarkHTTP11NoKeepAlive/TCP/SmallGET$/Serial' -benchmem -benchtime=200x -count=10 .
```

Use `-cpu=1,4,8` to vary CPU capacity and client concurrency; the worker count
and queue capacity remain fixed in `BenchmarkHTTP11NoKeepAlive`. Avoid running
other benchmarks simultaneously. Loopback timing depends on CPU scheduling
and the operating system's TCP stack; use repeated samples and compare both
transports before drawing conclusions.

Profiles should be collected separately from timing results:

```sh
go test -c -o /tmp/turbo-bench.test .
GOMAXPROCS=8 /tmp/turbo-bench.test -test.run '^$' -test.bench '^BenchmarkHTTP11NoKeepAlive/Pipe/SmallGET$/Serial/Turbo$' -test.benchtime=3s -test.cpuprofile=/tmp/turbo-cpu.pprof
GOMAXPROCS=8 /tmp/turbo-bench.test -test.run '^$' -test.bench '^BenchmarkHTTP11NoKeepAlive/Pipe/SmallGET$/Serial/Turbo$' -test.benchtime=20000x -test.memprofilerate=1 -test.memprofile=/tmp/turbo-mem.pprof
go tool pprof -top /tmp/turbo-bench.test /tmp/turbo-cpu.pprof
go tool pprof -top -alloc_space /tmp/turbo-bench.test /tmp/turbo-mem.pprof
```

## Worker dispatch and latency

`BenchmarkHTTP11Accept` uses SmallGET and independently names Turbo's worker
count (1/8/32), queue capacity (0/8/32/1024), and client count (1/8/32/128).
Every client sends requests sequentially over fresh connections. Requests are
partitioned evenly across exactly the named maximum number of clients;
calibration with fewer requests uses fewer clients. Changing `-cpu` changes
CPU capacity without changing these named parameters. It does not isolate
Accept: it measures the full client/server round trip.

```sh
# Full sweep at fixed CPU capacity; run TCP separately with bounded churn.
GOMAXPROCS=8 go test -run '^$' -bench '^BenchmarkHTTP11Accept$/^Pipe$/' -benchmem -benchtime=100ms -count=6 .
GOMAXPROCS=8 go test -run '^$' -bench '^BenchmarkHTTP11Accept$/^TCP$/' -benchmem -benchtime=256x -count=6 .
# Hold clients at eight while changing CPU capacity, workers and queue.
go test -run '^$' -bench '^BenchmarkHTTP11Accept$/^Pipe$/^Workers(8|32)$/^Queue(0|1024)$/^Clients8$' -cpu=1,4,8 -benchmem -benchtime=100ms -count=6 .
```

These ns/op values measure inverse aggregate throughput. The opt-in
`TestHTTP11DispatchStudy` records individual dial-through-EOF latency, connection
counts, queue occupancy and forced-GC live heaps. It also supports clients
that delay sending headers and handlers that sleep. It adds instrumentation
and uses closed-loop clients; its percentiles do not represent an open-loop
arrival distribution. On the first request error it captures goroutines and
socket states before closing the failed connection, retaining the original
5 s dial/10 s I/O timeouts.

```sh
TURBO_DISPATCH_DIR=/tmp/turbo-study \
TURBO_DISPATCH_CONFIG='{"Transport":"Pipe","Implementation":"Turbo","Workers":1,"Queue":8,"Clients":128,"Requests":512,"Scenario":"slow-handler","Delay":1000000}' \
GOMAXPROCS=8 go test -run '^TestHTTP11DispatchStudy$' -v -timeout=1m .
```

Durations and delays in the JSON configuration are nanoseconds. A positive
`Duration` replaces the request limit. Run each study in its own process.
See [task 4 evidence](../performance/worker-dispatch/README.md) for the full
configuration table, short execution traces and sustained TCP retries.
