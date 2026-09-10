# 配置

[English](configuration.en.md)

D-API 从环境变量读取进程和数据库配置；上游、客户端 Key、分组、价格、通知和告警
通过管理后台写入 PostgreSQL。修改环境变量后需要重建 `dapi` 容器。

## 必填配置

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `DAPI_MASTER_KEY` | 无 | Base64 编码的 32 字节 AES-256-GCM 主密钥 |
| `DAPI_ADMIN_USERNAME` | 无 | 首次创建管理员的名称 |
| `DAPI_ADMIN_PASSWORD` | 无 | 首次密码或重置密码，至少 12 个字符 |
| `POSTGRES_PASSWORD` | 无 | Compose PostgreSQL 密码 |

用 `openssl rand -base64 32` 生成主密钥。主密钥不写入数据库备份，必须单独保管。

## 应用变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `DAPI_ADDR` | `:8080` | Go HTTP 服务监听地址 |
| `DAPI_WEB_DIR` | `web/dist` | Vue 构建产物目录 |
| `DAPI_SESSION_TTL` | `24h` | 管理员会话有效期 |
| `DAPI_LOG_RETENTION` | `720h` | 请求日志、审计和告警保留时间 |
| `DAPI_DAILY_USAGE_RETENTION` | `8760h` | 日汇总独立保留 365 天 |
| `DAPI_HOURLY_USAGE_RETENTION` | `2160h` | 小时汇总独立保留 90 天 |
| `DAPI_DOWNSTREAM_WRITE_TIMEOUT` | `30s` | 单次响应写入/刷新期限，受请求总时限约束 |
| `DAPI_MAX_BUFFERED_RESPONSE_BYTES` | `536870912` | 全局非流式响应缓冲容量上限（字节），扩容时同时计入新旧缓冲 |
| `DAPI_MAX_BUFFERED_REQUEST_BYTES` | `536870912` | 全局请求体加权内存预留上限（字节） |
| `DAPI_BALANCE_INTERVAL` | `10m` | 自动余额检查间隔 |
| `DAPI_HEALTH_INTERVAL` | `30s` | 自动健康检查间隔 |
| `DAPI_MAX_REQUEST_DURATION` | `15m` | 单个代理请求最长生命周期 |
| `DAPI_TRUST_PROXY` | `false` | 是否信任 Caddy 写入的 `X-Real-IP` |
| `DAPI_TRUSTED_PROXY_CIDRS` | 空 | 可选的受信代理 CIDR 列表，逗号分隔 |

网关资源限制：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `DAPI_MAX_CONCURRENT_REQUESTS` | `256` | 全局最大并发请求数 |
| `DAPI_MAX_CONCURRENT_PER_KEY` | `32` | 每个客户端 Key 最大并发数 |
| `DAPI_MAX_REQUESTS_PER_MINUTE` | `600` | 每 Key 固定窗口分钟限流 |

时间使用 Go duration，例如 `30s`、`10m`、`24h`。无效或非正数回退到默认值；进程还会
限制极端配置：全局并发 10,000、单 Key 并发 1,000、每分钟 100,000、请求最长 24 小时。

只有确定客户端无法绕过可信代理访问 D-API 时才启用 `DAPI_TRUST_PROXY=true`。

## 数据库变量

设置完整 `DAPI_DATABASE_URL`，或使用拆分变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `DAPI_DATABASE_URL` | 无 | PostgreSQL URL，优先于拆分变量 |
| `DAPI_DATABASE_HOST` | `postgres` | 数据库主机 |
| `DAPI_DATABASE_PORT` | `5432` | 数据库端口 |
| `DAPI_DATABASE_NAME` | `dapi` | 数据库名称 |
| `DAPI_DATABASE_USER` | `dapi` | 数据库用户 |
| `DAPI_DATABASE_PASSWORD` | 无 | 拆分配置时的密码 |
| `DAPI_DATABASE_SSLMODE` | `disable` | lib/pq SSL 模式 |

外部 PostgreSQL 应使用适合服务器的 SSL 模式，不要直接照搬 Compose 的 `disable`。

## Compose 变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `POSTGRES_PASSWORD` | 必填 | PostgreSQL 容器密码 |
| `DAPI_DOMAIN` | `localhost` | Caddy HTTPS 站点地址 |
| `DAPI_BIND` | `127.0.0.1:18083` | 主机到 D-API 容器端口的映射 |

`DAPI_BIND` 是 Compose 插值，不是 Go 进程环境变量。临时直连 HTTP 时必须关闭代理信任，
并用防火墙限制端口。

## 后台配置项

后台保存以下运行配置：上游 URL、凭据、User-Agent、优先级、协议、模型、别名、超时、
失败阈值、冷却时间、余额保护、分组成员、客户端 Key、价格档案、USD/CNY 汇率、最大尝试次数、
通知渠道和告警规则。

客户端 Key 只会在所属分组内路由。相同 Base URL 的多个 Key 仅在界面聚合，路由和成本仍按
Key 分别处理。价格估算是运营参考，不执行扣费；未知价格或缺失 Token 时显示未知和覆盖率。

清理任务启动时执行，之后每小时运行一次；同一数据库 schema 的实例通过咨询锁互斥。每张表最多运行 15 秒，每批删除最多 1000 条并退让 25ms，积压留待下一轮继续。按 `DAPI_LOG_RETENTION` 分批删除请求日志、审计和告警事件；日汇总和小时汇总分别按独立保留期清理，会话按到期时间删除。原始日志清理后，对应平均耗时/P95 不再可用，日汇总仍保留。独立的上游生命周期请求与估算成本累计不受普通日志保留期影响；清理任务不能
替代 PostgreSQL 备份或大型部署需要的分区策略。

## 测试变量

`DAPI_TEST_DATABASE_URL` 启用 PostgreSQL 集成测试。测试会在数据库内创建隔离 schema，
未设置时自动跳过。

请求体预算在读取前按声明长度的 8 倍预留，用于正文、解析及模型重写；未知长度按 32 MiB 正文上限预留。默认 512 MiB 预算最多同时接纳两个未知长度的大请求；预算不足返回 429。此预算约束请求体缓冲，不等同于进程 RSS 上限，部署仍应设置容器内存限制。

管理台默认请求超时 15 秒；模型测试按配置连接/首包时限加余量，余额/健康/模型查询 120 秒，价格同步 45 秒，回算 300 秒，用量查询 60 秒。反向代理应允许这些操作的处理时间；取消模型测试或关闭客户端模拟窗口会中止客户端请求。

非流式响应按实际缓冲容量申请独立预算，默认 512 MiB；扩容时必须同时容纳旧缓冲和新缓冲。预算不足在响应头发出前返回 `503/response_budget_exceeded` 和 `Retry-After: 1`，不重试其他上游、不计上游故障。成功、读失败、取消或写失败都会释放预留。单响应仍限制 32 MiB，流式正文不做全量缓冲。此预算不覆盖 JSON 解析、TLS、Go GC 等其他内存，也不是 RSS 硬上限。

日志队列满时，同步回退最多 4 个写入、64 个等待者，排队上限 100ms；每次写入仍采用原有两秒期限及有限重试。等待容量不足、超时或关闭时该条日志计入累计丢弃；这是一项明确的过载取舍，不承诺数据库故障期间零日志损失。运维面板显示当前同步写入/等待数量、累计回退/容量拒绝、累计等待和总耗时。
