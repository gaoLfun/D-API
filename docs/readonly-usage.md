# 长期只读用量接口（v1）

供 Paseo 等轮询客户端使用。中转站对应 D-API `upstreams`，ID 使用数据库 ID 的十进制字符串，改名不改变 ID。此接口不会刷新余额、调用模型、修改中转站或触发探测。只读取已有余额快照与日聚合表；正常业务写入统计的延迟仍然存在。

## 查询

```http
GET /api/readonly/usage HTTP/1.1
Authorization: Bearer <创建时获得的专用凭据>
```

只接受专用 `dapi_usage_` 凭据，不接受管理员会话或 `dapi_` 模型调用密钥。专用凭据不能用来登录管理后台、管理凭据或调用 `/v1/*`。凭据为 32 字节密码学随机数，数据库只保存 SHA-256 哈希；原文仅在创建成功响应出现一次，不支持再次读取，遗失后创建新凭据并撤销旧凭据。

公网必须通过 HTTPS 访问；允许回环 HTTP。部署入口必须继续使用应用自带的 `ProxyHeaders`，仅信任管理员已配置的代理网段；代理须覆盖客户端传入的 `X-Real-IP` 和 `X-Forwarded-Proto`。不要把 HTTP 后端作为公开入口，不要让不可信客户端伪装可信代理。部署不需要新增端口或改防火墙。

建议每 60 秒查询一次。服务端每个凭据最多 10 次/分钟，每个来源 IP 最多 120 次/分钟，固定窗口从首次请求开始，进程重启清零。超限返回 429 和 `Retry-After: 60`。IP 限制在认证前执行，因此被限流的来源可能先收到 429。

第一版**不缓存认证、权限或响应**，返回 `Cache-Control: no-store` 和 `Vary: Authorization`。每次查询在同一事务中验证凭据并读取明确的白名单；权限编辑/撤销通过凭据行锁与查询串行化。撤销或缩权完成后开始的查询不可能获得旧权限数据；已经开始的查询可能完成其原快照，已被客户端收到的数据无法追回。不要在代理/CDN覆盖 `no-store`；插件也不应把上一次成功响应当作撤销后的新查询结果。

白名单为空或所授权中转站均被删除时，返回 `stations: []`，不是所有中转站。仅返回已启用中转站；余额暂停但仍启用的中转站保留展示。按 priority 升序、ID 升序排列。每条 station 新增 enabled、priority、groups（id/name/enabled）字段，仅描述已授权可见站点的所属分组，不返回其他成员或密钥。

## 脱敏成功响应

```json
{
  "version": 1,
  "generated_at": "2026-09-09T08:00:00Z",
  "timezone": "UTC",
  "stations": [
    {
      "id": "1",
      "name": "中转站 A",
      "balance": {
        "status": "ok",
        "available": 42.8,
        "used": 57.2,
        "currency": "USD",
        "unlimited": false,
        "updated_at": "2026-09-09T07:55:00Z"
      },
      "today": {
        "requests": 128,
        "input_tokens": 600000,
        "output_tokens": 60000,
        "total_tokens": 660000,
        "estimated_cost_usd": 2.36,
        "cost_coverage": 0.953125
      }
    }
  ]
}
```

### 余额

| 状态 | 确定规则 |
| --- | --- |
| `ok` | 已存余额状态为 `ok`，且成功数据时间存在，距离本次查询时间不超过 `max(30 分钟, 2 × DAPI_BALANCE_INTERVAL)` |
| `stale` | 已存余额状态为 `ok`，但数据时间缺失或超过上述时限。边界恰好等于时限仍为 `ok` |
| `error` | 最近查询失败、未初始化或其他不能确认成功的状态 |
| `unsupported` | 明确记录不支持余额查询；兼容旧探测器 `unknown` + 固定的“不支持余额 API”标记 |

`balance.updated_at` 优先使用快照的 `last_success_at`，仅成功快照允许回退到其 `updated_at`；绝不使用 HTTP 响应时间、表更新时间或失败尝试时间代替余额数据时间。时间使用带时区的 RFC3339/ISO 8601。

`available`、`used`、`currency`、`updated_at` 未知时显式为 `null`。失败及不支持时不暴露可能混杂历史数据的数值，以上四项均为 `null`，`unlimited` 为 false。`stale` 可携带原成功快照的数据。接口不返回余额原始错误、计划信息、上游 URL、上游密钥或管理员信息。

币种原样使用快照记录的实际值（例如 `USD`、`CNY`、`CREDITS`），不默认 USD、不换算站内积分。`unlimited: true` 时 `available: null`，不能根据余额为零推断限额。`used` 是上游余额快照中的累计已用量，与当日估算费用不是同一口径。

### 当日统计

现有 D-API 日聚合写入和今日仪表盘使用 **UTC**，所以 v1 返回 `timezone: "UTC"`。不采用服务器本地时区、不假定 Asia/Shanghai，也不接受客户端时区覆盖。统计日为查询时刻所在 UTC 日期，从 UTC 零点开始。

- `requests`：既有 `daily_usage` 请求数，沿用现有“有客户端密钥及归属上游的请求”统计口径，不是每次失败重试尝试数。
- `input_tokens` / `output_tokens`：沿用日聚合的输入/输出 Token 总和，缓存读取和写入 Token 不额外累加到 `total_tokens`。
- 为避免未知值冒充零，仅当当日所有请求都有相应 Token 计数时返回该项总和；任何请求缺少输入/输出时，相应项为 `null`。二者均已知时 `total_tokens` 才为二者之和，否则为 `null`。
- 新增 `output_usage_requests` 完整性计数，单条及批量日志写入同时维护。迁移前的旧日聚合无法证明输出完整性，保守返回 `output_tokens: null` 和 `total_tokens: null`，直到新的完整统计日；不从已清理原始日志推断未知数据。输入完整性沿用 `usage_requests`。
- `estimated_cost_usd`：有计价数据请求的 D-API 估算费用之和，**不代表上游真实扣款**；可能只覆盖部分请求。没有任何计价数据时返回 `null`，已知零费用则返回 0。
- `cost_coverage`：`cost_known_requests / requests`，范围 0～1；无请求或数据不一致无法计算时为 `null`。有请求但完全无计价时为 0。
- 无请求时，`requests` 和三个 Token 总数为 0，费用和覆盖率为 `null`。
- `generated_at` 为本次响应快照生成时刻，和余额数据时间独立。

## 错误

沿用接口错误信封，固定消息不包含 SQL、请求凭据或上游原始错误：

```json
{"error":{"code":"invalid_usage_credential","message":"用量查询凭据无效"}}
```

| HTTP | code | 含义 |
| --- | --- | --- |
| 401 | `invalid_usage_credential` | 缺少、错误或已撤销的专用凭据；附 `WWW-Authenticate: Bearer` |
| 403 | `usage_forbidden` | 凭据有效但被禁用 |
| 403 | `https_required` | 非回环 HTTP |
| 429 | `rate_limited` | 超过限流，遵循 `Retry-After` |
| 503 | `usage_unavailable` | 数据库/快照暂不可用，安全重试 |

管理接口另可返回 `invalid_request`（400）、`invalid_station_scope`（400）、`usage_credential_not_found`（404）。认证与来源校验沿用现有管理接口错误码。调用方不得把错误响应当成零余额或零用量。

## 创建、列举、缩权、撤销

以下操作只能由管理员通过**现有登录会话 Cookie**完成，并遵守现有同源校验；不能用用量查询凭据操作。无需修改插件仓库。

1. 使用现有后台登录接口 `POST /api/admin/login` 建立会话，并用 `GET /api/admin/upstreams` 获取允许授权的中转站 ID。
2. 同源请求创建凭据（名称最长 100 字符；白名单必须显式提供，最多 1000 个 ID；不存在的 ID 会使整次操作失败）：

   ```http
   POST /api/admin/usage-credentials
   Content-Type: application/json
   Cookie: dapi_session=<管理员会话>

   {"name":"Paseo 用量插件","station_ids":["1","3"]}
   ```

   成功返回 201：

   ```json
   {"id":"7","credential":"<仅此次显示的专用凭据>","station_ids":["1","3"]}
   ```

   在受控终端/页面把该值直接交给插件的秘密存储，勿粘贴聊天、工单、日志、源码或命令行参数。创建成功但响应丢失时，从列表找到新 ID 并撤销，再重新创建。不要自动无限重试创建。

3. `GET /api/admin/usage-credentials` 返回元数据数组：`id`、`name`、`enabled`、`station_ids`、`created_at`、`revoked_at`；永不返回原文或哈希。
4. `PUT /api/admin/usage-credentials/7` 使用完整替换白名单：

   ```json
   {"enabled":true,"station_ids":["1"]}
   ```

   这会立即删除中转站 3 的授权。`station_ids: []` 是空权限范围（查询成功返回空数组）；`enabled: false` 暂停全部查询并返回 403。未撤销的凭据可再次启用。
5. `DELETE /api/admin/usage-credentials/7` 永久撤销（保留元数据审计），返回 `{"ok":true}`。重复撤销幂等；已撤销凭据不能再启用，后续查询返回 401。

创建/编辑/撤销写入管理审计，仅包含 ID、白名单和启用状态，不记录凭据。管理员自定义名称及站名应是显示名称，不应填入秘密。

## 验证与部署（本次不执行生产操作）

测试需独立 PostgreSQL，通过 `DAPI_TEST_DATABASE_URL` 指向隔离测试数据库，勿使用生产连接。执行：

```sh
go test ./...
go vet ./...
go test -race ./...
go build ./cmd/dapi
```

集成测试会创建并删除独立测试 schema，覆盖真实 HTTP 查询、认证/管理隔离、模型接口拒绝、撤销/缩权、限流、UTC 零点、计价覆盖率、Token 缺失及单条/批量聚合写入。默认无数据库配置时集成测试跳过，交付验收必须配置测试库。

管理员日后部署步骤：

1. 记录当前应用镜像/提交及数据库版本；使用现有受控备份流程备份数据库，配置若需改动先另存带日期的备份。备份包含哈希及既有业务秘密，不上传仓库。
2. 在隔离环境执行上述测试，确认新旧部署参数保持兼容。按现有 Compose 发布流程构建新镜像；不需要新增环境变量、端口或防火墙规则。
3. 由有生产权限的管理员在维护窗口执行现有发布流程。启动迁移会幂等创建 `usage_credentials`、`usage_credential_stations`，并为 `daily_usage` / `hourly_usage` 添加 `output_usage_requests`。现有数据默认 0，不触发外部请求或大规模日志回填。DDL 仍可能等待数据库锁，应按既有维护流程监控。
4. 检查 `/healthz`，使用临时受限凭据通过公网 HTTPS 查询，确认站点范围、UTC 当日数据和 `null` 语义；撤销后确认 401。验证不需要调用模型或手动刷新余额。把正式专用凭据安全交付 Paseo 插件会话，不通过聊天传递。

回滚：管理员切回记录的旧镜像并按原发布流程启动。新增表和列均为增量，可保留，不要在线删表/删数据；旧版本不提供只读路由，插件应识别错误并停止把旧数据当作新结果。保留新增列也不会影响旧统计写入；若回滚期间旧版本写入过数据，再升级后相应日的输出完整性仍会保守为未知。必要时先在新版本撤销正式查询凭据。数据库恢复仅在独立评估、授权后按既有备份流程执行，本次不执行。

### 旧币种快照兼容

旧 Sub2API 解析器会把缺失币种默认成 USD。新解析器取消默认值，在余额快照中保存 `currency_reported` 来源标记；成功查询没有币种时也不继承上次币种。只读响应不暴露该内部标记。对于旧 Sub2API 快照中没有来源标记的 USD，只读接口返回 `currency: null`，直到常规余额探测获得明确币种；本接口不会为此触发刷新。旧快照的 CNY/CREDITS 等非默认币种仍可使用。NewAPI 的 USD 沿用已有协议中明确的美元字段或额度换算口径。

## 本次实现验收记录（2026-09-09）

在 Go 1.26.0、隔离 PostgreSQL 16.15 环境中完成：全量 `go test ./...`、`go vet ./...`、全量 `go test -race ./...`、后端构建均通过。集成测试使用独立 schema，未跳过数据库覆盖。

新二进制在独立回环端口完成健康检查、管理员登录、创建查询凭据、查询成功、撤销及撤销后 401 的实际 HTTP 验证；检查其日志没有查询凭据或测试管理员密码。余额/权限隔离、UTC 日界、缺失输出计数、币种来源等由单元和数据库集成测试覆盖。

未创建生产查询凭据，未部署或重启生产服务，未修改防火墙，未修改 Paseo 插件仓库。测试应用和隔离数据库已停止。

## 展示扩展（2026-09-09）

- station.enabled、priority、groups 供客户端按 D-API 分组和优先级展示；groups 按 ID 排序，未分组为 []。一个站点可出现在多个分组，其 today 始终为上游全量统计，不是该分组独立统计。
- today 新增 known_input_tokens、known_output_tokens、known_total_tokens：已有聚合记录的数量（合计不重复累加缓存 Token），不保证覆盖所有请求。原 input_tokens/output_tokens/total_tokens 仍严格表示完整统计，缺失时为 null。
- input_coverage/output_coverage 为有完整性记录的请求占比，无请求时 null；迁移前输出完整性计数为 0，所以覆盖率可能保守偏低。
- 客户端在 total_tokens 为 null 时可以显示 known_total_tokens，必须标注“已记录、不完整”，不得当作当天完整用量。历史未返回的 Token 不补造。
- 配置 NewAPI 账户登录凭据时，余额探测优先 /api/user/self，再按原逻辑回退；避免已缓存的密钥“不限额”遮住账户余额。账户认证失效时仍可能回退密钥额度，不能把不限额当成账户余额。

## 上游数据展示扩展

station.upstream_today 是最近一次上游成功查询返回的今日统计，缺失为 null。Sub2API /v1/usage 的 usage.today 中 requests/input_tokens/output_tokens/cache_read_tokens/cache_creation_tokens/total_tokens/actual_cost 直接映射；cache_creation_tokens 对外名为 cache_write_tokens。未知字段保持 null，不用 D-API 日聚合补齐。上游未说明时区时 timezone 为 null，不假定 UTC；input 与 cache 是否重叠遵循上游返回，总数不重新计算。实际费用使用 actual_cost，不用原价 cost 替代。负数忽略为未知。

balance.source 标记 sub2api_usage/newapi_account/newapi_token/billing_subscription。旧订阅接口的大额度是不限额标记，不是账户现金余额；其 used 通过 /v1/dashboard/billing/usage 读取 total_usage（美分转美元），仅表示该接口返回的消费口径，不冒称今日费用。NewAPI 账户认证失败仍回退旧接口。

只读接口仍不触发外部查询；定时余额探测同时保存上游今日快照。上游快照失败或不支持时不暴露历史 today，历史成功快照过期时保留数据并通过 balance.status=stale 标记。

### NewAPI / AgentRouter 今日用量

账户认证成功后，通过上游 `/api/log/self/stat` 读取当日 quota，按现有 NewAPI 每美元 500000 quota 的口径换算 actual_cost；通过 `/api/log/self` 消费日志汇总 prompt_tokens 和 completion_tokens。查询范围固定为 UTC+08:00 当日零点至本次探测开始时间，upstream_today.timezone 明确返回 UTC+08:00，与网关 today 的 UTC 口径独立。

兼容仅支持尾斜杠的 NewAPI 路由；只尝试配置上游的固定地址，不跟随重定向 Location，不向其他主机转发账户凭据。每页请求 100 条、最多 20 页，总时限 20 秒。分页总数变化、跨页重复内容、越界记录或未读取完整时，输入输出保持 null，仍可展示独立成功获取的费用。上游明确无记录时请求数和 Token 为 0；日志缺少某项 Token 时该项保持 null，不用网关数据补齐。
