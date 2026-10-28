# GET/HEAD header parser

Completed TODO task 5 on 2026-10-28. The parser recognizes canonical and
lowercase Host, Connection, and Content-Length names as validated constants.
For other canonical names, one scan checks token validity and casing together.
Uncommon casing still uses `http.CanonicalHeaderKey` after validation. Every
value is validated, and duplicates and unsupported forms still fall back to
`http.ReadRequest` without consuming input. Host skips the header-map lookup
because it lives in `Request.Host`.

Fresh storage still uses a line count to size the map and value array. Reused
storage counts only the remaining lines when the value array must grow. The
growth estimate excludes a Host that has not yet appeared, preserving reuse
at the 128-value retention boundary even when Host comes last. Each mapped
value slice remains capped at one. Reset still clears references and discards
maps/value arrays beyond 128 entries/capacity.

Immutable string ownership is unchanged: requests of at most 256 bytes share
append-only 1 KiB blocks, and larger headers own an allocation. No old string
bytes are overwritten. A retained `Request.Clone` can safely share those
strings across parser reuse. Reported zero allocations in the reused parser
benchmark are rounded down; SmallGET still allocates about 68 bytes/request
on average in immutable blocks.

## Measurement setup

The baseline is commit `214bf58eca07af9b18edb6ba314a7d50d82e5c64`, with the
new benchmark cases and fixture copied into its source snapshot. Only
`request/request.go` differs in production. [Metadata](metadata.json) records
binary and source hashes. Both versions use Go 1.27.2, Darwin arm64, Apple M3,
`GOMAXPROCS=8`, and the same harness, including the revised Pipe watchdog,
response coalescing, and Date cache. Round trips use 32 workers, a 1,024-slot
queue, and one/eight serial/parallel clients, with a fresh connection per
request. Parallel ns/op measures inverse aggregate throughput, not latency.

Each case has ten samples, alternating binary order each round. Parser,
process, and Pipe samples use 200 ms; TCP samples use bounded 200-request
runs because sustained teardown stalls remain a separate issue from task 4.
Timing, tests, compilation, and profiling ran separately. No handler work
was removed: process benchmarks keep the existing SmallGET handler's two
`Header.Set` calls. The larger process cases use that same handler.

The shared [browser fixture](../../testdata/browser-headers.txt) has 19 fields,
including User-Agent, cookies, content negotiation and Sec-Fetch fields. It
exceeds the immutable block sharing threshold. Parser benchmarks cover
canonical, lowercase and mixed-case variants, HEAD with an escaped path,
the original six-field request, and POST/chunked fallbacks. Stdlib and
`BenchmarkOwnHeader` provide unchanged-code controls.

Raw `before-*.txt`/`after-*.txt` samples, [all medians](medians.md), and
[parser](parser-benchstat.txt), [process](process-benchstat.txt),
[Pipe](pipe-benchstat.txt), and [TCP](tcp-benchstat.txt) benchstat reports
retain time, bytes and object counts, including uncertain results.

## Results

| Case | Before | After | Change | p |
| --- | ---: | ---: | ---: | ---: |
| Reused parser, SmallGET | 361.1 ns | 290.35 ns | -19.59% | <0.001 |
| Reused parser, lowercase SmallGET | 410.2 ns | 290.8 ns | -29.11% | <0.001 |
| Reused parser, six fields | 600.4 ns | 520 ns | -13.39% | <0.001 |
| Reused parser, browser headers | 1,824.5 ns | 1,644.5 ns | -9.87% | <0.001 |
| Reused parser, escaped HEAD/browser headers | 1,923.5 ns | 1,756.5 ns | -8.68% | <0.001 |
| Process, SmallGET | 834.4 ns | 762.8 ns | -8.58% | <0.001 |
| Process, HEAD | 832.3 ns | 761.65 ns | -8.49% | <0.001 |
| Process, browser headers | 2,298.5 ns | 2,122.5 ns | -7.66% | <0.001 |
| Process, escaped HEAD/browser headers | 2,404.5 ns | 2,239.5 ns | -6.86% | 0.001 |
| Pipe SmallGET serial | 4,898 ns | 4,585.5 ns | -6.38% | <0.001 |
| Pipe HEAD serial | 4,881.5 ns | 4,754.5 ns | -2.60% | <0.001 |
| Pipe SmallGET parallel | 1,964 ns | 1,950 ns | uncertain | 0.382 |
| Pipe HEAD parallel | 1,926.5 ns | 1,896.5 ns | uncertain | 0.184 |
| TCP SmallGET serial | 68.3745 µs | 64.343 µs | uncertain | 0.165 |
| TCP SmallGET parallel | 24.412 µs | 25.242 µs | uncertain | 0.739 |

All reused GET/HEAD parser cases improve significantly. Larger lowercase and
mixed-case sets improve 5.86% and 1.95%; mixed-case SmallGET improves 5.13%.
Allocating Turbo parsing improves 7.26–21.42% on the common/canonical cases;
mixed-case browser headers improve only 0.89% (`p=0.045`) and mixed-case
SmallGET is uncertain (`p=0.075`). Every parser and storage-copy workload
has identical B/op and reported objects/op. POST/chunked fallback timings
are uncertain. The unchanged Stdlib controls are uncertain except for a
0.33% escaped-path decrease (`p=0.041`); storage-copy controls are uncertain.

The six original GET/HEAD process cases improve 8.02–9.28%, and the two
larger cases improve 6.86–7.66%. SmallGET stays at 100 B/op and two objects;
browser headers stay at 736 B/three objects; escaped HEAD stays at
920 B/six objects. POST is 1,192.5 to 1,183.5 ns (`-0.75%`, `p=0.015`),
still 1,107 B/13 objects. Its parser path is unchanged, so that small process
difference does not establish a POST parsing optimization.

Both serial Pipe cases improve; parallel changes are uncertain. Pipe object
counts are unchanged (29 SmallGET, 27 HEAD), and B/op differences are
uncertain. All TCP timing and allocation differences are uncertain, including
the SmallGET parallel median time increase of 3.40% and reported object
median of 45.5 to 46. Short TCP runs include worker, pool and buffer warmup:
the complete table retains their byte/object fluctuations. TCP HEAD
serial/parallel changes by -3.48/-2.25% (`p=0.684/0.280`). No TCP speedup is
established, and no requests timed out. There are no significant timing or
allocation regressions in the measured matrix.

## Profiles

CPU profiles use separate 3 s runs of reused SmallGET/browser parsing and
SmallGET/browser processing. `*-cpu-run.txt` records commands and instrumented
output; `*-cpu-top.txt` records sampled stacks. Parser source listings use
the matching baseline or changed source snapshot. Cumulative sampled shares
overlap; profile totals include calibration and setup and do not measure a
fixed number of requests. Uninstrumented timings determine speedup claims.

In reused SmallGET parsing, `parseHeaders`' cumulative sampled share changes
from 44.64% to 40.79%, while `CanonicalMIMEHeaderKey` changes from 10.42% to
3.97%. Canonicalization still occurs elsewhere, including `Header.Get`.
For browser parsing, `parseHeaders` changes from 42.40% to 35.34%. In process
profiles it changes from 20.38% to 10.96% for SmallGET, and 36.29% to 36.00%
for browser headers. The browser profile moves token/casing work into the
new helper; map access, value validation and string ownership remain. These
sampled shares support the reduced repeated work, with the timing matrix
providing the more precise performance comparison.

Separate allocation profiles use 20,000 SmallGET/browser process requests
with sampling rate one. `*-memory-run.txt` and
`*-{alloc_space,alloc_objects,inuse_space}.txt` retain their results. Profile
totals include setup/runtime costs; post-benchmark live heaps do not measure
resident workers. The implementation adds no parser fields or background work.

`ownHeader` allocates exactly the same profiled storage on both sides:
1,335 blocks totaling 1,335 KiB for SmallGET, and 20,001 allocations totaling
13,750.69 KiB for browser headers. Handler/default-header `MIMEHeader.Set`
objects are 40,008/40,008 for SmallGET and 40,008/40,009 for browser headers,
including setup and clock-boundary effects. Overall profiled allocation
totals are 2,172.18/2,176.66 KiB (SmallGET) and 14,632.70/14,623.34 KiB
(browser). Final live heaps are 33.47/33.27 KiB and 39.80/34.68 KiB;
runtime/setup variation in these totals is not a per-request allocation or
resident-worker memory change.

## Validation and reproduction

`GOCACHE=/tmp/turbo-go-build-cache go test -race -count=1 ./... -timeout=2m`
passes with loopback access, including protocol, streaming, worker, close,
body-lifecycle, mutation, and concurrent clone tests. Sandboxed TCP binds
were denied on the first attempt; the permitted run passes. The additional
tests check all 256 bytes at several key positions and in common values,
every fragmented read boundary through late fallbacks, mixed-case duplicate
Host/Content-Length, handler appends, clone retention through value-array
growth, and both sides of the retained-capacity limit. The new differential
and storage tests also pass against the baseline parser.

Both differential fuzzers run independently for 30 s with eight workers;
[parse](fuzz-parse.txt), [reuse](fuzz-reuse.txt), and [race](race.txt) logs
are retained. Their oracle compares complete requests, exact error text,
body bytes, body-read errors and close errors with `http.ReadRequest`.
The final runs exercise 2,430,840 parse cases and 2,509,409 reuse cases.

From the repository root, using an empty baseline directory:

```sh
mkdir -p /tmp/turbo-get-head-parser/before-source
git archive 214bf58eca07af9b18edb6ba314a7d50d82e5c64 | tar -x -C /tmp/turbo-get-head-parser/before-source
cp request/request_test.go request/headers_test.go /tmp/turbo-get-head-parser/before-source/request/
mkdir -p /tmp/turbo-get-head-parser/before-source/request/testdata
cp request/testdata/browser-headers.txt /tmp/turbo-get-head-parser/before-source/request/testdata/
cp request_headers_benchmark_test.go /tmp/turbo-get-head-parser/before-source/
(cd /tmp/turbo-get-head-parser/before-source && GOCACHE=/tmp/turbo-go-build-cache go test -c -o /tmp/turbo-get-head-parser/before-request.test ./request)
(cd /tmp/turbo-get-head-parser/before-source && GOCACHE=/tmp/turbo-go-build-cache go test -c -o /tmp/turbo-get-head-parser/before.test .)
GOCACHE=/tmp/turbo-go-build-cache go test -c -o /tmp/turbo-get-head-parser/after-request.test ./request
GOCACHE=/tmp/turbo-go-build-cache go test -c -o /tmp/turbo-get-head-parser/after.test .
python3 request/performance/get-head-parser/measure.py timing
python3 request/performance/get-head-parser/measure.py summary
python3 request/performance/get-head-parser/measure.py profiles
GOCACHE=/tmp/turbo-go-build-cache GOMAXPROCS=8 go test ./request -run '^$' -fuzz '^FuzzParse$' -fuzztime=30s -parallel=8 -timeout=1m
GOCACHE=/tmp/turbo-go-build-cache GOMAXPROCS=8 go test ./request -run '^$' -fuzz '^FuzzParserReuse$' -fuzztime=30s -parallel=8 -timeout=1m
```

Use `timing --groups parser process pipe` and then `timing --groups tcp` to
separate the part requiring loopback access. Binaries, source snapshots and
binary profiles stay under `/tmp/turbo-get-head-parser`; text evidence is in
this directory. `--scratch` selects another scratch directory. Evidence lives
under `request/performance` to respect the existing root performance ignore.
