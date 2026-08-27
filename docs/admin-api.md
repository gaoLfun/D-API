# 管理 API

D-API 的管理 API 供后台 SPA 和受信任的自动化脚本使用。它不是稳定的
公共 API：v1.0 前字段、校验和响应可能发生不兼容变更。

## 认证与通用约定

- 管理员先调用 `POST /api/admin/login`，服务通过 `HttpOnly` 的
  `dapi_session` Cookie 维持会话。
- 后续请求必须携带该 Cookie。浏览器跨站修改请求还必须有与当前 Host
  一致的 `Origin`；建议脚本始终发送 `Origin`。
- 登录成功后不要把 Cookie 写入日志或命令历史。登出使用
  `POST /api/admin/logout`。
- JSON 请求体最大 2 MiB，服务拒绝未知字段和尾部垃圾 JSON。
- 成功响应使用 JSON；错误统一为：

  ```json
  {"error":{"code":"invalid_request","message":"JSON 请求无效"}}
  ```

管理响应带有 `Cache-Control: no-store`，代理和客户端不应缓存敏感结果。

## 端点

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/api/admin/login` | 使用用户名和密码创建会话 |
| `POST` | `/api/admin/logout` | 撤销当前会话 |
| `GET` | `/api/admin/me` | 当前管理员 |
| `PUT` | `/api/admin/password` | 修改密码并撤销旧会话 |
| `GET` | `/api/admin/dashboard` | 24 小时指标和近 7 日趋势 |
| `GET/POST/PUT/DELETE` | `/api/admin/upstreams[/{id}]` | 上游列表、创建、更新、删除 |
| `POST` | `/api/admin/upstreams/test` | 测试未保存或已保存上游 |
| `POST` | `/api/admin/upstreams/test-model` | 测试一个模型和协议 |
| `POST` | `/api/admin/upstreams/test-models/audit` | 保存批量模型测试汇总 |
| `POST` | `/api/admin/upstreams/{id}/check` | 运行健康检查 |
| `POST` | `/api/admin/upstreams/{id}/balance` | 查询余额/用量 |
| `POST` | `/api/admin/upstreams/{id}/models` | 获取并保存上游模型 |
| `GET/POST/PUT/DELETE` | `/api/admin/groups[/{id}]` | 分组及其上游成员管理 |
| `GET/POST/PUT/DELETE` | `/api/admin/keys[/{id}]` | 客户端密钥管理 |
| `GET` | `/api/admin/keys/{id}/secret` | 从加密副本恢复可复制密钥 |
| `GET` | `/api/admin/logs` | 分页请求日志 |
| `GET` | `/api/admin/usage` | 多维度用量汇总 |
| `GET/POST/DELETE` | `/api/admin/channels[/{id}]` | Webhook/邮件渠道 |
| `GET/POST/PUT/DELETE` | `/api/admin/alert-rules[/{id}]` | 告警规则 |
| `GET/PUT` | `/api/admin/settings` | 路由最大尝试次数 |
| `GET` | `/api/admin/pricing` | 价格档案和 USD/CNY 汇率 |
| `POST/PUT/DELETE` | `/api/admin/pricing/profiles[/{id}]` | 管理价格档案 |
| `POST` | `/api/admin/pricing/refresh` | 同步 LiteLLM 价格 |
| `POST` | `/api/admin/pricing/backfill` | 按历史有效价格回算未知请求成本 |

## 上游

创建和更新使用同一套 JSON 字段。最小示例：

```json
{
  "name": "主 NewAPI",
  "kind": "newapi",
  "base_url": "https://upstream.example.com/v1",
  "api_key": "upstream-secret",
  "protocols": ["responses"],
  "models": ["gpt-5.6"]
}
```

`kind` 必须是 `newapi` 或 `sub2api`；`protocols` 至少包含一个
`responses`、`chat`、`messages`。后台新建表单默认 Sub2API，协议默认
Responses，但直接调用 API 时仍应显式传值。

可选字段：`enabled`、`balance_protection_enabled`、`user_agent`、`priority`、
`models_locked`、`model_aliases`、`access_token`、`user_id`、
`connect_timeout_ms`、`first_byte_timeout_ms`、`idle_timeout_ms`、
`failure_threshold`、`cooldown_seconds`。默认值分别为
优先级 100、连接 5000 ms、首包 60000 ms、空闲 300000 ms、失败阈值 3、
冷却 60 s。仅 NewAPI 使用 `access_token` 和 `user_id` 做兼容的余额查询；
Sub2API 会忽略这两个字段。

`balance_protection_enabled` 默认开启。后台自动余额检查连续两次确认
`status=ok`、非无限额度且 `available<=0` 后，会把该上游标记为
`balance_suspended` 并停止路由；手动刷新余额只需一次确认。余额恢复为正数或无限
额度时自动恢复，查询失败会打断连续计数但不会解除已有暂停。列表响应同时包含
`balance_suspended` 和 `zero_balance_checks`。

`user_agent` 为空时保留客户端请求的 User-Agent；设置后，网关转发、健康检查、
余额查询、模型发现和模型测试都会使用该值。字段最多 256 个字符且不能包含换行。
后台提供默认、Codex 兼容、OpenCode 兼容和自定义四种策略。

余额查询成功后会保留 `used`、`currency` 和 `last_success_at`。后续查询失败或
返回不完整字段时，管理界面仍显示最近一次成功结果，并将当前状态标为未知或不可用；
这不会恢复已暂停的上游，也不会伪造新的成功时间。

更新时，空的凭据字段表示保留已有值；需要清除 NewAPI 余额凭据时传
`clear_balance_credentials: true`。服务会拒绝无效协议、超长字段、非
HTTP(S) URL 和指向回环、私网、链路本地、组播、CGNAT 或云元数据地址的 URL。

成功创建返回 `201 {"id":123}`。列表只返回 `has_api_key`、
`has_access_token`、`has_user_id` 等存在性标记，不返回明文凭据。

上游列表还会返回今日及生命周期的 `today_requests`、`today_tokens`、
`today_cost_usd`、`today_cost_coverage`、`lifetime_requests`、
`lifetime_cost_usd` 和 `lifetime_cost_coverage`。成本仅统计已匹配价格档案且
包含 Token 用量的请求；多个账号聚合展示时，账号扣费和官方估算会在详情抽屉中
分别列出，不会把不同账号的余额或成本混为一项。

模型测试会发送真实的最小请求，可能消耗上游额度。NewAPI 按模型选择
Chat 或 Responses；Sub2API 先做源站 HEAD，再发送带算术 challenge 的模型请求。

## 价格档案与成本估算

上游可通过 `pricing_profile_id` 绑定一个价格档案。价格档案按模型记录输入、
输出、缓存读和缓存写的 **USD / 百万 Token** 单价：

```json
{
  "name": "自定义价格",
  "provider": "Example",
  "source_url": "https://example.com/pricing",
  "source_version": "2026-08",
  "prices": [{
    "model": "example-model",
    "input_usd_per_million": 1,
    "output_usd_per_million": 2,
    "cache_read_usd_per_million": 0.2,
    "cache_write_usd_per_million": 0.4
  }]
}
```

`GET /api/admin/pricing` 返回 `profiles` 和 `usd_cny_rate`（默认 7.2）。
`POST /api/admin/pricing/profiles` 创建档案并返回 `201 {"id":123}`；更新和
删除分别使用 `PUT`、`DELETE`。数据库首次迁移会内置 OpenAI、Anthropic 和
Google Gemini 价格档案，自动从 LiteLLM 的结构化价格文件同步；手动价格档案
可作为未覆盖模型的兜底。

`POST /api/admin/pricing/refresh` 下载 LiteLLM 价格并更新受管档案；下载或解析
失败时保留现有价格，自定义档案永远由管理员维护。价格来源版本使用文件摘要
记录，便于追踪变更。
请求日志和用量统计会在上游已绑定档案、模型匹配且请求包含 Token 用量时计算
`cost_usd`。找不到价格或 Token 缺失时成本为空，并通过覆盖率字段标示未知；
该数值仅用于运营估算，不代表供应商账单，也不执行扣费。

历史请求不会因新增或更新价格档案自动重算。调用
`POST /api/admin/pricing/backfill` 可补齐最近 365 天内的未知成本；请求体字段可选：
`{"from":"2026-08-01","to":"2026-08-26"}`，日期按 UTC 解释。接口只更新
`request_logs.cost_usd` 为空的记录，并同步更新已存在的日/小时聚合，不覆盖已有成本。
如果对应聚合行已被清理，回算不会重建该聚合行。

## 分组与路由范围

分组把一个或多个上游组成独立的路由范围。客户端密钥必须绑定一个启用且至少
包含一个上游的分组；网关只会在该分组成员中按上游优先级尝试，不会回退到其他
分组或全局上游池。

分组接口使用以下字段：`id`、`name`、`enabled`、`upstream_ids`、`key_count`、
`created_at` 和 `updated_at`。创建示例：

```json
{"name":"生产线路","enabled":true,"upstream_ids":[1,2]}
```

- `GET /api/admin/groups` 返回分组列表。
- `POST /api/admin/groups` 创建分组，名称不能为空，且至少绑定一个已存在的上游。
- `PUT /api/admin/groups/{id}` 更新名称、启用状态和成员；停用的分组可以暂时没有成员，
  启用时必须至少保留一个成员。
- `DELETE /api/admin/groups/{id}` 删除分组并返回 `204`。仍有客户端密钥绑定时会返回
  `409 group_has_keys`，必须先把密钥迁移到其他分组。

分组停用或删除上游后，绑定的密钥不会自动迁移；密钥创建和更新会拒绝不可用的
分组。首次启动的数据库迁移会创建“默认分组”，并把已有上游和密钥纳入其中；若
列表仍有历史未分组密钥，需要通过更新接口补填 `group_id`，否则无法获得分组内的
路由候选。

## 客户端密钥

每个密钥必须绑定一个启用且至少包含一个上游的分组。创建和更新请求需传
`group_id`；请求只会在该分组内选择上游。分组停用或无可用上游时沿用网关的
现有错误语义。

创建：

```json
{"name":"本地开发","group_id":1,"protocols":["responses"],"models":[]}
```

成功响应中的 `key` 只应在可信终端短暂显示：

```json
{"id":123,"key":"dapi_...","prefix":"dapi_ab12"}
```

列表永远只返回前缀。`GET /api/admin/keys/{id}/secret` 会从 AES-256-GCM
加密副本恢复明文密钥并设置 `no-store`；这是为了复制和 CCSwitch 导入，不应被
反向代理缓存。旧版本创建且没有加密副本的密钥会返回 `422
secret_unavailable`，必须重新创建。删除密钥会立即使其失效。

## 日志和用量

`GET /api/admin/logs` 支持 `limit`（默认 50，最大 200）、`offset`、
`status=success|error|5xx|429`、`upstream_id` 和 `group_id`。响应包含请求 ID、模型、
协议、状态、总耗时、TTFB、流式 TTFT、尝试链和可用 Token 字段；请求体和
响应体不会存储。

`GET /api/admin/usage` 支持：

| 参数 | 取值 |
| --- | --- |
| `days` | 1-365，默认 30 |
| `from`/`to` | UTC 日期 `YYYY-MM-DD`，范围最多 365 天 |
| `granularity` | `day`、`week`、`month` |
| `dimension` | `upstream`、`api_key`、`group`、`protocol`、`model` 或留空 |
| `top_n` | 1-100，默认 5；其余聚合为“其他” |
| `upstream_id`/`api_key_id`/`group_id`/`protocol`/`model` | 可选筛选条件 |

响应同时提供 `daily`、`items`、`totals` 和 `summary` 字段，包含请求、成功、
输入/输出 Token、缓存读写、缓存命中率、平均耗时、P95 耗时、`cost_usd` 和
成本覆盖率。缺少上游 usage 字段或价格档案时对应成本保持未知，不会推测价格。

## 通知、告警和设置

通知渠道 `kind` 为 `webhook` 或 `email`。Webhook 至少需要 JSON 配置中的
`url`；邮件需要 SMTP 主机或 `address`，以及 `to` 收件人。配置会加密存储，
列表接口只返回 `configured: true/false`，不会返回 SMTP 密码、Webhook URL
或自定义请求头。

告警事件包括 `low_balance`、`balance_unavailable`、`error_rate` 和
`latency`。规则的窗口至少 60 秒，冷却至少 60 秒；每条规则可绑定一个
上游。`max_attempts` 位于 1-5，默认 3，控制一次客户端请求最多尝试几个
候选上游。

余额保护暂停、恢复以及关闭保护导致的恢复会记录为
`upstream_balance_protection` 事件，并发送到已启用通知渠道。这类状态转换由
上游自身状态触发，不需要单独创建告警规则。

## 限制与兼容性

管理接口只面向单管理员部署，不提供多租户、角色权限或分布式会话。建议
通过 HTTPS、可信反向代理和内网访问控制保护它；不要把管理 Cookie、日志或
带有 `key` 字段的创建响应发送到第三方监控系统。
