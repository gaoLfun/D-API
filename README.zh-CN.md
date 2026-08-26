# D-API

[![CI](https://github.com/gaoLfun/D-API/actions/workflows/ci.yml/badge.svg)](https://github.com/gaoLfun/D-API/actions/workflows/ci.yml)
[![许可证](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![版本](https://img.shields.io/github/v/release/gaoLfun/D-API?include_prereleases&sort=semver)](https://github.com/gaoLfun/D-API/releases)

[English](README.md)

D-API 是面向 NewAPI 和 Sub2API 上游的轻量级单管理员网关。它按优先级路由已支持的 AI API 请求，并在连接失败、超时或遇到可重试 HTTP 错误时尝试下一个可用上游。

当前版本：**v0.1.0**。项目已经可用，但配置和管理 API 在 v1.0 前仍可能发生不兼容变更。

## 功能

- 按优先级路由 NewAPI 和 Sub2API 上游
- 自动故障切换、健康检查、熔断和每上游独立超时
- 支持 OpenAI Responses、OpenAI Chat Completions 和 Anthropic Messages 接口
- 模型探测、允许列表和客户端模型到上游模型的别名
- 上游连通性测试、余额查询和余额耗尽自动暂停/恢复
- 本地记录请求尝试过程和 Token 用量
- 可按分组隔离上游，并限制协议和模型的客户端 API Key
- 可选价格档案，提供 USD 成本估算和计价覆盖率
- 带审计日志和登录限速的单管理员后台
- 邮件及 Webhook 通知，可配置告警规则
- 一个 Go 服务、一个 Vue 前端、PostgreSQL，以及可选的 Caddy 代理
- 有界并发、按密钥限流、请求时长限制和 SSRF 防护

## 快速开始

正式支持的部署环境为 Linux 和 Docker Compose v2。公网部署建议使用域名。

```sh
cp .env.example .env
openssl rand -base64 32
```

将生成值写入 `DAPI_MASTER_KEY`，并在 `.env` 中设置独立的 `POSTGRES_PASSWORD` 和至少 12 位的管理员密码。主密钥不会存入数据库备份，必须另行保管；丢失后将无法读取已保存的上游、通知凭据和客户端密钥副本。

```sh
docker compose up -d --build
docker compose ps
```

将 `DAPI_DOMAIN` 设置为已解析到服务器的域名，并开放 TCP 80、443 端口。Caddy 会自动申请和续期 HTTPS 证书。本地部署默认访问 `https://localhost`，浏览器可能需要信任其本地 CA。

Compose 默认将 D-API 映射到 `127.0.0.1:18083`，并保持
`DAPI_TRUST_PROXY=false`。如果 Caddy 是唯一公网入口，建议设置
`DAPI_TRUST_PROXY=true`，这样审计日志和登录限流可以使用真实客户端 IP；
开启代理信任后不要再把 18083 端口暴露到公网。临时使用 IP 直连 HTTP 的方法
见[部署文档](docs/deployment.md)。

使用 `DAPI_ADMIN_USERNAME` 和 `DAPI_ADMIN_PASSWORD` 登录，然后：

1. 添加 NewAPI 或 Sub2API 上游，保存前使用“获取上游模型”检查模型列表。
2. 在模型列表中选择模型后，可对单个模型或已选模型执行“测试模型”；这会发起真实但很小的模型请求（Sub2API 使用算术 challenge），可能消耗上游额度。
3. 创建分组并绑定一个或多个上游；客户端请求只会在所属分组内路由。
4. 可选：为上游绑定价格档案，用于估算成本；该估算不代表供应商账单。
5. 选择支持的协议和模型。优先级数字越小越先路由。
6. 创建客户端 API Key 并选择分组。列表默认只显示前缀，可用复制按钮或导入 CCSwitch；旧版本创建且未保存加密副本的密钥需要重新创建。

生产部署、IP 直连、升级、备份和恢复详见[部署文档](docs/deployment.md)。
需要脚本化管理时参阅[管理 API](docs/admin-api.md)。

## 客户端请求

请使用后台创建的客户端 Key，不要使用上游 API Key。

```sh
export DAPI_BASE_URL=https://api.example.com
export DAPI_KEY=dapi_replace_me

curl "$DAPI_BASE_URL/v1/models" \
  -H "Authorization: Bearer $DAPI_KEY"

curl "$DAPI_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $DAPI_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4.1-mini","messages":[{"role":"user","content":"hello"}]}'
```

支持以下路由：

- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/messages`

D-API 原样转发 JSON 和流式响应，不在协议之间转换。认证方式、重试行为、流式限制、用量采集和响应头详见 [API 兼容性](docs/api-compatibility.md)。

## 架构

```text
客户端 -> Caddy (HTTPS) -> D-API -> 分组内按优先级排序的上游
                                |
                                +-> PostgreSQL
```

Go 进程同时提供管理后台和 API 网关，并运行健康/余额探测、告警评估，将运行状态存入 PostgreSQL。上游凭据和通知渠道密钥使用 AES-256-GCM 加密后入库；请求正文不会存储。

路由、健康状态、持久化和安全边界详见[架构文档](docs/architecture.md)。

## 生产检查清单

- 使用 HTTPS，并将管理员后台限制在可信网络内。
- 将 `DAPI_MASTER_KEY`、`.env`、数据库备份和客户端密钥创建响应排除在
  代码仓库和普通日志之外。
- 外部 PostgreSQL 使用适当的 `sslmode`；Compose 的 `disable` 仅适用于
  Docker 私有网络。
- 检查上游模型允许列表，启用流量前先测试模型。模型测试是真实请求，可能
  消耗上游额度。
- 设置日志保留时间并定期验证备份。只有数据库 dump 而没有对应主密钥时，
  无法恢复加密凭据。
- 暴露直连端口前阅读[安全策略](SECURITY.md)和[故障排查](docs/troubleshooting.md)。

## 文档

- [架构](docs/architecture.md)
- [配置](docs/configuration.md)
- [部署、备份和恢复](docs/deployment.md)
- [API 兼容性](docs/api-compatibility.md)
- [管理 API](docs/admin-api.md)
- [故障排查](docs/troubleshooting.md)
- [文档索引](docs/README.md)
- [贡献指南](CONTRIBUTING.md)
- [安全策略](SECURITY.md)
- [支持策略](SUPPORT.md)
- [更新日志](CHANGELOG.md)
- [第三方声明](THIRD_PARTY_NOTICES.md)
- [发布流程](RELEASING.md)

## 本地开发

需要 Go 1.26、Node.js 26 和 PostgreSQL 17。启动 Go 服务前先构建前端：

```sh
cd web
npm ci
npm run build
cd ..

export DAPI_DATABASE_URL='postgres://dapi:password@127.0.0.1:5432/dapi?sslmode=disable'
export DAPI_MASTER_KEY="$(openssl rand -base64 32)"
export DAPI_ADMIN_USERNAME=admin
export DAPI_ADMIN_PASSWORD='local-password-at-least-12-chars'
go run ./cmd/dapi
```

运行项目检查：

```sh
go test ./...
go vet ./...
go test -race ./...
(cd web && npm test && npm run build && npm audit --audit-level=high)
docker build -t dapi:local .
```

只有设置 `DAPI_TEST_DATABASE_URL` 后才会运行 PostgreSQL 集成测试。

CI 会在每个 Pull Request 上运行同一套前后端检查。维护者发布版本时请遵循
[发布流程](RELEASING.md)。

## 项目边界

D-API 有意保持专用和轻量。v0.1.0 **不以以下功能为目标**：

- 多租户账号或组织
- 转售计费、额度扣费或支付处理
- 登录 OpenAI、Anthropic、NewAPI 或 Sub2API 用户账号
- 协议转换或通用反向代理
- 稳定的公开管理 API

D-API 是独立兼容项目，与 NewAPI、Sub2API、CCSwitch、OpenAI 或 Anthropic
不存在隶属或官方背书关系。

## 许可证

项目采用 [Apache License 2.0](LICENSE)。Copyright 2026 gaoLfun。
捆绑字体和依赖的许可证见[第三方声明](THIRD_PARTY_NOTICES.md)。
