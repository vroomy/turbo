# Turbo

The current performance target is HTTP/1.1 with a fresh connection per request.

Turbo reuses request metadata and response writers per worker. They are valid
while the handler runs. To retain a request's metadata after the handler returns,
call `req.Clone(req.Context())` before returning. The clone shares the body,
which must be consumed during the handler.

Benchmark tests and reusable scripts are kept with the source. Local benchmark
results and research notes belong in `research/`, which is ignored by Git.
See the [TODO](TODO.md) for current optimization priorities and validation
requirements.

The [standalone hello-world examples](examples/README.md) compare stdlib and
Turbo as separate executables using the same handler and the oha load
generator. Turbo's `NewWithHandler` constructor installs the handler before
the server starts accepting connections.
