# Turbo

The current performance target is HTTP/1.1 with a fresh connection per request.

Turbo uses `net/http.ReadRequest` for every incoming request. Each request owns
its metadata; response writers are reused per worker and are valid only while
the handler runs. Request bodies must be consumed during the handler and are
closed afterward. Use `req.Clone(req.Context())` when you need an independent
copy of request metadata; cloning shares the body.

The experimental parser is preserved in the separate [request package](request/README.md)
and is currently unused by the server. [ROADMAP.md](ROADMAP.md) describes the plan
to revisit an internal parser after its coverage, maintenance cost, and measured
benefits justify replacing stdlib parsing.

Benchmark tests and reusable scripts are kept with the source. Local benchmark
results and research notes belong in `research/`, which is ignored by Git.
See the [TODO](TODO.md) for current optimization priorities and validation
requirements.

Turbo's `NewWithHandler` constructor installs the handler before
the server starts accepting connections.

## Turbo vs stdlib benchmarks

This compares the current Turbo server with Go's `net/http.Server`, using the
same handlers, request bytes, and client. Both servers use stdlib request
parsing. Each request opens a fresh HTTP/1.1 connection; stdlib keep-alive is
disabled. There is no TLS.

Measured 2026-10-29 at `7bdf2c5`: Go 1.27.2, Darwin arm64, Apple M3,
`GOMAXPROCS=8`, `GOGC=100`, `GOMEMLIMIT=off`. Turbo has 32 workers and a
1,024-slot queue. Results are medians of ten samples per case, alternating
Turbo/stdlib order in separate benchmark processes. Pipe samples target 200 ms;
TCP samples perform 200 requests each. Contract checks and all 560 timing
samples completed without request errors.

### In-memory Pipe, one client

Pipe includes the server and client but avoids kernel TCP costs. Turbo takes
37.0–53.3% less time across these one-client workloads; all differences have
`p<0.001`. Results across one and eight clients range from 24.8–53.3% less time.

| Case | Turbo µs/op | Stdlib µs/op | Turbo B/op | Stdlib B/op | Turbo allocs/op | Stdlib allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Small GET | 5.104 | 10.777 | 3,472.5 | 5,697.5 | 37 | 58 |
| Implicit GET | 5.348 | 10.653 | 3,401.0 | 4,885.0 | 33 | 52 |
| Large GET | 11.627 | 18.447 | 3,466.0 | 5,713.0 | 37 | 58 |
| POST | 5.215 | 10.996 | 3,588.0 | 5,829.0 | 40 | 62 |
| HEAD | 5.174 | 10.866 | 3,353.0 | 5,566.0 | 35 | 56 |
| Empty GET | 4.936 | 10.560 | 3,291.0 | 5,486.5 | 32 | 53 |
| Stream GET | 6.602 | 10.889 | 3,470.0 | 5,700.5 | 37 | 58 |

Small GET returns 128 B; large GET returns 64 KiB; POST consumes 1 KiB and
returns 128 B. HEAD returns headers for a 128 B representation, empty GET has
no body, and stream GET writes 512 B in four writes. Implicit GET uses only
`Write`, allowing each server's normal response framing; stdlib may add
Content-Length while Turbo uses connection-close framing.

### Loopback TCP

**All TCP timing differences are statistically inconclusive** (`p>=0.063`
across the full matrix), including the faster and slower medians below. These
samples establish no TCP speedup. Turbo has lower median allocated bytes and
allocation counts in every measured Pipe and TCP case.

| Case | Turbo µs/op | Stdlib µs/op | Turbo B/op | Stdlib B/op | Turbo allocs/op | Stdlib allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Small GET / 1 client | 74.504 | 79.084 | 4,003.5 | 6,139.5 | 52 | 75 |
| Small GET / 8 clients | 28.521 | 37.239 | 4,498.0 | 6,850.0 | 53 | 76 |
| POST / 1 client | 74.983 | 77.847 | 4,030.5 | 6,106.0 | 55 | 79 |
| POST / 8 clients | 32.321 | 28.085 | 4,512.0 | 6,762.0 | 56 | 79 |

The tables report allocations for the complete benchmark process, including the
shared client, connections, and timeout handling. They are not server-only
allocation counts or retained heap. Short TCP runs include worker warmup.
Eight-client µs/op measures inverse aggregate throughput, not request latency.
Half-byte values are medians of integer B/op samples; integer allocs/op counters
truncate fractional allocations. These same-host, short-window results do not
establish production capacity, tail latency, or sustained GC behavior.

### Run the comparison

The existing harness runs both implementations. These commands repeat the same
workload matrix; the published comparison additionally alternated implementation
order in fresh processes.

```sh
GOMAXPROCS=8 GOGC=100 GOMEMLIMIT=off go test -run '^$' \
  -bench '^BenchmarkHTTP11NoKeepAlive$/^Pipe$/' -benchmem -benchtime=200ms -count=10 .
GOMAXPROCS=8 GOGC=100 GOMEMLIMIT=off go test -run '^$' \
  -bench '^BenchmarkHTTP11NoKeepAlive$/^TCP$/' -benchmem -benchtime=200x -count=10 .
```

Save benchmark output, profiles, and experimental artifacts under `research/`.
The complete 28-case medians, raw samples, benchstat reports, settings/hashes,
and balanced-order collector are retained in the local-only
[comparison report](research/mvp-stdlib-comparison-2026-10-29/README.md).
