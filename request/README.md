# Experimental request parser

This package preserves Turbo's internal HTTP/1.x request parser for future
development. **Turbo currently uses `net/http.ReadRequest` for every incoming
request and does not use this package.** It remains a separate Go package in the
Turbo module; it does not need a separate module or repository.

The retained implementation has a fast path for fully buffered, bodyless
HTTP/1.1 GET/HEAD requests and delegates other forms to stdlib. `Parse` returns
independently owned metadata. `Parser` reuses request, URL, and header storage;
its returned metadata is valid until the next parse or reset. Callers must read
or close bodies and clone metadata they need to retain across parser reuse.

Tests, differential fuzzers, fixtures, and parser benchmarks remain alongside
the implementation. They exercise this experimental library independently of
Turbo's server benchmarks, which measure stdlib request parsing.

See [the roadmap](../ROADMAP.md) for the coverage and validation required before
an internal parser is used by the server again.
