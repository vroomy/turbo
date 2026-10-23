# Turbo

The current performance target is HTTP/1.1 with a fresh connection per request.

Turbo reuses request metadata and response writers per worker. They are valid
while the handler runs. To retain a request's metadata after the handler returns,
call `req.Clone(req.Context())` before returning. The clone shares the body,
which must be consumed during the handler.

See [benchmarks](benchmarks/README.md) for repeatable Turbo versus `net/http`
comparisons, workload definitions, and recorded results.
