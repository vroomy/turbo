# Recorded results

Measured on 2026-10-23 with Go 1.27.2, Darwin arm64, Apple M3, GOMAXPROCS=8.

The full matrix uses six samples per case at `-benchtime=200ms`. These are
medians of complete request/response operations, including the same client
and connection overhead for both servers. Parallel time/op is inverse
aggregate throughput, not per-request latency.

`http11-before.txt` is before reader reuse; `http11-after.txt` is after it.
The stdlib implementation is unchanged between those runs.

## Focused TCP check

The longer matrix showed high variability in later TCP cases. A separate
`-benchtime=200x -count=10` run limits total connection churn and checks the
128 B GET case again. It measures only 200 requests per sample, so allocation
figures include more pool warmup than the longer matrix. Do not mix samples
from the fixed-count and time-based runs.

| Server | Median time/op | Median B/op | Allocs/op |
| --- | ---: | ---: | ---: |
| Turbo | 58.95 µs | 4,910 | 65 |
| Stdlib | 62.71 µs | 6,030 | 75 |

See [focused TCP statistics](http11-tcp-comparison.txt) for confidence
intervals and the significance test.

## Reader reuse

For serial SmallGET over Pipe, Turbo's allocated bytes/op fell from **8,890 to 4,679**
(**47.4% less**); allocations fell from **52 to 50**.
An allocation profile identified the fresh 4 KiB request reader as the largest
allocation source. Readers are now pooled, reset to drop connection references,
and returned only after the connection and original request body have closed.
The lifecycle tests include unread, consumed, replaced, and incomplete bodies.

See [before/after statistics](http11-improvement.txt). Changes in unchanged
stdlib timings also indicate environmental variation; do not attribute those
changes to the Turbo optimization.

## Full matrix after reader reuse

| Transport | Workload | Mode | Turbo µs/op | Stdlib µs/op | Turbo B/op | Stdlib B/op | Turbo allocs/op | Stdlib allocs/op |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| TCP | SmallGET | Serial | 61.98 | 68.65 | 4,712 | 5,782 | 65 | 75 |
| TCP | SmallGET | Parallel | 25.81 | 27.98 | 4,716 | 5,766 | 65 | 75 |
| TCP | ImplicitGET | Serial | 72.78 | 188.60 | 4,488 | 4,952 | 59 | 69 |
| TCP | ImplicitGET | Parallel | 43.35 | 28.16 | 4,500 | 4,938 | 59 | 69 |
| TCP | LargeGET | Serial | 77.63 | 81.53 | 4,726 | 5,793 | 65 | 75 |
| TCP | LargeGET | Parallel | 143.33 | 34.43 | 4,744 | 5,804 | 65 | 75 |
| TCP | POST | Serial | 72.38 | 73.09 | 4,832 | 5,914 | 68 | 79 |
| TCP | POST | Parallel | 49.11 | 154.98 | 4,835 | 5,896 | 68 | 79 |
| TCP | HEAD | Serial | 71.05 | 73.33 | 4,578 | 5,624 | 63 | 73 |
| TCP | HEAD | Parallel | 28.04 | 105.61 | 4,597 | 5,656 | 63 | 73 |
| TCP | Empty | Serial | 147.49 | 322.88 | 4,326 | 5,555 | 58 | 70 |
| TCP | Empty | Parallel | 110.76 | 65.23 | 4,342 | 5,578 | 58 | 70 |
| Pipe | SmallGET | Serial | 6.39 | 11.32 | 4,679 | 5,674 | 50 | 58 |
| Pipe | SmallGET | Parallel | 2.21 | 3.01 | 4,682 | 5,787 | 50 | 58 |
| Pipe | ImplicitGET | Serial | 6.41 | 10.73 | 4,283 | 4,802 | 44 | 52 |
| Pipe | ImplicitGET | Parallel | 2.35 | 3.00 | 4,283 | 4,800 | 44 | 52 |
| Pipe | LargeGET | Serial | 11.85 | 17.73 | 4,518 | 5,612 | 50 | 58 |
| Pipe | LargeGET | Parallel | 3.02 | 3.88 | 4,518 | 5,609 | 50 | 58 |
| Pipe | POST | Serial | 6.81 | 11.11 | 4,624 | 5,734 | 53 | 62 |
| Pipe | POST | Parallel | 2.35 | 3.07 | 4,624 | 5,734 | 53 | 62 |
| Pipe | HEAD | Serial | 5.78 | 11.16 | 4,408 | 5,505 | 48 | 56 |
| Pipe | HEAD | Parallel | 2.18 | 3.01 | 4,408 | 5,504 | 48 | 56 |
| Pipe | Empty | Serial | 5.57 | 10.72 | 4,154 | 5,427 | 43 | 53 |
| Pipe | Empty | Parallel | 2.32 | 2.99 | 4,155 | 5,427 | 43 | 53 |

The [server comparison](http11-comparison.txt) reports confidence intervals
and significance for these measurements. Large spreads in TCP rows make
them unsuitable for a general speed claim; the focused TCP result is a
separate, narrower check. Pipe comparisons reduce kernel noise but are
not a substitute for deployed TCP measurements.

Raw measurements: [before](http11-before.txt), [after](http11-after.txt),
[focused TCP](http11-tcp-focused.txt). See the [methodology](README.md)
for commands, framing differences, and limitations.
