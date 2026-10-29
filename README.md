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

The [standalone hello-world examples](examples/README.md) compare stdlib and
Turbo as separate executables using the same handler and the oha load
generator. Turbo's `NewWithHandler` constructor installs the handler before
the server starts accepting connections.
