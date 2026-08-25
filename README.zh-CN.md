# D-API

[English](README.md)

D-API 是面向 NewAPI 和 Sub2API 上游的轻量级单管理员网关。它按优先级路由已支持的 AI API 请求，并在连接失败、超时或遇到可重试 HTTP 错误时尝试下一个可用上游。

当前版本：**v0.1.0**。项目已经可用，但配置和管理 API 在 v1.0 前仍可能发生不兼容变更。

## 功能

- 按优先级路由 NewAPI 和 Sub2API 上游
- 自动故障切换、健康检查、熔断和每上游独立超时
- 支持 OpenAI Responses、OpenAI Chat Completions 和 Anthropic Messages 接口
- 模型探测、允许列表和客户端模型到上游模型的别名
- 上游连通性测试和尽力而为的余额查询
- 本地记录请求尝试过程和 Token 用量
- 可限制协议和模型的客户端 API Key
- 带审计日志和登录限速的单管理员后台
- 邮件及 Webhook 通知，可配置告警规则
- 一个 Go 服务、一个 Vue 前端、PostgreSQL，以及可选的 Caddy 代理

## 快速开始

正式支持的部署环境为 Linux 和 Docker Compose v2。公网部署建议使用域名。

```sh
cp .env.example .env
openssl rand -base64 32
```

将生成值写入 `DAPI_MASTER_KEY`，并在 `.env` 中设置独立的 `POSTGRES_PASSWORD` 和至少 12 位的管理员密码。主密钥不会存入数据库备份，必须另行保管；丢失后将无法读取已保存的上游和通知凭据。

```sh
docker compose up -d --build
docker compose ps
```

将 `DAPI_DOMAIN` 设置为已解析到服务器的域名，并开放 TCP 80、443 端口。Caddy 会自动申请和续期 HTTPS 证书。本地部署默认访问 `https://localhost`，浏览器可能需要信任其本地 CA。

使用 `DAPI_ADMIN_USERNAME` 和 `DAPI_ADMIN_PASSWORD` 登录，然后：

1. 添加 NewAPI 或 Sub2API 上游，保存前使用“测试连接”。
2. 选择支持的协议和模型。优先级数字越小越先路由。
3. 创建客户端 API Key。明文只显示一次。

生产部署、IP 直连、升级、备份和恢复详见[部署文档](docs/deployment.md)。

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
客户端 -> Caddy (HTTPS) -> D-API -> 按优先级排序的上游
                                |
                                +-> PostgreSQL
```

Go 进程同时提供管理后台和 API 网关，并运行健康/余额探测、告警评估，将运行状态存入 PostgreSQL。上游凭据和通知渠道密钥使用 AES-256-GCM 加密后入库；请求正文不会存储。

路由、健康状态、持久化和安全边界详见[架构文档](docs/architecture.md)。

## 文档

- [架构](docs/architecture.md)
- [配置](docs/configuration.md)
- [部署、备份和恢复](docs/deployment.md)
- [API 兼容性](docs/api-compatibility.md)
- [贡献指南](CONTRIBUTING.md)
- [安全策略](SECURITY.md)
- [支持策略](SUPPORT.md)
- [更新日志](CHANGELOG.md)

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

## 项目边界

D-API 有意保持专用和轻量。v0.1.0 **不以以下功能为目标**：

- 多租户账号或组织
- 转售计费、额度扣费或支付处理
- 登录 OpenAI、Anthropic、NewAPI 或 Sub2API 用户账号
- 协议转换或通用反向代理
- 稳定的公开管理 API

## 许可证

项目采用 [Apache License 2.0](LICENSE)。Copyright 2026 gaoLfun。
