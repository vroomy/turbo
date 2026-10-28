# Turbo optimization TODO

Profiled on 2026-10-24 at commit `52a75f80c31b9d2ca3a7eb36e10685d37369687b`,
Go 1.27.2, Darwin arm64, Apple M3, `GOMAXPROCS=8`. No production or benchmark
code was changed for this baseline. `go test -race ./...` passed.

The target remains HTTP/1.1 with **one request per connection**. Keep-alive,
TLS, and HTTP/2 changes would be separate projects. Tasks below are experiments,
not promises of speedups; keep changes only when correctness and measurements
support them. Each task includes its entry points and acceptance criteria so
another agent can implement it without this conversation.

## What the current profiles say

Uninstrumented SmallGET medians; parallel ns/op is inverse aggregate throughput,
not request latency:

| Case | Turbo time/op | Stdlib time/op | Turbo B/op | Stdlib B/op | Turbo allocs/op | Stdlib allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| TCP serial | 58.35 µs | 63.55 µs | 3,182.5 | 6,025 | 46 | 75 |
| TCP parallel, 8 clients | 23.77 µs | 24.23 µs | 3,691 | 6,632 | 46 | 75 |
| Pipe serial | 5.417 µs | 11.086 µs | 2,724 | 5,885 | 30 | 58 |
| Pipe parallel, 8 clients | 2.052 µs | 3.057 µs | 2,753 | 5,730 | 30 | 58 |

TCP serial takes 8.2% less time with Turbo (`benchstat p=0.002`, n=10).
The TCP parallel difference is **not statistically significant** (`p=0.579`).
Pipe serial takes 51.1% less time; Pipe parallel takes 32.9% less time
(`p=0.002`, n=6 for both). Pipe includes client parsing, allocations, deadlines,
and synchronization, so it is not a server-only measurement.

`BenchmarkTurboProcess/SmallGET` isolates parsing, handler execution, response
serialization, and cleanup: **887 ns/op, 132 B/op, 3 allocs/op**. POST is
**1,224 ns/op, 1,139 B/op, 14 allocs/op**. The connection in this benchmark
discards writes and has no socket/dispatch costs. Allocation counts are rounded
down by Go's benchmark reporting; GET also allocates a header block roughly
once per 15 requests. Its real average is slightly above three objects.

The allocation reduction is real, but allocations are only part of the work:

- **TCP CPU:** Turbo serial samples are dominated by `syscall.rawsyscalln`
  (56.5%), `runtime.kevent` (26.9%), and runtime thread wait/wakeup functions.
  Stdlib shows the same broad pattern (50.8% and 24.8% for the first two).
  Both sides still dial, accept, read, write, and close a connection per request.
  These are sampled stack shares on this OS, not an exact wall-time breakdown.
  The 887 ns isolated benchmark cannot be subtracted directly from TCP latency.
- **Server-only CPU:** cumulative shares include `Parser.Parse` 27.6%,
  `parseHeaders` 17.5%, `populateHeaders` 18.9%, `time.Time.Format` 8.4%, and
  `http.Header.writeSubset` 11.7%. Nested cumulative percentages overlap.
  The Date expression in `populateHeaders` accounts for 440 ms of 3.59 s
  sampled, including time lookup, formatting, and setting the header.
- **Blocking:** Pipe parallel records 782.5 ms waiting to receive from the
  worker queue, 68.6% of 1.141 s total cumulative blocked goroutine time.
  TCP parallel similarly attributes 83.5% to worker queue receives.
  With 32 workers and only 8 clients, this largely represents idle workers,
  **not evidence that the queue is full or that a lock needs replacing**.
  In the 128-client Pipe run, 92.0% of blocked time is in `selectgo`, with
  substantial client dial/request-write and server read stacks. The queue has
  1,024 slots, so this run does not test a full queue either.
  Go block profiles measure synchronization waits, not all socket/netpoll waits;
  use execution traces to attribute network and runnable delays.
- **GET allocations:** of the isolated process's allocation bytes, about
  68 B/request comes from immutable request-header blocks, 32 B from Date
  formatting, and 32 B from the benchmark handler's two `Header.Set` calls.
  Whole-profile shares including setup are 47.7% `ownHeader`, 22.3% Date, and
  22.4% `MIMEHeader.Set`. Do not remove handler work to claim a server gain.
- **POST allocations:** the `http.ReadRequest` fallback accounts for 93.1%
  of allocation bytes. Header maps, requests, URLs, and body readers dominate.
- **Harness memory:** in Turbo's full Pipe allocation profile, `net.Pipe`
  accounts for 50.4% of allocated bytes, response parsing 29.0%, and setting
  client deadlines 14.2%. Server `process` accounts for only 5.3%.
  Both implementations retain approximately **25 MiB** after 20,000 requests,
  mostly pipes and deadline timers; isolated GET retains only about 44 KiB
  including runtime/testing/pools. Local Go source (`net/pipe.go`) shows
  `SetDeadline` uses `time.AfterFunc`, while `Close` only closes a channel.
  Timer-driven retention is the leading explanation, not evidence of a Turbo
  server leak. Verify with a post-expiry heap snapshot in task 1.

The actual benchmark uses **32 workers and a 1,024-slot queue**, hard-coded in
`startHTTP11BenchmarkServer`. The existing `benchmarks/README.md` incorrectly
says both are `GOMAXPROCS`; `-cpu` currently changes clients/CPU capacity only.
Older allocation reports also predate parser/header reuse and are not current.

## Evidence and reproduction

Local artifacts are in [`benchmarks/current-2026-10-24/`](benchmarks/current-2026-10-24/):
`collect.py`, `summarize.py`, the matching `turbo.test`, raw `timing-*.txt`,
`timing-summary.json`, `pipe-comparison.txt`, `tcp-comparison.txt`, and
23 successful `.pprof` files with text summaries. This directory and profile
binaries are ignored by the existing `.gitignore`; the essential findings and
reproduction commands are therefore included here for agents on fresh checkouts.

Timing runs were sequential, without profiling/race instrumentation:

```sh
GOMAXPROCS=8 go test -run '^$' -bench '^(BenchmarkTurboProcess|BenchmarkResponseWriter)$' -benchmem -benchtime=200ms -count=6 .
GOMAXPROCS=8 go test -run '^$' -bench '^BenchmarkHTTP11NoKeepAlive$/^Pipe$/' -benchmem -benchtime=200ms -count=6 .
GOMAXPROCS=8 go test -run '^$' -bench '^BenchmarkHTTP11NoKeepAlive$/^TCP$/^SmallGET$/' -benchmem -benchtime=200x -count=10 .
GOMAXPROCS=8 go test -run '^$' -bench '^BenchmarkHTTP11Accept$' -benchmem -benchtime=200x -count=6 .
```

The recorded runs invoke a compiled test binary with equivalent `-test.*`
flags. Fixed-count TCP samples include more setup/pool warmup per request than
the longer Pipe runs. Do not compare their allocation counts as equivalent
steady-state measurements. Do not run benchmarks concurrently with other work.

Collect each profile separately from timing and from other profile types:

```sh
mkdir -p /tmp/turbo-profile
go test -c -o /tmp/turbo-profile/turbo.test .
GOMAXPROCS=8 /tmp/turbo-profile/turbo.test -test.run '^$' -test.bench '^BenchmarkTurboProcess$/^SmallGET$' -test.benchtime=3s -test.cpuprofile=/tmp/turbo-profile/get-cpu.pprof
GOMAXPROCS=8 /tmp/turbo-profile/turbo.test -test.run '^$' -test.bench '^BenchmarkTurboProcess$/^SmallGET$' -test.benchtime=20000x -test.memprofilerate=1 -test.memprofile=/tmp/turbo-profile/get-memory.pprof
GOMAXPROCS=8 /tmp/turbo-profile/turbo.test -test.run '^$' -test.bench '^BenchmarkHTTP11NoKeepAlive$/^Pipe$/^SmallGET$/^Parallel$/^Turbo$' -test.benchtime=10000x -test.blockprofilerate=1 -test.blockprofile=/tmp/turbo-profile/pipe-block.pprof
go tool pprof -top /tmp/turbo-profile/turbo.test /tmp/turbo-profile/get-cpu.pprof
go tool pprof -top -alloc_space /tmp/turbo-profile/turbo.test /tmp/turbo-profile/get-memory.pprof
go tool pprof -top -alloc_objects /tmp/turbo-profile/turbo.test /tmp/turbo-profile/get-memory.pprof
go tool pprof -top -inuse_space /tmp/turbo-profile/turbo.test /tmp/turbo-profile/get-memory.pprof
go tool pprof -top -sample_index=delay /tmp/turbo-profile/turbo.test /tmp/turbo-profile/pipe-block.pprof
go tool pprof -top -sample_index=contentions /tmp/turbo-profile/turbo.test /tmp/turbo-profile/pipe-block.pprof
```

Coverage collected: CPU for isolated GET/POST and SmallGET across
Pipe/TCP × serial/parallel × Turbo/Stdlib; full-allocation sampling for isolated
GET/POST and Pipe serial Turbo/Stdlib; blocking for all eight round-trip cases
plus Pipe `BenchmarkHTTP11Accept/Parallel16` (128 clients).
CPU runs used 3 s, except TCP parallel retries used `10000x -count=3`.
Block runs used 10,000 requests for Pipe and 2,000 for TCP; memory runs used
20,000 requests. Profiles include calibration/setup/cleanup, unlike benchmark
timers. Raw cumulative totals cannot be compared across different run lengths.

A 3 s TCP parallel Turbo CPU run failed with eight read timeouts after longer
serial TCP runs. Its log is `cpu-tcp-parallel-turbo-failed.txt`, and its profile
has suffix `.pprof.failed`; neither is used for conclusions. Bounded retries
passed. This does not establish whether TCP churn, scheduling, or a server
problem caused the failure. Investigate it in task 4; do not silently increase
timeouts or claim all sustained-load runs passed.

## Tasks

### 1. [x] P0 — Remove Pipe deadline retention from the measurement harness

**Completed 2026-10-24.** Pipe now owns one 10 s watchdog per fresh connection,
starting before the listener handoff. Closing the connection stops its timer
and joins an already-running callback; successful requests create no watchdog
goroutine. Expiry closes the Pipe and returns a timeout error, including during
dial or the required EOF check. TCP retains its 5 s dial timeout and native
10 s I/O deadline. Error cleanup closes the connection before closing an
unread response body, then releases the response reader's connection reference.
The watchdog costs a wrapper, channel, callback and timer (about 288 B and four
objects per connection), replacing the old Pipe deadline timers/closures.

Fresh before/after runs used this document's base commit/toolchain/CPU,
32 workers, queue 1,024, and 1/8 clients at `GOMAXPROCS=8`. After 20,000 GETs,
forced-GC live heaps with the servers still running changed from **25.528 to
0.441 MiB (Turbo)** and **25.658 to 0.462 MiB (stdlib)**. Stopping the old servers
left 25.420/25.654 MiB with only two goroutines, separating the retention from
worker storage. After deadline expiry these fell to 1.017/1.239 MiB and the
Pipe allocations disappeared from the heap profiles; residual runtime timer
and callback storage remained. Revised heaps after shutdown were 0.333/0.456 MiB
and stayed at approximately those levels after expiry. Turbo idle worker/queue
allocation stacks are tens of KiB, not the approximately 25 MiB of old Pipe/timer
retention. Full allocation-byte/object profiles were collected separately.

The full Pipe timing matrix (six workloads × serial/parallel × both servers,
six 200 ms samples each) is retained alongside the original samples. SmallGET
serial medians changed from 5.490 to 5.017 µs for Turbo and 11.126 to 10.528 µs
for stdlib (`p=0.002` each); parallel changes were not significant. SmallGET
remains 30/58 allocs/op. Some parallel cases regress: Turbo LargeGET by 3.2%,
and stdlib ImplicitGET/LargeGET/POST/Empty by 6.2–10.7%. Several B/op values rise.
These mixed results establish a **new harness baseline**, not a production
Turbo optimization. Use the same revised harness for future comparisons.

`go test -race -count=1 ./... -timeout=2m` passed, including TCP/Pipe contract
coverage. New tests exercise stalled dial/write/header/body/EOF, malformed and
rejected responses, error cleanup, listener closure, and concurrent close/expiry.
Detailed costs, all timing/B/op/allocation samples, benchstat, heap counters,
profile summaries, binary hashes and collection scripts are in
[`testdata/pipe-timeouts/`](testdata/pipe-timeouts/README.md). That directory and
binary profiles are locally retained and ignored; essential results are above.
The original `benchmarks/current-2026-10-24/` baseline was preserved.

The opt-in `TestHTTP11PipeHeap` diagnostic is in the source tree. It collects
initial/idle/completed/stopped/post-expiry snapshots, with two forced GCs per
snapshot. Run each implementation in a separate process (use a separate output
directory and change `Turbo` to `Stdlib` for the second run):

```sh
TURBO_PIPE_PROFILE_DIR=/tmp/turbo-pipe-heap TURBO_PIPE_PROFILE_SERVER=Turbo GOMAXPROCS=8 go test -run '^TestHTTP11PipeHeap$' -memprofilerate=1 -timeout=1m .
GOMAXPROCS=8 go test -run '^$' -bench '^BenchmarkHTTP11NoKeepAlive$/^Pipe$/' -benchmem -benchtime=200ms -count=6 .
```

**Entry points:** `http11BenchmarkClient.roundTrip`,
`newHTTP11BenchmarkListener`, `http11PipeListener` in `http11_benchmark_test.go`;
the local Go toolchain's `src/net/pipe.go`.

**Work:** reproduce the approximately 25 MiB retained heap in both servers,
then take a forced-GC snapshot after the 10 s deadlines expire. Attribute
timer/pipe retention separately from worker storage. Introduce a bounded Pipe
timeout/watchdog mechanism whose timers and goroutines stop on completion and
failure, while preserving fresh connections, EOF checks, and identical client
work for both servers. A deferred `SetDeadline(time.Time{})` after EOF is not a
sufficient fix: `net.Pipe` rejects deadline changes after either end closes.
Keep TCP and Pipe timeout implementations explicit and document their costs.

**Validation/done:** exercise normal completion, stalled peers, and errors;
show before/after retained heaps and allocation profiles, including after
deadline expiry. Rerun the full Pipe timing matrix and retain the original
baseline. Label improvements as harness changes, not Turbo optimizations.
Preserve `TestHTTP11BenchmarkContract` and bounded test completion.

**Dependencies:** none. Establish the revised Pipe baseline before judging
the end-to-end effect of other tasks. Server-only work can use this baseline
independently. Coordinate benchmark-file edits with task 4.

### 2. [x] P1 — Combine response headers and the first body write

**Completed 2026-10-24.** First body writes are appended to the worker's
serialized header buffer when the total is at most 4 KiB, then sent
immediately. Later writes, oversized first writes/headers, HEAD, and bodyless
statuses keep their existing paths. A 64 KiB body is never copied into the
buffer. The existing 16 KiB retention limit/reset remains; no new fields or
handler-return buffering were introduced. Counting connections verify one
write instead of two for SmallGET/ImplicitGET/POST and four instead of five
for the new four-chunk StreamGET workload. Partial writes count body bytes
only and preserve sticky errors. Go's local `net.Buffers` implementation uses
`writev` for TCP but separate writes for Pipe; bounded copying supports both.

Before/after measurements use base commit `f8a9868`, Go 1.27.2, Darwin arm64,
Apple M3, `GOMAXPROCS=8`, 32 workers, queue 1,024, and 1/8 clients, with task 1's
revised Pipe harness identical in both binaries. Both an initial sequential
matrix and an alternating-order confirmation used ten samples per case:
200 ms for isolated/Pipe and 200 requests for bounded TCP. Confirmation Pipe
SmallGET serial changes from **5.177 to 5.060 µs** (-2.27%, `p=0.001`),
ImplicitGET serial/parallel improves 1.79/5.02%, POST 9.67/5.83%, and StreamGET
6.09/4.60%. These gains also appeared in the initial matrix. SmallGET parallel
confirmation is uncertain (`p=0.052`); LargeGET, HEAD, Empty, and all isolated
timing changes are not significant. Small initial isolated regressions and
0.94/1.00% Pipe LargeGET/HEAD serial regressions did not reproduce as
significant differences in the alternating run.

Initial TCP SmallGET gains of 4.74/5.51% did not repeat significantly in the
noisy confirmation matrix; **no TCP speedup is established**. Initial TCP
HEAD/Empty regressions also did not reproduce significantly. Isolated
SmallGET remains **132 B/op, 3 allocs/op**, with no isolated allocation changes
for any workload. Pipe object counts are unchanged. Short TCP samples include
larger worker-buffer warmup costs: SmallGET serial rises 62 B/op (+1.94%) and
parallel reports 46 to 47 allocs/op; all costs and uncertain results are
retained. Acceptance rests on repeated Pipe round-trip gains, verified prompt
streaming, and no demonstrated material large-body regression.

`go test -race -count=1 ./... -timeout=2m` passed, including committed headers,
informational/bodyless responses, content sniffing, length enforcement,
partial-write boundaries, reset bounds, and gated streaming over TCP/Pipe.
Tracked raw samples, all ns/op/B/op/object medians, benchstat, metadata,
profile summaries, and reproduction scripts are in
[`performance/response-coalescing/`](performance/response-coalescing/README.md).

**Entry points:** `response_writer.go`: `Write`, `writeHeader`, `writeBody`,
`recordWriteResult`, `reset`; `Turbo.process` in `turbo.go`.

**Evidence:** nonempty responses currently call `net.Conn.Write` once for
headers and again for body. Stdlib buffers small responses. TCP samples include
both write stacks; each `net.Pipe.Write` also requires synchronous exchanges.

**Work:** prototype bounded coalescing of serialized headers with a small first
body chunk, using worker-owned storage. Compare with `net.Buffers` for TCP if
appropriate; check the actual connection type's implementation rather than
assuming scatter/gather gives one syscall for every `net.Conn`. Avoid copying
entire 64 KiB bodies. A first-write optimization should retain prompt streaming;
buffering until handler return needs a separate behavioral decision.

**Validation/done:** add a counting connection to verify fewer underlying writes
for SmallGET and correct wire bytes. Preserve committed-header snapshots,
informational responses, HEAD, 204/304, content sniffing, empty handlers,
Content-Length enforcement, sticky errors, and partial writes spanning the
header/body boundary. `Write` must report body bytes, not header bytes.
Keep buffer retention bounded and reset all new fields. Run response-writer and
process tests plus race tests; compare SmallGET, ImplicitGET, LargeGET, HEAD,
POST, and multi-write streaming in isolated, Pipe, and bounded TCP benchmarks.
Accept only with a repeatable end-to-end gain and no material streaming or
large-body regression.

**Dependencies:** none for implementation; use task 1's revised Pipe baseline
when available. Coordinate `response_writer.go` edits with tasks 3 and 6.

### 3. [x] P1 — Cache formatted HTTP Date at one-second resolution

**Completed 2026-10-25.** Each response writer retains a Unix second and an
immutable formatted Date string across `reset`. `populateHeaders` still reads
the clock on every response, but formats only on first use or when the second
differs, including backward changes. The empty-string sentinel handles Unix
second zero. The cache adds 24 bytes per worker on arm64, with no shared state,
locks, ticker, or background goroutine. Handler Date overrides and nil/empty
suppression still apply to the current response and are restored to the cached
default on reuse. Header value slices remain independently owned.

Measurements compare base commit `45364e7d1c6c0b1cf71211d5f2b8ae4bfb0d8a8d`
against this change with the identical harness, Go 1.27.2, Darwin arm64,
Apple M3, `GOMAXPROCS=8`, 32 workers, queue 1,024, and 1/8 clients. Ten
alternating-order samples per case use 200 ms for isolated/Pipe and 200
requests for bounded TCP. SmallGET isolated improves **918.25 to 791 ns/op**
(-13.86%, `p<0.001`), **132 to 100 B/op**, and **3 to 2 allocs/op**. All seven
isolated workloads save 32 B and one reported object; their time improves
8.00–13.86%. ResponseWriter final/informational improves 17.26/15.44% and
48 to 16 B/op. Rounded object counts do not imply allocation-free requests.

Pipe SmallGET serial/parallel improves **2.54/1.74%** (`p=0.017/<0.001`),
with one fewer reported object in every Pipe case. Other significant Pipe
gains range from 1.76–3.66%; ImplicitGET, LargeGET parallel, POST parallel,
and StreamGET serial are uncertain. There are no significant timing
regressions in the matrix. All TCP timing differences are uncertain,
including HEAD/Empty parallel increases of 4.88/9.54%; no TCP speedup is
established. Short TCP samples include per-worker cache initialization and
other warmup costs. All timing/profile TCP requests completed without errors.

Separate 20,000-request allocation profiles reduce `time.Time.Format` from
20,003 allocations (625.09 KiB) to two (64 bytes, setup), confirming that
formatting no longer allocates per request. CPU profiles cover isolated GET
and full SmallGET round trips over Pipe/TCP, serial/parallel. The isolated
Date expression's sampled share drops from 11.33% to 5.29%, while clock and
map work remain; full round-trip profiles are dominated by synchronization
and system calls. `go test -race -count=1 ./... -timeout=2m` passed with
loopback enabled. Deterministic helper tests cover same-second hits, rollover,
backward time, epoch initialization, UTC output, immutable prior values, and
reset reuse; parallel override tests cover suppression and committed headers.
Raw samples, benchstat, metadata, profile summaries, and reproduction scripts
are tracked in [`performance/http-date-cache/`](performance/http-date-cache/README.md).

**Entry points:** `responseWriter.populateHeaders`, `populateHeader`, `reset`
in `response_writer.go`; `BenchmarkResponseWriter` and `BenchmarkTurboProcess`.

**Evidence:** Date formatting costs 32 B/request, one allocation, and 8.4% of
the isolated GET CPU samples; the full Date expression accounts for 12.3%.
The same formatted string is recomputed even when the second has not changed.

**Work:** prototype a worker-local cached Unix second and immutable formatted
string, retaining that cache across `reset`. Compare to an immutable shared
cache only if measurements justify its synchronization cost. Reformat when
the second differs, including backward clock changes. Avoid a background
goroutine/ticker unless lifecycle and contention are justified.

**Validation/done:** use a controllable clock/helper to test repeated calls
within a second, rollover, backward time, and worker reuse. Preserve handler
overrides and nil Date suppression, and ensure an override on one response
does not affect the next. Run parallel race checks. Demonstrate removal of the
per-request Format allocation and compare isolated plus full round-trip CPU
and timings. Expect roughly 32 B/request savings; any TCP speedup may be small.

**Dependencies:** independent of task 2's write strategy; coordinate edits.

### 4. [x] P1 — Measure and tune worker dispatch, and diagnose sustained TCP timeouts

**Completed 2026-10-28, with no production dispatch change.**
`BenchmarkHTTP11Accept` now independently names workers 1/8/32, queue capacity
0/8/32/1,024 and clients 1/8/32/128. The full TCP/Pipe sweep has six samples per
configuration at `GOMAXPROCS=8`, plus selected fixed-client `-cpu=1,4,8` cases.
The same revised Pipe harness and compiled binary are used throughout,
at base commit `ca70fcb42db2182812708350c8a3546bce8809e1`, Go 1.27.2,
Darwin arm64, Apple M3. Documentation now explains the independent parameters,
legacy client scaling, timeout costs and throughput/latency distinction.

An eight-worker/unbuffered candidate initially improves Pipe at 128 clients
by 5.73% (`p=0.002`, n=6), but its benefit does not repeat in an alternating
confirmation: Pipe at 8/128 clients has `p=1.000/0.937`, TCP `p=0.132/0.818`.
One worker materially regresses concurrent Pipe throughput. Instrumented
latency/live-heap tables cover all 96 configurations separately from timing;
36 slow-client/handler runs demonstrate saturation and its costs. Pipe slow
handlers with 128 clients and queue 1,024 reach p95 142.84/17.56/4.29 ms at
1/8/32 workers. Fewer workers save idle memory but reduce blocking-handler
capacity; no broadly justified configuration or dispatch replacement emerged.

Five short traces distinguish empty receives, blocked sends, runnable delay,
accept and network wait. With 32 workers/eight clients the sampled queue stays
empty and receives account for 81.21 ms of 100.39 ms synchronization wait.
With one worker/eight slots/slow handlers, the queue reaches eight and the
listener blocks on sends for 545.78 ms of a 562.97 ms load interval. These
cumulative trace delays are not wall-time percentages or latency estimates.

The sustained TCP timeout reproduces in **both implementations**. Three
rounds reverse run order and test a 15 s pause, retaining failed CPU-profile
retries and diagnostic state. All six bounded 10,000-request runs pass;
three of six 3 s and all six 10 s diagnostic runs fail, totaling 72 EOF read
timeouts after successful response validation. Failure captures show all
accepted connections already closed, empty Turbo queues, idle workers,
server sockets in `FIN_WAIT_1` and clients still `ESTABLISHED`. This locates
the reproduced stall at TCP teardown, not pending dispatch. Port exhaustion
and the precise kernel/packet mechanism remain unproven. Separate 35 s
cooldowns without preceding serial churn allow both 3 s and both 10 s runs
to pass; churn history matters, but sustained reliability is not established.
No deadlines, EOF checks or socket settings were weakened.

`go test -race -count=1 ./... -timeout=2m` passes, including existing worker,
close, protocol and lifecycle tests. New deterministic full-queue coverage
checks blocked-send shutdown, drain, malformed work and exactly-once close;
the opt-in study also runs under race detection. Reproduction scripts, raw
samples, benchstat, all configuration/latency/memory tables, trace summaries,
request/connection counters and preserved failure captures are tracked in
[`performance/worker-dispatch/`](performance/worker-dispatch/README.md).

**Entry points:** `startHTTP11BenchmarkServer`, `BenchmarkHTTP11Accept`,
`benchmarkHTTP11` in `http11_benchmark_test.go`; `newTurbo`, `listen`, `work`,
`Close` in `turbo.go`. Current setup: 32 workers, queue 1,024.

**Work:** expose workers, queue capacity, and client count as independently
named benchmark parameters. Sweep workers 1/8/32, queue 0/8/32/1,024, and
clients 1/8/32/128 at fixed `GOMAXPROCS`, then selected `-cpu=1,4,8` cases.
Correct benchmark documentation. Use short traces to distinguish empty-queue
receives, blocked sends, runnable delay, accept, and network wait. Add slow
clients/handlers and a deliberately full queue to measure backpressure and
tail latency. Current `ns/op` is throughput and cannot provide p95 latency.

Reproduce the failed 3 s TCP parallel profile with bounded and sustained runs
for both implementations, recording connection counts, errors, and socket/
goroutine state on failure. Test run order/cooldown as possible factors; do
not assume port exhaustion from a read timeout alone. Keep failure evidence.

Only after this evidence, compare fewer workers, worker-local readers, or a
dispatch alternative. A larger queue or more workers is not automatically
faster. The high receive-side block percentage by itself is not a bottleneck.

**Validation/done:** provide a throughput/latency/memory configuration table,
a trace-supported diagnosis, and either a measured configuration/dispatch
improvement or an explicit no-change conclusion. Preserve graceful queue
drain, exactly-once connection close, malformed-request handling, and
`TestTurboWorkers`/`TestTurboClose`. Run race tests for any code change.

**Dependencies:** coordinate benchmark infrastructure with task 1; rebaseline
after its harness changes. Avoid changing public defaults from one microbenchmark.

### 5. [x] P1 — Reduce repeated work in the GET/HEAD header parser

**Completed 2026-10-28.** Canonical/lowercase Host, Connection, and
Content-Length names use validated constants. General canonical names combine
token validation and casing checks in one scan; uncommon casing retains the
standard canonicalizer after validation. Host avoids a redundant map lookup.
Reused value arrays skip the full newline pre-scan and count remaining fields
only on growth. Growth excludes a pending Host, preserving reuse at the
128-value bound even with Host last. Capped value slices, reset bounds,
immutable 1 KiB string blocks, and fallback without consuming input remain.
No parser fields, shared state, or supported request forms were added.

Measurements compare base commit `214bf58eca07af9b18edb6ba314a7d50d82e5c64`
with identical expanded harnesses, Go 1.27.2, Darwin arm64, Apple M3,
`GOMAXPROCS=8`, 32 workers, queue 1,024, and 1/8 clients. Ten alternating-order
samples use 200 ms for parser/process/Pipe and bounded 200-request TCP runs.
Reused SmallGET parsing improves **361.1 to 290.35 ns (-19.59%)**, lowercase
SmallGET 29.11%, the six-field set 13.39%, a 19-field browser set 9.87%, and
escaped HEAD/browser headers 8.68% (`p<0.001` each). Larger lowercase/mixed-case
sets improve 5.86/1.95%. Every parser/storage benchmark preserves B/op and
objects, including SmallGET's 68 B/op in immutable blocks; its reported zero
objects is rounded down. POST/chunked fallback parser timings are uncertain.

Isolated SmallGET processing improves **834.4 to 762.8 ns (-8.58%)**, staying
at **100 B/op, two objects**. All six original GET/HEAD process cases improve
8.02–9.28%; browser and escaped HEAD/browser processing improve 7.66/6.86%,
with unchanged allocation counts/bytes. The unchanged POST parser path's small
0.75% process decrease does not establish a POST parsing optimization. Pipe
SmallGET/HEAD serial improves 6.38/2.60% (`p<0.001`); parallel changes are
uncertain. Every TCP timing/allocation change is uncertain, including a 3.40%
SmallGET parallel median time increase. No TCP speedup is established. All
final timing runs complete without request errors; the matrix has no significant
timing or allocation regressions.

Separate CPU profiles show `parseHeaders`' cumulative sampled share changing
from 44.64 to 40.79% in reused SmallGET parsing, 42.40 to 35.34% for browser
parsing, and 20.38 to 10.96% for SmallGET processing. Browser processing's
share is approximately unchanged (36.29/36.00%); cumulative shares overlap
and are not exact per-request costs. Full allocation profiles confirm identical
`ownHeader` storage: 1,335 blocks for 20,000 SmallGETs and 20,001 owned strings
for browser headers, including setup. Handler header allocations remain.

`go test -race -count=1 ./... -timeout=2m` passes with loopback access. Both
differential fuzzers pass 30 s runs with eight workers (2,430,840 parse and
2,509,409 reuse cases). New tests cover all 256 bytes at key/value boundaries,
mixed casing and duplicate framing fields, fragmentation and late fallback,
handler appends, clone retention across growth, and retained-capacity limits.
They also pass against the baseline parser. Raw samples, all medians,
benchstat, metadata, profile summaries and reproduction instructions are in
[`request/performance/get-head-parser/`](request/performance/get-head-parser/README.md).

**Entry points:** `request/request.go`: `parseHeaders`, `canonicalHeaderKey`,
`validValue`, `headerStorage`, `ownHeader`, `Parser.Reset`;
`BenchmarkParse`/`BenchmarkOwnHeader` in `request/request_test.go`.

**Evidence:** `parseHeaders` is 17.5% of isolated GET CPU. It counts newlines,
splits fields, validates keys, calls canonicalization (which also validates),
trims values, and performs map lookups for every header.

**Work:** benchmark narrowly scoped changes: a validated common-header path
for Host/Connection/Content-Length, avoiding duplicate token scans, and using
retained value capacity without a full newline pre-scan when possible. Keep a
general fallback for uncommon casing and fields. Measure realistic larger
header sets and escaped paths, not just the three-header benchmark request.

**Validation/done:** preserve exact request/error/body behavior relative to
`http.ReadRequest` through the existing differential tests and both fuzzers.
Cover mixed casing, duplicate Host/Content-Length, invalid tokens/values,
fragmentation, fallback after partially parsed headers, and handler mutation.
Keep header slices capped and owned strings immutable: `Request.Clone` is
shallow for strings and must remain safe across worker reuse. The 1 KiB
header blocks already amortize allocations; never reuse mutable string backing
storage to claim zero allocations. Preserve retained-capacity bounds.
Show parser and process CPU/time improvements without allocation regressions.

**Dependencies:** coordinate parser edits with tasks 7 and 8. Does not require
a change to supported request forms.

### 6. [x] P2 — Reduce response header-map and serialization work

**Completed 2026-10-28.** `populateHeaders` saves the current Connection and
Date slices, clears the existing map in place, and restores defaults through
the existing helper. Matching slices remain reusable; changed or aliased
handler values are replaced without overwriting their backing arrays. There
are no new worker fields or scratch buffers. Header commitment, Date caching,
framing and the 16 KiB serialized-buffer retention bound remain unchanged.

`Header.WriteSubset` is retained. A separate serializer prototype rebuilds
sorted worker-local key/value scratch on every commitment, clears references
immediately, caps scratch at 64 fields and delegates larger maps to stdlib.
Six balanced-order samples compare original, reset-only, serializer-only and
combined binaries. Serialization alone improves isolated SmallGET by 4.07%
(`p=0.026`) but its SmallGET Pipe changes are uncertain. Adding it to map
clearing has no significant difference in any of four sampled Pipe cases
(`p>=0.132`); the extra protocol code and retained storage are not justified.
The prototype passes exact-byte differential tests and the full race suite.

Final measurements compare base commit `0b60ce0beeb0767415bfdf0106128482c7c3f15a`
with identical original/expanded harnesses, Go 1.27.2, Darwin arm64, Apple M3,
`GOMAXPROCS=8`, 32 workers, queue 1,024, and 1/8 clients. Ten alternating-order
samples use 200 ms for isolated/Pipe and bounded 200-request TCP cases. The
original handlers, including both Header.Set allocations, are unchanged.
Isolated SmallGET improves **753.25 to 709.95 ns (-5.75%, `p=0.005`)** and
stays **100 B/op, two objects**. Other original isolated workloads improve
1.88–6.19%, except ImplicitGET's uncertain +0.94%. Adding 8/32/128 response
headers improves isolated time 11.17/5.81/9.56%, with unchanged object counts.

Pipe SmallGET serial improves **4.7125 to 4.5205 µs (-4.07%, `p=0.001`)**;
its parallel difference is uncertain. HEAD/LargeGET serial improve 4.04/1.80%.
With 32 extra response headers, Pipe serial/parallel improves 5.69/3.35%;
128 extra headers improves 6.32/6.25%. Pipe ImplicitGET parallel regresses
**0.87% (`p=0.037`)**. Pipe object counts are unchanged; 32-extra-header serial
B/op rises 9 B (+0.12%) and 128-extra-header serial falls 22 B (-0.08%).
Every TCP timing/allocation change is uncertain, including large parallel
median increases; **no TCP speedup is established**. All final timing runs
complete without errors. This small reset change accepts the recorded
default-only cost in exchange for repeated SmallGET and larger-map gains.

Separate SmallGET CPU profiles attribute 10.70/10.20% cumulative sampled CPU
to `populateHeaders` before/after; 32-extra-header profiles show 10.96/2.07%.
Shares overlap and are not exact request costs. Allocation profiles preserve
the handler's two value-slice allocations. `go test -race -count=1 ./...
-timeout=2m` passes with loopback access. New tests cover all key/value bytes,
invalid names, sanitization, multiple/nil values, exact-key exclusions,
informational/bodyless statuses, small/large maps, retained clones, default
aliasing, map/slice reuse, and the serialized-buffer bound. Existing snapshot,
streaming, lifecycle and framing tests pass.

Raw samples, benchstat reports, all medians, source/binary hashes, prototype
source, profile summaries and reproduction scripts are locally retained in
[`performance/response-headers/`](performance/response-headers/README.md),
ignored by the existing `/performance` rule. Binaries/raw profiles are in
`/tmp/turbo-response-headers`. To reproduce on a fresh checkout, archive the
base commit there, copy `response_headers_benchmark_test.go` into it, and
compile baseline/current tests with `go test -c`. Use `GOMAXPROCS=8`,
`-run '^$' -bench '^(BenchmarkTurboProcess|BenchmarkResponseWriter|BenchmarkTurboProcessResponseHeaders)$' -benchmem -benchtime=200ms -count=10`
for isolated measurements; use
`-bench '^BenchmarkHTTP11NoKeepAlive$/^Pipe$/.*/.*/^Turbo$|^BenchmarkHTTP11ResponseHeaders$/^Pipe$/'`
for Pipe, and replace Pipe with TCP plus `-benchtime=200x` for bounded TCP.
Alternate before/after binary order, and collect profiles separately.

**Entry points:** `response_writer.go`: `populateHeaders`, `prepareHeader`,
`headerExclusions`, `appendFramingHeaders`; local `net/http/header.go`.

**Evidence:** `Header.writeSubset` accounts for 11.7% of isolated GET CPU,
including 7.5% in `sortedKeyValues`. `populateHeaders` also walks/deletes map
entries on every request. Existing buffer reuse has already removed most
serialization allocations, so fewer allocations alone is not the goal.

**Work:** profile after task 3; compare default-header handling and a reusable
serialization scratch structure against `Header.WriteSubset`. Limit scope to
demonstrated hot work. Direct handler access to `http.Header` means the server
cannot assume a previously serialized map is unchanged or intercept `Header.Set`.
Keep the benchmark handler identical; its two value-slice allocations belong
to the handler and must not disappear through benchmark-only changes.

**Validation/done:** preserve invalid-name handling, CR/LF sanitization,
multi-value headers, nil suppression, framing exclusions, informational and
bodyless statuses, and snapshot-at-WriteHeader behavior. Test small and large
maps and bound retained scratch memory. Run all response-writer tests and
compare isolated plus end-to-end timings. Retain `WriteSubset` if a replacement
cannot justify its maintenance and correctness cost with a measurable gain.

**Dependencies:** implement after/rebase onto tasks 2 and 3 to avoid overlapping
writer changes and to profile the remaining bottleneck.

### 7. [x] P2 — Extend storage reuse to a narrowly defined POST fast path

**Completed 2026-10-28 — no improvement retained.** The POST storage-reuse
prototype was evaluated and reverted. Isolated POST gains came with extra
framing/body-lifecycle code and measurable costs on fallback and GET paths;
the tradeoff was not accepted. The existing `http.ReadRequest` POST path is
retained, with no implementation, test, or benchmark changes for this task.
No TCP improvement was established.

**Entry points:** `request.parse`, `parseBuffered`, `Parser` in
`request/request.go`; `Turbo.process`/`finishRequest` in `turbo.go`;
`TestTurboRequestBodyLifecycle`, parser differential tests and fuzzers.

**Evidence:** every POST currently falls back to `http.ReadRequest`; it accounts
for 93.1% of isolated POST allocated bytes. POST remains 14 allocs/op and
1,139 B/op versus GET's approximately 3 and 132.

**Work:** first add focused parser/body lifecycle benchmarks. Prototype reuse
for complete, validated HTTP/1.1 POST headers with a single valid Content-Length
and a reusable bounded body reader. The body must stream beyond bytes already
buffered. Keep chunked encoding, ambiguous/duplicate lengths, upgrades, and
other unsupported forms on the existing stdlib fallback without consuming
input before deciding to fall back. Do not expand scope to a complete HTTP
parser rewrite or change rejection semantics to make the fast path simpler.

**Validation/done:** differential tests must cover zero/large/truncated bodies,
length overflow/signs, Transfer-Encoding precedence, duplicate fields, and
fragmented headers. Preserve original-body cleanup when handlers replace
`req.Body`; close the connection before closing an unread body to avoid hangs.
Preserve `ErrBodyReadAfterClose`, clone/lifetime contracts, and worker reset.
Run parser fuzzers, lifecycle/race tests, and POST process/Pipe/TCP benchmarks.
Accept only if workload gains justify the extra protocol implementation.

**Dependencies:** rebase onto task 5; higher protocol risk than Date or writes.

### 8. [x] P3 — Evaluate worker-owned readers and redundant reset costs

**Completed 2026-10-28 — no production change retained.** Independently
evaluated lazy worker-owned readers and a private parser lifecycle flag that
skips repeated clearing while preserving automatic public reset. Measurements
use base commit `c9533b0fa6efa5e4627bda147e435a871d953ad9`, Go 1.27.2,
Darwin arm64, Apple M3 and `GOMAXPROCS=8`. Identical harnesses compare each
candidate with a pooled-reader/original-reset baseline; a separate unchanged
source control finds no significant timing effect from the common internal
signature adjustment. No combined candidate was measured.

Ten balanced-order samples cover all isolated/Pipe workloads, browser/escaped
headers, bounded TCP GET/HEAD/POST, and task 4's Pipe matrix with workers
1/8/32, queues 0/1024 and clients 1/8/128. Worker-owned readers improve isolated
SmallGET **735.0 to 729.55 ns (-0.74%)**, with **100 B/op and two objects**
unchanged. Initial small LargeGET and high-client round-trip gains do not
repeat significantly in a separate ten-sample confirmation. Confirmation
SmallGET Pipe serial regresses **0.69% (`p=0.037`)**; eight workers/queue 1024/
one client regresses **1.07% (`p=0.007`)**. Every TCP timing difference is
uncertain. Bounded TCP SmallGET serial B/op rises **18.53%**, reflecting
per-worker reader warmup; no TCP improvement is established.

Forced-GC profiles with 1/8/32 workers cover cold idle, all workers active,
warm idle and shutdown. Neither reader strategy allocates buffers before use.
Both own **131 KiB** of readers/buffers with 32 active workers; only the
worker-owned candidate retains those 32 pairs after becoming idle and forcing
two GCs. Both release them at shutdown. The reset flag produces only small
isolated gains in some workloads, uncertain SmallGET/public-parser timings,
and no significant default Pipe round-trip gains. Neither tradeoff is accepted.

The baseline and both prototypes pass `go test -race -count=1 ./...
-timeout=2m`, including lifecycle, failure, retained-clone and concurrent-worker
tests. All three opt-in heap diagnostics pass under race detection. The reset
prototype passes both differential fuzzers for 30 s each (4,067,404 parse and
3,196,824 reuse executions). The final repository race suite also passes.
Reproduction scripts, prototype patches, raw samples, all ns/op/B/op/object
medians, benchstat reports, source/binary hashes, CPU/allocation summaries and
worker heap evidence are locally retained in
[`experiments/worker-lifecycle/`](experiments/worker-lifecycle/README.md).
That directory is ignored by the `/experiments` rule; the essential results
and measurement configuration are recorded above.

**Entry points:** `requestReaders`, `work`, `process`, `finishRequest` in
`turbo.go`; `Parser.Parse` and `Parser.Reset` in `request/request.go`.

**Evidence:** a worker already owns its parser and response writer but gets and
puts a pooled request reader every request. `process` defers `Parser.Reset`,
and the next `Parser.Parse` resets it again. Reset is only 1.7% of isolated GET
CPU, and reader pooling is not a leading hotspot: this is a low-priority
experiment, not a reason to redesign ownership immediately.

**Work:** compare one lazily allocated reader per worker with the global pool,
including fixed resident memory (at least 32 × 4 KiB of read buffers at the
current worker count) and idle-worker costs. Separately benchmark an internal
reset/lifecycle change that avoids duplicate clearing while retaining the
public Parser API's automatic reset and prompt reference release.

**Validation/done:** preserve reset-to-nil reader references, original-body
cleanup ordering, failure paths, and no cross-worker sharing. Keep public
`request.Parse` ownership and retained-clone guarantees. Run lifecycle/race
tests and evaluate both active and idle-worker heap profiles. Land each change
only if it gives a repeatable gain without disproportionate retained memory;
record a no-change outcome if it does not.

**Dependencies:** use task 4's worker matrix; coordinate parser changes with
tasks 5 and 7. Do not combine both experiments in one performance comparison.

## Completion standard for every optimization

Keep each optimization independently reviewable. Record the commit/toolchain,
workload, worker/queue/client configuration, before/after raw samples,
`benchstat` output, and relevant profiles. Use the same harness on both sides
of a production-code comparison. Run `go test -race ./...` for implementation
changes and relevant differential/fuzz tests for parser changes. Do not time
race or full-allocation-profile runs. Report ns/op and B/op as well as objects;
show regressions and uncertain results. Preserve correctness and the documented
request/response lifetime contract even when it prevents an allocation shortcut.
