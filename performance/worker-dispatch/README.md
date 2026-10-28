# Worker dispatch and sustained TCP investigation

TODO task 4 is complete with a **no-change decision for production dispatch**.
The evidence does not justify replacing the channel, changing the benchmark's
32-worker/1,024-slot default, or introducing worker-owned readers. The benchmark
now independently names workers, queue capacity and clients, and the opt-in
study measures latency and memory separately from throughput.

Sustained TCP timeouts reproduced in **both Turbo and stdlib**. Captures locate
the observed stall at connection teardown after successful response reads and
server closes, with no queued Turbo work. A longer cooldown allowed both
implementations to complete sustained runs. The specific kernel/packet cause
remains unproven; neither port exhaustion nor a dispatch defect is established.

## Setup and measurement limits

The production baseline is `ca70fcb42db2182812708350c8a3546bce8809e1`.
Production source is unchanged. All measurements use the same compiled binary,
Go 1.27.2, Darwin arm64 (kernel 25.5.0), Apple M3 and task 1's revised Pipe
watchdog. [Metadata](metadata.json) records the binary and source hashes.
Final validation hashes distinguish later diagnostic-only hardening for
preflight errors and bounded socket snapshots. Raw binaries, `.trace` and
`.pprof` files remain in `/tmp/turbo-dispatch`; text evidence is tracked here.

The main sweep holds `GOMAXPROCS=8`: workers 1/8/32, queues 0/8/32/1024,
clients 1/8/32/128, both TCP and Pipe. Six samples for each of 96 configurations
use 100 ms Pipe runs and bounded 256-request TCP runs. Transport order reverses
each round. Within each transport the sweep order is fixed, so the selected
candidate is also checked in a separate alternating-order confirmation.
Selected `-cpu=1,4,8` cases hold workers, queues and clients fixed independently:
workers 1/8/32, queues 0/1024 and clients 8/128, six samples each. These comprise
1,008 sweep/CPU samples; confirmation adds 48. Timing, tracing, full allocation
profiling and race tests do not run together. There is no production before/
after comparison: configuration comparisons use identical code and handlers.

Each operation opens a fresh connection, reads the complete response, checks
HTTP/1.1/status/framing/body length and requires server EOF. Preflight also
checks payload contents. The original `BenchmarkHTTP11NoKeepAlive` retains its
serial loop and GOMAXPROCS-dependent `RunParallel` path for historical retries.
`BenchmarkHTTP11Accept` evenly partitions requests over the named client count
without a shared per-request counter. Short calibration runs can have fewer
requests than clients. Its name is historical: it measures full round trips.

The separate latency study records every request from dial through EOF and
client close, including queue/network waits. It adds clocks, atomic connection
accounting, latency storage and 1 ms occupancy sampling. Each normal
configuration gets one 2,048-request run in a fresh process. These percentiles
are observations from **closed-loop clients**, not an open-loop arrival model
or confidence intervals. Forced-GC initial/idle/completed/stopped snapshots
include runtime, pool and goroutine effects; heap deltas are not precise
worker-owned memory charges. Stack bytes and goroutine counts are also retained
in [raw study results](studies.jsonl). Warm heap values follow completed load;
they are not peak in-flight memory. B/op includes client and connection work.
Short TCP samples also include per-worker buffer/cache warmup.

## Configuration results

[All 96 configurations](configurations.md) report median ns/op, req/s, B/op,
objects/op, p50/p95/p99 and idle/warm live heap deltas. Selected examples:

| Transport | Workers/queue/clients | ns/op | B/op | objects/op | p95 µs | Idle KiB | Warm KiB |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Pipe | 32/1024/8 | 1,966.5 | 2,574 | 29 | 47.8 | 87.1 | 187.8 |
| Pipe | 8/0/8 | 1,991.5 | 2,572 | 29 | 53.0 | 24.3 | 91.9 |
| Pipe | 32/1024/128 | 2,452.5 | 2,592 | 29 | 742.1 | 92.3 | 297.2 |
| Pipe | 8/0/128 | 2,312.0 | 2,586 | 29 | 522.3 | 23.1 | 186.1 |
| Pipe | 1/0/8 | 4,599.5 | 2,582 | 29 | 43.7 | 11.2 | 44.9 |

In the initial sweep, eight workers with an unbuffered queue take 5.73% less
time at 128 Pipe clients (`p=0.002`, n=6); differences at 1/8/32 clients are
uncertain. Eight workers with an eight-slot queue improve that same case
5.12% (`p=0.017`). One unbuffered worker regresses Pipe timing by 3.12% serial
and 91.87–133.89% with concurrent clients. Some of its short TCP results improve
9.77–15.76%, but these do not transfer to Pipe or slow handlers. All allocation
costs, uncertain results and confidence intervals appear in the
[Pipe](pipe-benchstat.txt) and [TCP](tcp-benchstat.txt) reports.

The selected eight-worker/unbuffered candidate does **not** show a repeatable
timing benefit in the alternating confirmation. At 8/128 clients, Pipe medians
change from 2.190/2.598 µs to 2.189/2.557 µs (`p=1.000/0.937`), and TCP from
47.30/24.21 µs to 43.25/24.01 µs (`p=0.132/0.818`). The confirmation contains
large outliers and is retained without filtering. See the
[Pipe](confirmation-pipe-benchstat.txt) and [TCP](confirmation-tcp-benchstat.txt)
confirmation reports and raw `confirmation-*-{default,candidate}.txt` files.
The original candidate gain is therefore insufficient to change dispatch.

[CPU-capacity medians](cpu-configurations.md) and the corresponding
`cpu-*-benchstat.txt` reports retain every selected case. For example,
eight-worker/unbuffered Pipe with eight clients measures 3,814/1,322/1,987.5
ns/op at 1/4/8 CPUs; 32 workers/1,024 slots measures 3,813.5/1,320.5/1,955.5.
More CPUs or more workers is not uniformly faster on this workload/hardware.

## Backpressure and execution traces

[Slow-client/handler results](slow-clients-handlers.md) contain 36 instrumented
runs: both transports, workers 1/8/32, queues 0/8/1024, 128 clients and 512
requests. Slow clients wait a requested 1 ms after dial, before sending
headers; TCP may complete this wait in the listen backlog before acceptance.
Slow handlers sleep a requested 1 ms before writing. Actual sleeps depend on
scheduling. These are separate scenarios, not timing speedup comparisons.

For Pipe slow handlers, queue 1,024 holds up to 127/120/96 sampled connections
with 1/8/32 workers. Throughput is 908/7,448/29,964 req/s and p95 latency
142.84/17.56/4.29 ms. With queue zero, p95 is 149.69/17.69/4.51 ms. A large
queue changes where waiting occurs; it does not create handler capacity.
Eight workers materially reduce idle memory but also reduce capacity for
blocking handlers. Slow-client observations show that worker occupancy and
TCP backlog progress are separate effects. Occupancy samples are lower bounds;
the full-queue correctness test also establishes a saturated queue deterministically.

Five short traces use 512 requests and the same study instrumentation. They
retain separate synchronization, runnable-delay, network-wait and syscall
profiles in [`traces/`](traces/). The trace includes setup, forced GCs and
shutdown; a named `HTTP/1.1 dispatch load` task identifies the load interval in
the interactive trace viewer. Profile totals sum goroutine delays and must
not be treated as wall-time shares or compared across run lengths.

* **Idle receives, Pipe 32/1024/8:** 81.21 ms at the worker queue receive,
  80.9% of 100.39 ms cumulative synchronization wait. The sampled queue stays
  empty. No listener queue-send wait appears in the synchronization profile.
  This is spare worker capacity, consistent with the original block profile.
* **Full queue, Pipe 1/8/128 with slow handlers:** occupancy reaches eight.
  The listener spends 545.78 ms blocked on `t.conns <- c` during a 562.97 ms
  load interval. Worker receives account for only 3.73 ms. The client dial
  handoff backs up behind the listener. This is demonstrable backpressure.
* **Runnable delay:** the idle trace records 6.94 ms total scheduler delay,
  including 1.54 ms attributed to worker wakeups at the listener send. These
  are ready-to-run delays, distinct from an actually blocked channel send.
  The full-queue trace records 50.18 ms cumulative scheduler delay, including
  23.13 ms at client startup. Replacing the queue cannot be justified by
  treating all synchronization or startup delay as queue contention.
* **TCP network wait:** the ordinary TCP trace records 100.03 ms in dial
  readiness (`WaitWrite`), 81.66 ms in reads and 22.53 ms in accept. The slow
  client trace includes 0.15 s of server parser read wait. These are network
  waits, separately visible from worker queue receives and runnable delays.

## Sustained TCP failure evidence

The historical [original failure](original-failure.txt) is preserved. Three
rounds exercise both implementations, reversing implementation order in round
2 and pausing 15 s before round 3. Each implementation first runs serial TCP
SmallGET for 3 s, then retries the original 3 s parallel CPU profile with
eight clients. Round 1 fails for both implementations with eight EOF read
timeouts each; rounds 2 and 3 pass those profile retries. Those raw failures
remain in `sustained/round1-*/original-profile-retry.txt`; their CPU profiles
remain in `/tmp/turbo-dispatch/round1-*-cpu.pprof`. Failed profile timings are
excluded from performance conclusions.

Each round also runs an instrumented bounded 10,000-request test, a 3 s load
and a 10 s load. [All request and connection counters](sustained.md) are
retained. All six bounded runs pass. Three of six 3 s runs and all six 10 s
runs fail, producing **72 errors, all waiting for EOF after successful
response validation**. Each failure stops new work and drains outstanding
client deadlines. Its elapsed time and latency maximum include failure capture
and are not throughput measurements. A preflight connection is counted in
dial/accept/close totals but excluded from load attempts/completions.

Failure snapshots are captured before client close. For example,
[`round1-Turbo/10s`](failures/sustained/round1-Turbo/10s/failure-goroutines.txt)
records 99,740 accepted and 99,740 closed connections, queue 0/1,024, all 32
workers waiting for work and the listener waiting in accept. Its
[socket snapshot](failures/sustained/round1-Turbo/10s/failure-sockets.txt) shows
eight server endpoints in `FIN_WAIT_1` opposite eight `ESTABLISHED` clients.
The [stdlib capture](failures/sustained/round1-Stdlib/10s/failure-sockets.txt)
shows the same eight endpoint pairs; its stack has no active request handlers.
Every sustained study returns to two goroutines after shutdown, and accepted/closed/
close-call totals match. Matching TIME_WAIT counts are retained along with
eight sample rows; full original snapshots remain under `/tmp/turbo-dispatch`.

These observations locate the reproduced stall after application processing
and server close, at TCP teardown. They do not support queue saturation or a
worker leak. Successful dial/accept and complete response bodies also do not
support labeling the errors ephemeral-port exhaustion. Heavy TIME_WAIT state
is a correlated observation, not proof of a specific packet/kernel mechanism.

The 15 s pause before round 3 is followed by serial churn, so it does not
isolate cooldown. A separate control pauses **35 s before each implementation,
with no intervening serial load**. The global socket tables then contain only
8/1 TIME_WAIT entries before Turbo/stdlib. Both 3 s runs pass (61,439/111,450
completed requests), and both subsequent 10 s runs pass (235,995/302,046).
The [socket settings](socket-settings.txt) record a 15,000 ms MSL and ephemeral
range 49,152–65,535; no setting is changed. This is evidence that churn history
matters, but a single cooled control per server cannot identify the precise
cause or guarantee sustained-load reliability. Packet/kernel tracing would be
needed for that narrower root cause. Deadlines and the EOF requirement remain
unchanged, and the observed sustained failures are explicitly retained.

## Validation and reproduction

`GOCACHE=/tmp/turbo-dispatch/go-build go test -race -count=1 ./... -timeout=2m`
passes with loopback access enabled. The added deterministic full-queue test
checks blocked-send shutdown, graceful drain, a queued malformed request and
exactly-once close of accepted connections. Existing `TestTurboWorkers`,
`TestTurboClose`, body lifecycle, streaming and TCP/Pipe contract tests pass.
The opt-in study is also exercised under the race detector with saturated
Pipe slow handlers and TCP slow clients. Request partition tests cover zero,
fewer-than-clients and uneven counts. Normal studies record no request errors.

From the repository root, run the phases sequentially:

```sh
mkdir -p /tmp/turbo-dispatch
GOCACHE=/tmp/turbo-dispatch/go-build go test -c -o /tmp/turbo-dispatch/turbo.test .
python3 performance/worker-dispatch/measure.py matrix
python3 performance/worker-dispatch/measure.py latency
python3 performance/worker-dispatch/measure.py sustained
python3 performance/worker-dispatch/measure.py traces
python3 performance/worker-dispatch/measure.py confirmation
python3 performance/worker-dispatch/measure.py cooldown
python3 performance/worker-dispatch/measure.py summary
go tool trace /tmp/turbo-dispatch/traces-full-queue.trace
```

TCP requires local socket access and failure snapshots use `netstat`.
The collection script is tailored to Darwin. `studies.jsonl` and
`study-runs.txt` append diagnostic observations; the summary uses the latest
entry for each named case. Sweep and confirmation files are replaced when
rerun. Preserve this evidence directory before collecting a new experiment.
The final diagnostic adds preflight failure capture and bounds TIME_WAIT rows;
these changes do not affect the successful-load loops used for the measurements.
Binary profiles/traces and the original failed profile are locally retained
and reproducible, while all essential results and text summaries are tracked.
