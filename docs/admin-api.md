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
| `GET/POST/PUT/DELETE` | `/api/admin/keys[/{id}]` | 客户端密钥管理 |
| `GET` | `/api/admin/keys/{id}/secret` | 从加密副本恢复可复制密钥 |
| `GET` | `/api/admin/logs` | 分页请求日志 |
| `GET` | `/api/admin/usage` | 多维度用量汇总 |
| `GET/POST/DELETE` | `/api/admin/channels[/{id}]` | Webhook/邮件渠道 |
| `GET/POST/PUT/DELETE` | `/api/admin/alert-rules[/{id}]` | 告警规则 |
| `GET/PUT` | `/api/admin/settings` | 路由最大尝试次数 |

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

可选字段：`enabled`、`priority`、`models_locked`、`model_aliases`、
`access_token`、`user_id`、`connect_timeout_ms`、`first_byte_timeout_ms`、
`idle_timeout_ms`、`failure_threshold`、`cooldown_seconds`。默认值分别为
优先级 100、连接 5000 ms、首包 60000 ms、空闲 300000 ms、失败阈值 3、
冷却 60 s。仅 NewAPI 使用 `access_token` 和 `user_id` 做兼容的余额查询；
Sub2API 会忽略这两个字段。

更新时，空的凭据字段表示保留已有值；需要清除 NewAPI 余额凭据时传
`clear_balance_credentials: true`。服务会拒绝无效协议、超长字段、非
HTTP(S) URL 和指向回环、私网、链路本地、组播、CGNAT 或云元数据地址的 URL。

成功创建返回 `201 {"id":123}`。列表只返回 `has_api_key`、
`has_access_token`、`has_user_id` 等存在性标记，不返回明文凭据。

模型测试会发送真实的最小请求，可能消耗上游额度。NewAPI 按模型选择
Chat 或 Responses；Sub2API 先做源站 HEAD，再发送带算术 challenge 的模型请求。

## 客户端密钥

创建：

```json
{"name":"本地开发","protocols":["responses"],"models":[]}
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
`status=success|error|5xx|429` 和 `upstream_id`。响应包含请求 ID、模型、
协议、状态、总耗时、TTFB、流式 TTFT、尝试链和可用 Token 字段；请求体和
响应体不会存储。

`GET /api/admin/usage` 支持：

| 参数 | 取值 |
| --- | --- |
| `days` | 1-365，默认 30 |
| `from`/`to` | UTC 日期 `YYYY-MM-DD`，范围最多 365 天 |
| `granularity` | `day`、`week`、`month` |
| `dimension` | `upstream`、`api_key`、`protocol`、`model` 或留空 |
| `top_n` | 1-100，默认 5；其余聚合为“其他” |
| `upstream_id`/`api_key_id`/`protocol`/`model` | 可选筛选条件 |

响应同时提供 `daily`、`items`、`totals` 和 `summary` 字段，包含请求、成功、
输入/输出 Token、缓存读写、缓存命中率、平均耗时和 P95 耗时。缺少上游
usage 字段时对应数值保持未知或为零，不会推测价格。

## 通知、告警和设置

通知渠道 `kind` 为 `webhook` 或 `email`。Webhook 至少需要 JSON 配置中的
`url`；邮件需要 SMTP 主机或 `address`，以及 `to` 收件人。配置会加密存储，
列表接口只返回 `configured: true/false`，不会返回 SMTP 密码、Webhook URL
或自定义请求头。

告警事件包括 `low_balance`、`balance_unavailable`、`error_rate` 和
`latency`。规则的窗口至少 60 秒，冷却至少 60 秒；每条规则可绑定一个
上游。`max_attempts` 位于 1-5，默认 3，控制一次客户端请求最多尝试几个
候选上游。

## 限制与兼容性

管理接口只面向单管理员部署，不提供多租户、角色权限或分布式会话。建议
通过 HTTPS、可信反向代理和内网访问控制保护它；不要把管理 Cookie、日志或
带有 `key` 字段的创建响应发送到第三方监控系统。
