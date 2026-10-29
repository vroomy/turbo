# Allocation optimization TODO

## Stdlib request parsing — implemented 2026-10-29

Turbo now uses `http.ReadRequest` for every request and no longer owns custom
parser state. The experimental `request` package and its tests/benchmarks remain
available but are unused by the server. [ROADMAP.md](ROADMAP.md) records the plan
and acceptance criteria for revisiting an internal parser. This decision
supersedes the GET/HEAD reuse guidance in the historical reviews below.
The full race suite and both 30-second parser fuzzers pass; repeated benchmark
confirmation and validation are in the
[local integration report](research/parser-uniformity-2026-10-29/stdlib-integration/README.md).

## Parser uniformity review — completed 2026-10-29

Compared current parsing with a stdlib-only overlay inside the same Turbo server.
Internal small-GET parsing is 2.1× faster; stdlib-only adds 0.34 µs and 890 B per
isolated small GET. POST/OpenRTB timing is unchanged within measurement noise.
GET/HEAD Pipe time rises 7–17%; bounded TCP timing is inconclusive. These measured
GET/HEAD costs were accepted for uniformity. Full results, validation and effort
estimates:
[local parser uniformity report](research/parser-uniformity-2026-10-29/README.md).

Profiled 2026-10-29 against `7758a06` (Go 1.27.2, Darwin arm64, Apple M3).
At that profiling baseline, production code was unchanged. Its new benchmark
isolates the existing OpenRTB request and handler from the client and transport.

## Read this first

The following review records the earlier parser/reuse baseline; consult the
current decision and ROADMAP before proposing parser changes.

Our priorities are simplicity, low allocation churn, and bounded worker-owned
storage reset between requests. HTTP/1.1 still serves one request per connection.
Do not introduce shared caches, new pools, unsafe string reuse, background
maintenance, a fork of net/http, or a general HTTP/URL parser to chase these numbers.

[Local profile report and reproduction](research/allocation-review-2026-10-29/README.md)
contains the current measurements. [Previous TODO](research/allocation-review-2026-10-29/previous-TODO.md)
preserves earlier experiments. Detailed reports and artifacts are local-only
under the ignored `research/` directory.
The complete old TODO is also available with `git show 7758a06:TODO.md`.
Read its completed tasks 7, 8, 10 and 11 before proposing overlapping work.

**The top three below are ranked by flat allocated bytes in isolated OpenRTB
processing, not cumulative stack totals or an assumed production traffic mix.**
They are three allocation sites inside the same stdlib fallback, not three
independent easy fixes. Body draining and the unchanged response handler are
included; JSON parsing and auction logic are not.

| Rank | Allocation site | Profile bytes/request, approximately | What allocates |
| --- | --- | ---: | --- |
| 1 | `net/textproto.readMIMEHeader` | 556 | Header map, map storage, value slice and value strings |
| 2 | `net/http.readRequestLimit` (flat) | 320 | `new(http.Request)` |
| 3 | `net/url.parse` (flat) | 144 | `new(url.URL)` |

These sites together account for about 82% of the isolated allocation profile.
Small string accounting differs between heap profiles and benchmark counters;
use uninstrumented B/op and allocs/op to verify total savings. The current
OpenRTB median is **1,231 B/op, 18 allocs/op**. Ranking by object count puts the
handler's three `Header.Set` calls second; their 48 B/request are handler-owned.
`Header()` returns a normal Go map, so Turbo cannot intercept those calls.

## Shared working agreement for each fresh agent

1. Read this file, the report, and the prior task outcomes. Inspect the current
   branch/diff; earlier tasks may already have changed the baseline. Work
   sequentially and reprofile after each accepted change.
2. Keep the exact OpenRTB wire bytes and `openRTBHandler` unchanged. Use
   `BenchmarkOpenRTBTurboProcess` for server attribution, and
   `BenchmarkOpenRTBHTTP11/Pipe/Turbo` for sustained GC/throughput. Whole-process
   Pipe/TCP allocations include the client, transport and watchdog. Improvements
   there must be attributed to server stacks before being called a Turbo win.
3. Record a hypothesis and the simplest candidate before implementing it.
   Inspect the code and reject impossible approaches early: copying a request,
   URL or headers into reusable storage **after** `http.ReadRequest` returns
   cannot remove allocations that already happened. Public stdlib parsers do
   not accept caller-owned destination storage.
4. For a viable candidate, compare baseline/candidate binaries with identical
   harnesses and Go/environment settings. Collect at least six alternating-order
   uninstrumented samples, then allocation profiles separately. Include existing
   GET/HEAD, POST, browser/casing, escaped, fragmented and changing-size workloads.
   Compare repeated minute-long OpenRTB Pipe windows in fresh processes; use
   bounded TCP samples as a separate check. One timing sample is not significance.
5. Preserve fallback validation and input position, streaming bodies, original
   body cleanup, retained-clone safety, and bounded idle storage. Measure live
   heap for 1/8/32 warmed then idle workers and retained/released clones if adding
   storage. Fewer objects must not merely mean larger retained allocations.
6. Run `GOCACHE=/tmp/turbo-storage-go-cache GOMAXPROCS=8 go test -race -count=1 ./... -timeout=2m`
   with loopback access. Parser changes also require both `FuzzParse` and
   `FuzzParserReuse` separately with `-run '^$' -fuzz '^NAME$' -fuzztime=30s`
   in `./request`, using writable cache and GOMAXPROCS=8.
7. Finish by marking the task completed with evidence, implementation or
   no-change decision, validation, and residual allocation costs. Keep raw
   results and reproduction instructions under the ignored `research/` directory.
   If there is no simple measurable win, retain production behavior and stop;
   do not spend the complexity budget merely to check off a code change.

## 1. [x] Request header storage — decide whether POST reuse is now justified

**Completed 2026-10-29 — retain stdlib POST parsing.** No concrete simplification
over prior task 7 was found: reusing headers before allocation still requires
fixed-length streaming-body framing and close/lifecycle behavior. Per step 1,
the rejected prototype was not rebuilt; production and benchmark code are unchanged.
Six fresh OpenRTB samples confirm **1,231 B/op, 18 allocs/op**. A separate
20,000-request profile retains approximately 556 B/request for headers, 24 for
canonicalization, 320 for Request and 144 for URL; no savings are claimed.
The full race suite passes with loopback access, including body lifecycle and
Pipe/TCP contracts. Source/fixture hashes match the prior review. See the
[decision, evidence and reproduction](research/allocation-review-2026-10-29/task1-post-header-reuse/README.md).
Tasks 2/3 retain their dependent OpenRTB costs and remain separate reviews.

**Scope:** the largest OpenRTB site is about 556 profile B/request, plus a
separate approximately 24 B/request for `canonicalMIMEHeaderKey`. The wire name
`x-openrtb-version` goes through stdlib because POST falls back before Turbo's
header canonicalizer. Extending Turbo's constant-name switch alone will not
help this workload. GET browser names already benefit from the prior change.

**Entry points:** `request/request.go`: `parse`, `parseBuffered`, `parseHeaders`,
`headerStorage`, `ownHeader`, `Parser.Reset`; `turbo.go`: `process`,
`finishRequest`; `openrtb_benchmark_test.go`: workload, handler and both
benchmarks. Read local GOROOT `src/net/textproto/reader.go:readMIMEHeader` and
`src/net/http/request.go:readRequestLimit` for attribution, not code to copy.

**Do this in order:**

1. Review previous task 7: its POST reuse prototype was reverted because body
   framing/lifecycle complexity and GET/fallback costs outweighed isolated gains.
   The new OpenRTB profile establishes workload relevance, not a reversal of
   that decision. Write down a concrete simplification over that approach;
   if none exists, document no-change and let tasks 2/3 close their dependent
   OpenRTB opportunities without rebuilding the rejected prototype.
2. Only if justified, make a small experimental extension using the existing
   parser storage for fully buffered HTTP/1.1 headers, a simple origin target,
   and one unambiguous fixed Content-Length. Reuse existing parsing helpers;
   do not add a second general parser. The body must stream beyond the initial
   read. Unsupported or ambiguous input must reach stdlib with no bytes consumed.
   Account for all added body-reader state and allocations in the experiment.
3. Compare canonical/lowercase/custom/changing headers, duplicate headers,
   fragmented headers, zero/large/truncated bodies, invalid/overflow lengths,
   Transfer-Encoding precedence, chunking, trailers and unsupported forms against
   stdlib. Reuse existing differential tests and `TestTurboRequestBodyLifecycle`.
   Test handler body replacement, unread-body close, read-after-close behavior,
   metadata clones, append isolation of header value slices, and next-request reset.
4. Measure the full candidate as one change. It may also remove the request and
   URL allocations in tasks 2/3; explicitly report those savings here and do not
   later claim them twice. Compare against the current 1,231 B/op/18 allocs and
   verify sustained allocation/GC reductions without GET/fallback regressions.

**Done when:** a compact implementation earns its added protocol/state complexity
with repeatable end-to-end evidence and bounded retention, or a documented
no-change result explains why stdlib remains the right boundary. A custom parser
or broader body protocol implementation is outside this task's simplicity budget.

## 2. [x] Request object — reuse only before allocation, with a safe lifecycle

**Completed 2026-10-29 — retain stdlib-owned POST requests.** Task 1 accepted
no POST reuse path, and `http.ReadRequest` allocates its own Request without a
destination argument. GET/HEAD already reuse `Parser.req`; copying a fallback
request afterward cannot remove its allocation. Six fresh OpenRTB samples report
**1,231–1,232 B/op, 18 allocs/op**. A separate 20,000-request profile confirms
exactly **320 B and one Request object per request** at stdlib `new(Request)`.
Production/benchmark code is unchanged and no savings are claimed. The full
race suite with loopback access and a local POST→GET→error→POST lifecycle/clone
audit pass. See the [decision, evidence and reproduction](research/allocation-review-2026-10-29/task2-request-object/README.md).
Task 3 remains a separate review.

**Dependency:** read task 1's result and reprofile. OpenRTB request reuse requires
an accepted path that does not call `http.ReadRequest` for the eligible request.
If task 1 stays on stdlib, this task should record the 320 B/request cost as an
accepted boundary unless a new, simpler mechanism is demonstrated. If task 1
already reuses `Parser.req`, validate and close this task without duplicate work.

**Entry points:** `request/request.go`: `Parser.req`, `Parser.Parse`, `Reset`,
`makeRequest`, `parseBuffered`; `turbo.go`: `work`, `process`, `finishRequest`;
local GOROOT `src/net/http/request.go:readRequestLimit` (`req = new(Request)`).
See `process-openrtb-lines-request.txt` in the report's results directory.

**Do this in order:**

1. Confirm exactly which paths still allocate the request. The existing GET/HEAD
   fast path already reuses `Parser.req`; allocating public `request.Parse`
   deliberately returns independently owned metadata. Do not weaken that API.
2. If an accepted eligible POST path exists, populate the existing `Parser.req`
   directly, without a new pool, wrapper, lifecycle flag, or second request slot.
   Ensure fallback errors and partial parsing cannot expose stale worker fields.
   Do not attempt to recycle a stdlib request by assigning it into `Parser.req`.
3. Verify worker transitions POST→GET→error→POST, handler-mutated headers/URL,
   retained `Request.Clone` metadata, and original-body cleanup even if the
   handler replaces `req.Body`. Connection close must precede unread-body close.
   Reset must release request/body/connection references after every outcome.
4. Attribute the request site's bytes and objects before/after. Measure the
   actual total delta, including any new body state, ownership copies or
   retention. Apply the shared regression, race, fuzz and sustained checks.

**Done when:** the measured request allocation is eliminated on a proven path
without new lifecycle complexity, already eliminated by task 1, or explicitly
retained because stdlib owns its allocation. Do not claim 320 B saved based
solely on the size of a field reused after parsing.

## 3. [x] URL object — retain simple-target reuse and stdlib semantics

**Completed 2026-10-29 — retain existing URL parsing.** Tasks 1/2 kept POST on
stdlib; a fresh 20,000-request profile confirms **144 B and one URL per request**.
Simple GET/HEAD already reuse `Parser.url`. Escaped targets call stdlib once;
its unescape/default-escape work preserves `Path`/`RawPath`, and no simple
existing-helper change avoids it. Six fresh OpenRTB samples retain **1,231 B/op,
18 allocs/op**; escaped browser cases retain **184 extra B/op and three objects**.
No production/benchmark changes or savings are claimed. The full race suite
with loopback access and a local URL/input-position/clone audit pass (2,688 cases,
2,540 retained clones). See the
[decision, evidence and reproduction](research/allocation-review-2026-10-29/task3-url-object/README.md).

**Dependency:** read both prior outcomes and reprofile. OpenRTB's target
`/openrtb2/auction` is already eligible for Turbo's `simpleTarget` helper, but
POST dispatch sends it to stdlib. Task 1 may remove this 144 B/request allocation
as a side effect. Confirm that before attempting another optimization.

**Entry points:** `request/request.go`: `makeRequest`, `simpleTarget`, `parseURL`,
`Parser.url`; local GOROOT `src/net/url/url.go:parse` (`url := new(URL)`).
Profiles: `process-openrtb-lines-url.txt` and `process-escaped-alloc_space.txt`.
Existing regression fixtures: `BenchmarkTurboProcessHeaders/EscapedHeaders`,
`BenchmarkParse/EscapedPath`, and both parser differential fuzzers.

**Do this in order:**

1. Confirm whether accepted POST reuse already routes eligible simple targets
   through `makeRequest` and `Parser.url`. If so, record its measured benefit
   and close the OpenRTB portion. If POST still falls back, record the dependency;
   copying `*url.ParseRequestURI(...)` into `Parser.url` does not remove allocation.
2. Independently inspect the escaped GET/HEAD profile: it currently adds about
   184 B and three allocations relative to browser headers, through URL parsing,
   unescape and escape work. Look only for redundant work avoidable with existing
   helpers. An allocation site alone does not justify a custom unescaper/parser.
3. Any candidate must preserve `Path`, `RawPath`, `RawQuery`, `ForceQuery`,
   RequestURI, host handling and invalid-input behavior. Cover escaped slash,
   space, percent, invalid escapes, empty query, absolute/authority forms and
   retained clones through worker reset. Keep complex targets on stdlib unless
   a narrowly scoped change is independently justified by the measurements.
4. Compare OpenRTB, simple GET/HEAD and escaped/canonical browser cases, including
   unknown/changing targets. Measure total allocations, live string storage and
   end-to-end behavior; do not optimize only the literal auction URL.

**Done when:** an independently justified simple change passes the shared gates,
prior tasks already removed the eligible URL allocation, or the remaining
stdlib URL cost is documented as the simplest correct implementation.
