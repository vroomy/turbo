# Worker-local HTTP Date cache

Completed TODO task 3 on 2026-10-25. The formatted HTTP Date is retained in
each worker's response writer across resets. Every response still reads the
clock. Formatting occurs only on initialization or a different Unix second,
including backward clock changes. An empty string distinguishes an
uninitialized cache at Unix second zero.

The cache adds an int64 and string header (24 bytes on this arm64 build) per
worker and retains one formatted string. It needs no shared synchronization,
ticker, or background goroutine. A cache miss still allocates the formatted
string and, when Date changes, a header value slice. Date strings stay
immutable; existing `populateHeader` behavior preserves matching slices and
replaces changed slices rather than overwriting handler-owned storage.
Handler overrides and nil/empty Date suppression apply to the response being
written. The next response gets the cached default, even within the same
second. Header commitment and snapshot behavior are unchanged.

## Measurement setup

The baseline is commit `45364e7d1c6c0b1cf71211d5f2b8ae4bfb0d8a8d`.
Both binaries have the same benchmark harness and handlers, including task
1's revised Pipe watchdog and task 2's response coalescing. No benchmark
handler work was removed. [Metadata](metadata.json) records binary hashes,
production/harness source hashes, Go 1.27.2, Darwin arm64, and Apple M3.
`GOMAXPROCS=8`; Turbo has 32 workers and a 1,024-slot queue. Serial mode has
one client and parallel mode has eight. Each connection serves one request.

The matrix contains all seven workloads, plus both response-writer cases.
Ten samples per version alternate binary order each round. Isolated and
Pipe cases use 200 ms samples; TCP uses bounded 200-request samples. No test,
compilation, timing, or profiling jobs ran concurrently. Parallel ns/op is
inverse aggregate throughput, not individual request latency.

Raw timing files are `before-{isolated,pipe,tcp}.txt` and their `after-`
counterparts. [All medians](medians.md) include ns/op, B/op, and objects/op
for every case, including uncertain results. Confidence intervals and
significance are in the [isolated](isolated-benchstat.txt),
[Pipe](pipe-benchstat.txt), and [TCP](tcp-benchstat.txt) benchstat reports.

## Results

| Case | Before | After | Change | p |
| --- | ---: | ---: | ---: | ---: |
| Isolated SmallGET | 918.25 ns | 791 ns | -13.86% | <0.001 |
| Isolated POST | 1,243.5 ns | 1,144 ns | -8.00% | <0.001 |
| ResponseWriter final | 594.55 ns | 491.95 ns | -17.26% | <0.001 |
| ResponseWriter informational | 729.2 ns | 616.6 ns | -15.44% | <0.001 |
| Pipe SmallGET serial | 5.077 µs | 4.948 µs | -2.54% | 0.017 |
| Pipe SmallGET parallel | 1.983 µs | 1.9485 µs | -1.74% | <0.001 |
| Pipe LargeGET serial | 11.2435 µs | 11.0265 µs | -1.93% | 0.005 |
| Pipe POST serial | 5.333 µs | 5.221 µs | -2.10% | 0.001 |
| TCP SmallGET serial | 80.0455 µs | 74.591 µs | uncertain | 0.481 |
| TCP SmallGET parallel | 32.942 µs | 26.4155 µs | uncertain | 0.075 |

All seven isolated workloads improve significantly, ranging from 8.00% for
POST to 13.86% for SmallGET. All save 32 B/request and one reported allocation.
SmallGET changes from 132 B/3 objects to 100 B/2 objects; POST from 1,139 B/14
objects to 1,107 B/13 objects. The writer cases change from 48 B/2 objects to
16 B/1 object. ImplicitGET's reported zero objects is rounded down: immutable
request header blocks still allocate occasionally, as do cache refreshes.

Other significant Pipe gains are HEAD serial/parallel (2.46/1.76%), Empty
serial/parallel (1.93/3.66%), and StreamGET parallel (1.78%). ImplicitGET
serial/parallel changes by +0.36/+0.86%, LargeGET parallel by -0.57%, POST
parallel by -0.58%, and StreamGET serial by +0.02%; these changes are uncertain.
Every Pipe case reports one fewer object; SmallGET serial changes from
2,607 to 2,575.5 B/op. There are no significant timing regressions in the matrix.

Every TCP time difference is uncertain. This includes median increases for
POST serial (+2.24%), HEAD parallel (+4.88%), and Empty parallel (+9.54%).
These bounded samples have wide confidence intervals, so they establish no
TCP timing gain or regression. SmallGET serial B/op changes from 3,263 to
3,227.5 and reported objects from 46 to 45. Short samples include cache
initialization across the workers and pool/buffer warmup, so TCP allocation
differences need not equal the steady-state 32 B reduction exactly. All
timing and profiling TCP runs completed without request errors or timeouts.

## Profiles and costs

CPU profiles were collected separately for isolated SmallGET and full
SmallGET round trips over Pipe and TCP, in serial and parallel modes. Each
isolated/Pipe profile uses a 3 s benchmark; bounded TCP profiles use three
2,000-request samples. Commands and instrumented output are retained in
`*-cpu-run.txt`, and sampled stacks in `*-cpu-top.txt`. These instrumented
times are not used for speedup claims.

The isolated Date expression accounts for 460 ms of 4.06 s sampled before
(11.33%), versus 220 ms of 4.16 s after (5.29%); the remaining expression
includes time lookup and map work. The [before source listing](before-date-cpu-list.txt)
uses a baseline source snapshot, and the [after listing](after-date-cpu-list.txt)
uses the changed writer. Full Pipe profiles remain dominated by runtime
thread waits/signals; TCP profiles remain dominated by system calls and
netpoll. These samples do not resolve a precise end-to-end Date CPU cost.

Full-allocation profiles use 20,000 isolated GETs and sampling rate one.
The [before Format listing](before-date-allocations.txt) shows 20,003
allocations totaling 625.09 KiB; the [after listing](after-date-allocations.txt)
shows two allocations (64 bytes), from benchmark setup. Overall allocation
bytes change from 2,802.39 to 2,181.99 KiB and objects from 62,410 to 42,417.
The totals include setup, runtime, and testing costs; unrelated pool/runtime
variation prevents treating their difference as an exact per-request figure.
Handler header allocations and immutable request storage remain.

`*-process-{alloc_space,alloc_objects,inuse_space}.txt` retain heap summaries.
The final live heaps are 38.79/38.66 KiB, after the benchmark's local writer
is out of scope. They do not measure retained live workers. Worker ownership
and one retained immutable string bound this cache's storage.

## Validation and reproduction

`GOCACHE=/tmp/turbo-date-cache/go-build go test -race -count=1 ./... -timeout=2m`
passed with loopback access enabled, including the parallel worker tests and
TCP/Pipe contract and streaming checks. The initial sandboxed attempt failed
because TCP binds were denied; it reported no races. Deterministic helper
tests cover zero/negative Unix seconds, same-second hits with zero allocations,
rollover, backward time, forward jumps, UTC output, immutable prior values,
and reset reuse. Parallel response tests cover Date overrides, nil/empty
suppression, committed headers, and restored defaults on the next response.

From the repository root, using an empty baseline directory:

```sh
mkdir -p /tmp/turbo-date-cache/before-source
git archive 45364e7d1c6c0b1cf71211d5f2b8ae4bfb0d8a8d | tar -x -C /tmp/turbo-date-cache/before-source
(cd /tmp/turbo-date-cache/before-source && GOCACHE=/tmp/turbo-date-cache/go-build go test -c -o /tmp/turbo-date-cache/before.test .)
GOCACHE=/tmp/turbo-date-cache/go-build go test -c -o /tmp/turbo-date-cache/after.test .
python3 performance/http-date-cache/measure.py timing /tmp/turbo-date-cache/before.test /tmp/turbo-date-cache/after.test
python3 performance/http-date-cache/measure.py summary
python3 performance/http-date-cache/measure.py profiles /tmp/turbo-date-cache/before.test /tmp/turbo-date-cache/after.test
```

TCP requires loopback access; hardware metadata uses `sysctl`. To split the
collection, pass `--groups isolated pipe` or `--groups tcp` to `timing` and
`profiles`. Source snapshots and test binaries/profiles stay in
`/tmp/turbo-date-cache`; all text evidence and scripts in this directory are
tracked. The ignore exception applies only to this evidence directory.
