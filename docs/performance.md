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

## 2026-09 可靠性与容量更新

- 总超时从网关入口起计算，包含数据库鉴权及等待；鉴权另设有界并发准入，容量与全局请求并发上限一致，超额返回 `429 authentication_busy` 和 `Retry-After: 1`。鉴权超时返回 `504 request_timeout`；HTTP/1 拒绝未消费的正文时关闭连接并立即结束读等待，避免慢正文阻塞错误返回。Messages 错误保持其协议格式。
- 代理总超时在响应未提交时返回 504；SSE 终止事件、错误事件及提前 EOF 参与日志与告警判定。已提交响应不能更改 HTTP 状态，错误记录在 `error_code`。
- 错误正文最多排空 64 KiB 或 100 ms 后关闭；下游写入/刷新有独立期限。请求体采用读取前全局加权预算，细节见配置文档。
- 用量报告有 5 秒缓存、同查询合并加载，最多 32 项、单项 1 MiB；返回字节副本，避免调用方污染缓存。日汇总按日期桶/维度先在 PostgreSQL 聚合，耗时分位数仍由 SQL 计算。合并“其他”桶的 P95 保持未知，避免错误地合并分位数。
- Dashboard 仍基于原始日志统计全部请求，含认证/路由失败；用量报表保持已选路由统计口径，不混用两者。
- `schema_migrations` 记录已执行版本；后续数据库变更必须新增迁移版本，不修改已部署版本。首次接管旧数据库时按实际主键结构决定是否升级；随后启动不重建日汇总主键。
- 网关分为转发入口、transport、stream、usage、limits/resources；管理 API 按上游、访问控制、通知、报表资源分文件；前端抽出价格操作、格式/余额工具和运行状态组件，接口类型集中于 `types.ts`。
