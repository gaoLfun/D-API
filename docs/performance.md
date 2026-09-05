# Gateway performance

## Implemented optimizations

- Requests reuse their original body when the model is unchanged. Rewritten
  bodies are cached by target model within one request, including retries.
  Model whitespace normalization still rewrites the body when necessary.
  Duplicate or non-canonical top-level model/stream keys are rejected before
  authorization so upstream JSON parsers cannot select a different model.
- Stream and non-stream response readers reuse their timeout timer. Non-stream
  reads transfer ownership of a buffer until its contents have been appended,
  removing the temporary allocation for each chunk. SSE flush behavior and
  first-byte, idle, cancellation, and size limits remain unchanged.
- Concurrent authentication and route cache misses for the same key wait for
  the current load and recheck the cache. Waiting respects cancellation; failed
  loads are not cached. Generation checks prevent stale cache refill after
  local administrative changes.
- Unchanged healthy probes, unchanged discovered model lists, and balance
  updates without a suspension transition no longer invalidate route caches.
  Routing changes still invalidate the cache globally, including empty routes
  that become eligible after recovery. Administrative mutations retain their
  existing invalidation behavior.
- Price caches retain ordered historical versions and choose a rate using the
  request start time, preserving exact-model precedence over aliases and
  inclusive/exclusive validity boundaries. The cache has a 30-second TTL,
  at most 1,024 keys and at most 256 versions per key. A bounded 257-row query
  detects larger histories, which use a cached effective time interval instead.
  Interval boundaries include both exact-model and alias price changes, and
  unknown-price intervals are cached as well. Expired keys are reclaimed at capacity. Local price mutations
  invalidate cached histories and prevent in-flight stale refill. Long requests
  and entries in the same log batch can reuse the same history.
- Dashboard results are cached for three seconds, with concurrent loads
  serialized and returned data copied. Metrics may lag by three seconds;
  the rolling 24-hour window, latency, and failed-request accounting still use
  raw-log SQL rather than incomplete usage aggregates. Successful responses
  must also have no error code, so interrupted streams are excluded.

## Verification

Run with Go 1.26 and an isolated PostgreSQL database:

```sh
go test ./...
go vet ./...
go test -race ./...
GOMAXPROCS=4 go test ./internal/gateway -run '^$' \
  -bench 'Benchmark(HealthyProxyRequest|ReadLargeResponse|RelayManyChunks)$' \
  -benchmem -count 3
go test ./internal/gateway -run '^$' -bench BenchmarkRequestBodyRetries -benchmem
```

Set `DAPI_TEST_DATABASE_URL` to enable database integration tests. Tests cover
cache hits without an available database connection, circuit recovery,
unchanged probes, historical price transitions, price invalidation, dashboard
expiry, canceled load waiters, independent cache copies, and request body reuse.
Additional regression cases cover more than 256 price versions, ambiguous
model keys, interrupted-stream success rates and retained Top-N percentiles.

## Observed allocation baseline

Measured on an AMD EPYC-Milan host in a Go 1.26 container, with GOMAXPROCS=4.
The baseline includes the request-log reliability fixes preceding this work.
These measurements precede the subsequent ambiguous-model validation and
shutdown tracking changes and are retained as historical optimization results.

| Benchmark | Before allocations/op | After allocations/op | Before bytes/op | After bytes/op |
| --- | ---: | ---: | ---: | ---: |
| Healthy proxy, small request | 202 | 187 | approximately 52,800 | approximately 52,100 |
| Non-stream response, 1 MiB | 151 | 24 | approximately 6,504,800 | approximately 5,448,400 |
| SSE, 1,000 events in 128-byte chunks | 15,237 | 14,040 | approximately 992,700 | approximately 893,800 |

Elapsed times varied with host load and some post-change runs were slower.
These runs establish allocation reductions, not improved production latency
or throughput. The request-body microbenchmark compares three redundant
rewrites with three unchanged-body lookups; its zero-allocation optimized path
does not represent total gateway request cost. Production P95, TTFT and SQL
load should be assessed separately under controlled representative traffic.
