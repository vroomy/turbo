# Roadmap

## Current request parsing

Turbo uses `net/http.ReadRequest` for every incoming request. Uniform behavior
and a small server implementation take priority over the allocation savings of
the previous GET/HEAD fast path. The server still handles one HTTP/1.x request
per connection and closes the original request body during cleanup.

The internal parser is retained in the separate [`request` package](request/README.md),
with its tests, fuzzers, fixtures, and benchmarks. It is currently unused by the
server. Its limited fast path and stdlib fallback are a starting point for future
work, rather than a complete internal parser.

## Revisit an internal request parser

The future goal is one internal parsing implementation for the server's supported
HTTP/1.x requests, while preserving the `http.Request` interface. Keep stdlib as
the production parser until the replacement meets the following milestones.

1. **Define the compatibility and ownership contract.** Use the current stdlib
   path as the reference for accepted inputs, metadata, body framing, errors, and
   input position. Cover methods, HTTP/1.0 and HTTP/1.1, origin/absolute/authority
   targets, escaped URLs, header casing, duplicates, and folded headers. Document
   any proposed change to request lifetimes before introducing metadata reuse.
   Standard-library URL parsing can remain part of an internal HTTP parser.
2. **Develop the parser inside the request package.** Handle headers across
   arbitrary read boundaries, fixed-length and chunked streaming bodies,
   trailers, transfer-encoding precedence, length validation, and truncated
   input. Preserve close and read-after-close behavior. Bodies must stream beyond
   the initial buffer. Keep storage bounded and retained strings immutable;
   avoid shared caches, unsafe string reuse, and a fork of `net/http`.
3. **Prove behavior and lifecycle compatibility.** Extend the existing
   differential tests and both fuzzers against stdlib. Cover handler mutation,
   retained metadata/clones, changing request sizes, and concurrent workers.
   Preserve cleanup of the original body when handlers replace `req.Body`, and
   close the connection before closing an unread body. Run the full race suite
   and measure active/idle storage retention at 1, 8, and 32 workers.
4. **Demonstrate a worthwhile performance benefit.** Compare the complete
   candidate with stdlib using identical wire bytes and handlers, repeated
   alternating samples, and separately collected profiles. Include GET/HEAD,
   browser and escaped headers, fragmented input, POST/OpenRTB, chunked bodies,
   and changing sizes. Measure allocations, isolated processing, round trips,
   and sustained load; account for regressions and retained memory. Public
   parsing and worker-owned reuse have different ownership costs and must be
   evaluated separately.
5. **Replace production parsing only after review.** Integrate through a single
   server parsing path once supported-request coverage, lifecycle correctness,
   and repeatable gains justify the maintenance cost. Avoid a permanent
   method-based split or parser-selection option. If the replacement cannot meet
   these requirements simply, continue using stdlib and retain the library for
   research.

There is no delivery date or active parser rewrite. HTTP/2, TLS, keep-alive, and
other server features are separate work.

## Evidence to preserve

The previous GET/HEAD fast path reduced CPU and allocations, while POST/OpenRTB
already used stdlib. A narrow POST reuse prototype was previously reverted
because body-handling complexity and GET/fallback costs outweighed its gains.
Future work should address those tradeoffs before repeating that approach.

Keep reusable benchmarks and fixtures with the source. Keep detailed reports,
profiles, experimental implementations, and collectors under ignored
`research/`. The local
[parser comparison](research/parser-uniformity-2026-10-29/README.md) records the
decision's measurements and limitations; [TODO.md](TODO.md) tracks completed
work and historical allocation reviews.
