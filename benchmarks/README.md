# HTTP/1.1 without keep-alives

See [recorded results](RESULTS.md) for the full comparison, raw samples, and
the allocation improvement from reader reuse.

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

`TCP` uses loopback sockets and includes connection setup and teardown. `Pipe`
uses a fresh `net.Pipe` connection and the full accept/worker/handler paths,
avoiding kernel TCP costs so server allocations and CPU work are easier to
compare. Both include client parsing, connection allocation, deadlines, and
response validation; their allocation counts are not server-only figures.

`Serial` has one client. `Parallel` uses `GOMAXPROCS` clients via `RunParallel`.
Turbo has `GOMAXPROCS` workers and an equally sized queue in both modes; stdlib
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

Use `-cpu=1,4,8` to vary worker and client concurrency together. Avoid running
other benchmarks simultaneously. Loopback timing depends on CPU scheduling
and the operating system's TCP stack; use repeated samples and compare both
transports before drawing conclusions.

Profiles should be collected separately from timing results:

```sh
go test -run '^$' -bench '^BenchmarkHTTP11NoKeepAlive/Pipe/SmallGET$/Serial/Turbo$' -benchtime=3s -cpuprofile=/tmp/turbo-cpu.pprof -memprofile=/tmp/turbo-mem.pprof -o /tmp/turbo-bench.test .
go tool pprof -top /tmp/turbo-bench.test /tmp/turbo-cpu.pprof
go tool pprof -top -alloc_space /tmp/turbo-bench.test /tmp/turbo-mem.pprof
```
