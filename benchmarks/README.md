# HTTP/1.1 without keep-alives

See the [2026-10-28 Turbo/stdlib comparison](#comparison-on-2026-10-28) for
measurements of the current production code.

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

HTTP round-trip and isolated Turbo process benchmarks report `goroutines/op`
and total `goroutines` created during the measured workload. These use Go's
cumulative `/sched/goroutines-created:goroutines` runtime metric, so goroutines
that finish between samples are counted. Reads happen outside the timer, once
at each boundary; there is no per-request instrumentation. Counts are
process-wide and include clients and runtime activity, not just server work.
Parallel clients contribute a fixed startup count (normally `GOMAXPROCS`),
which is divided by `b.N`; do not interpret that as per-request server spawning.
Round-trip `setup-goroutines` counts server startup and the validated preflight
separately. Cleanup is outside both intervals. The `workers` metric describes
Turbo's configured worker count; stdlib does not use that worker pool.

The [second profiling round](../profiling/2026-10-28/README.md) includes CPU,
allocation, blocking, heap and trace evidence, plus a fresh baseline and
allocation priorities. In the sampled workloads Turbo creates no per-request
goroutines; stdlib creates two, for connection serving and background reads.
Live goroutine snapshots alone cannot measure this difference.

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

## Comparison on 2026-10-28

Current production code is `c9533b0fa6efa5e4627bda147e435a871d953ad9`;
TODO tasks 7 and 8 retain that implementation. Go 1.27.2, Darwin arm64,
Apple M3, `GOMAXPROCS=8`; Turbo uses 32 workers and a 1,024-slot queue.
Serial has one client and parallel has eight. Both servers use the existing
identical handlers, requests and client validation, with a new connection per
request and required EOF. All TCP/Pipe contract checks passed.

Ten samples per case alternate which implementation runs first in each round.
TCP uses bounded 200-request samples and Pipe uses 200 ms samples, without
race instrumentation or profiling. All 560 samples complete without request
errors. Values below are medians; parallel time/op is inverse aggregate
throughput, not individual request latency. Byte/object counts include shared
client and connection work, and short TCP samples include worker warmup.

Turbo's Pipe time/op is 29.20–56.11% lower across all workloads (`p<0.001`).
SmallGET improves 56.11% serial and 41.58% parallel, with 29 versus 58 objects
per operation in both modes. **Every TCP timing difference is uncertain**
(`p>=0.089`), including SmallGET's lower medians and HEAD parallel's +17.16%
median. No TCP speedup is established. Turbo's median allocated bytes and
objects are lower in every sampled case; SmallGET TCP serial uses 3,231 versus
6,077.5 B/op and 45 versus 75 objects/op. These are current implementation
comparisons, with no production optimization introduced by this evaluation.

| Transport / workload / clients | Turbo µs/op | Stdlib µs/op | Turbo time change | p | Turbo B/op | Stdlib B/op | Turbo allocs/op | Stdlib allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| TCP / SmallGET / 1 | 72.367 | 79.995 | -9.54% (uncertain) | 0.089 | 3231 | 6077.5 | 45 | 75 |
| TCP / SmallGET / 8 | 29.748 | 35.098 | -15.24% (uncertain) | 0.315 | 3755 | 6866.5 | 46 | 76 |
| TCP / ImplicitGET / 1 | 71.929 | 75.499 | -4.73% (uncertain) | 0.353 | 3081.5 | 5230.5 | 41 | 69 |
| TCP / ImplicitGET / 8 | 30.118 | 32.505 | -7.34% (uncertain) | 0.853 | 3609.5 | 5909 | 41 | 70 |
| TCP / LargeGET / 1 | 85.950 | 83.469 | +2.97% (uncertain) | 0.579 | 3144 | 6064 | 45 | 75 |
| TCP / LargeGET / 8 | 36.365 | 37.630 | -3.36% (uncertain) | 0.684 | 3698 | 6753 | 45 | 76 |
| TCP / POST / 1 | 70.439 | 78.142 | -9.86% (uncertain) | 0.247 | 4071.5 | 6125 | 55 | 79 |
| TCP / POST / 8 | 33.367 | 32.097 | +3.96% (uncertain) | 0.529 | 4483.5 | 6764.5 | 56 | 79 |
| TCP / HEAD / 1 | 70.969 | 73.421 | -3.34% (uncertain) | 0.280 | 2871 | 5761.5 | 43 | 73 |
| TCP / HEAD / 8 | 35.160 | 30.010 | +17.16% (uncertain) | 0.579 | 3183 | 6269.5 | 43 | 73 |
| TCP / Empty / 1 | 73.528 | 75.224 | -2.25% (uncertain) | 0.315 | 2775 | 5675.5 | 40 | 70 |
| TCP / Empty / 8 | 29.549 | 34.141 | -13.45% (uncertain) | 0.247 | 3086.5 | 6169.5 | 40 | 70 |
| TCP / StreamGET / 1 | 74.488 | 81.550 | -8.66% (uncertain) | 0.280 | 3223 | 6004 | 45 | 75 |
| TCP / StreamGET / 8 | 33.349 | 33.367 | -0.06% (uncertain) | 0.971 | 3750.5 | 6682 | 45 | 75 |
| Pipe / SmallGET / 1 | 4.701 | 10.710 | -56.11% | <0.001 | 2576 | 5697 | 29 | 58 |
| Pipe / SmallGET / 8 | 1.927 | 3.297 | -41.58% | <0.001 | 2573 | 5720 | 29 | 58 |
| Pipe / ImplicitGET / 1 | 5.111 | 10.394 | -50.83% | <0.001 | 2506.5 | 4883.5 | 25 | 52 |
| Pipe / ImplicitGET / 8 | 1.996 | 3.212 | -37.85% | <0.001 | 2516 | 4898 | 25 | 52 |
| Pipe / LargeGET / 1 | 10.773 | 18.021 | -40.22% | <0.001 | 2573.5 | 5707.5 | 29 | 58 |
| Pipe / LargeGET / 8 | 3.038 | 4.290 | -29.20% | <0.001 | 2587 | 5752 | 29 | 58 |
| Pipe / POST / 1 | 5.217 | 10.729 | -51.37% | <0.001 | 3587 | 5827.5 | 40 | 62 |
| Pipe / POST / 8 | 2.145 | 3.389 | -36.70% | <0.001 | 3585 | 5854 | 40 | 62 |
| Pipe / HEAD / 1 | 4.705 | 10.485 | -55.13% | <0.001 | 2458 | 5566 | 27 | 56 |
| Pipe / HEAD / 8 | 1.890 | 3.260 | -42.03% | <0.001 | 2466 | 5607.5 | 27 | 56 |
| Pipe / Empty / 1 | 4.590 | 10.299 | -55.44% | <0.001 | 2400 | 5486 | 24 | 53 |
| Pipe / Empty / 8 | 1.831 | 3.205 | -42.85% | <0.001 | 2406.5 | 5529 | 24 | 53 |
| Pipe / StreamGET / 1 | 6.075 | 10.601 | -42.70% | <0.001 | 2574 | 5701.5 | 29 | 58 |
| Pipe / StreamGET / 8 | 2.349 | 3.359 | -30.07% | <0.001 | 2587 | 5720.5 | 29 | 58 |

Raw samples, normalized inputs, benchstat reports, the collector, contract log,
and source/binary hashes are locally retained in
[`stdlib-2026-10-28/`](stdlib-2026-10-28/). That directory is ignored under the
existing `/benchmarks/*` rule; the complete medians and timing significance
are preserved above. The prior race suite passed after task 8; this comparison
changes documentation only.

To reproduce the balanced matrix on a fresh checkout:

```sh
GOMAXPROCS=8 go test -c -o /tmp/turbo-stdlib.test .
GOMAXPROCS=8 /tmp/turbo-stdlib.test -test.run '^TestHTTP11BenchmarkContract$' -test.timeout=1m
python3 - <<'PYBENCH'
import os
import subprocess

env = dict(os.environ, GOMAXPROCS="8")
for transport, duration in (("TCP", "200x"), ("Pipe", "200ms")):
    with open(f"/tmp/http11-{transport.lower()}-raw.txt", "w") as output:
        for sample in range(10):
            order = ("Stdlib", "Turbo") if sample % 2 == 0 else ("Turbo", "Stdlib")
            for implementation in order:
                benchmark = (
                    f"^BenchmarkHTTP11NoKeepAlive$/^{transport}$/"
                    f".*/.*/^{implementation}$"
                )
                subprocess.run([
                    "/tmp/turbo-stdlib.test", "-test.run=^$",
                    "-test.bench=" + benchmark, "-test.benchmem",
                    "-test.benchtime=" + duration, "-test.timeout=1m",
                ], env=env, stdout=output, stderr=subprocess.STDOUT, check=True)
PYBENCH
for transport in tcp pipe; do
    sed -n 's@/Stdlib-@-@p' "/tmp/http11-$transport-raw.txt" > "/tmp/http11-$transport-stdlib.txt"
    sed -n 's@/Turbo-@-@p' "/tmp/http11-$transport-raw.txt" > "/tmp/http11-$transport-turbo.txt"
    benchstat "/tmp/http11-$transport-stdlib.txt" "/tmp/http11-$transport-turbo.txt"
done
```
