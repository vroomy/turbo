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

### 2. [ ] P1 — Combine response headers and the first body write

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

### 3. [ ] P1 — Cache formatted HTTP Date at one-second resolution

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

### 4. [ ] P1 — Measure and tune worker dispatch, and diagnose sustained TCP timeouts

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

### 5. [ ] P1 — Reduce repeated work in the GET/HEAD header parser

**Entry points:** `request/request.go`: `parseHeaders`, `validToken`,
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

### 6. [ ] P2 — Reduce response header-map and serialization work

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

### 7. [ ] P2 — Extend storage reuse to a narrowly defined POST fast path

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

### 8. [ ] P3 — Evaluate worker-owned readers and redundant reset costs

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
