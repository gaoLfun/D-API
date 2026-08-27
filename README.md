# D-API：面向 NewAPI 和 Sub2API 的 AI API 网关

[![CI](https://github.com/gaoLfun/D-API/actions/workflows/ci.yml/badge.svg)](https://github.com/gaoLfun/D-API/actions/workflows/ci.yml)
[![许可证](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![版本](https://img.shields.io/github/v/release/gaoLfun/D-API?include_prereleases&sort=semver)](https://github.com/gaoLfun/D-API/releases)

[English README](README.en.md) · [文档索引](docs/README.md) · [快速部署](docs/deployment.md)

**多上游路由、自动故障切换、分组密钥、优先级路由与可视化运维。**

D-API 是一个轻量级、自托管的 AI API 网关，连接 NewAPI、Sub2API 及其他
OpenAI/Anthropic 兼容上游。它为客户端提供统一的 OpenAI 和 Anthropic 兼容入口，
在分组范围内按优先级选择上游，并在连接失败、超时或可重试 HTTP 错误时自动切换。

> 本文描述当前 `main` 分支。项目已可用于个人和小团队部署；v1.0 前配置与管理 API
> 仍可能发生不兼容变更，已发布版本请以 [CHANGELOG](CHANGELOG.md) 为准。

![D-API 浅色总览（示例数据）](docs/screenshots/dashboard-light.png)

## 为什么使用 D-API

- **AI API 网关统一入口**：一个 Base URL 接入 OpenAI Responses、Chat Completions
  和 Anthropic Messages 客户端。
- **多上游故障切换**：按数字优先级路由，支持连接/首包/空闲超时、健康检查和熔断。
- **API Key 分组**：客户端密钥绑定分组，分组绑定上游；请求不会越过所属分组。
- **余额保护**：余额耗尽的上游可自动暂停，余额恢复后自动恢复路由。
- **用量与成本分析**：统计请求、Token、缓存命中率、延迟，并按 LiteLLM 或手动价格
  档案估算 USD/CNY 成本。
- **可视化运维**：总览列表和拓扑视图展示密钥、分组决策、Base URL 集群与响应出口。
- **安全边界清晰**：上游凭据加密存储，管理会话独立于客户端 Key，出站请求带 SSRF 防护。

![D-API 请求路由拓扑（示例数据）](docs/screenshots/dashboard-topology-light.png)

## 适用场景

| 场景 | D-API 提供的能力 |
| --- | --- |
| NewAPI / Sub2API 前置网关 | 统一入口、按优先级选择渠道、失败自动切换 |
| 个人或小团队的多模型部署 | 分组隔离客户端 Key，限制协议和模型 |
| OpenAI / Anthropic 兼容客户端接入 | 保留原协议和流式响应，不做协议转换 |
| 上游额度和成本观测 | 余额探测、耗尽暂停、Token/缓存/价格估算 |

## 不适合的场景

D-API 有意保持轻量，不提供多租户组织、转售计费、支付处理、供应商账号登录、
协议转换或分布式限流/调度。需要企业级多区域控制面或完整账单系统时，应选择更适合
该场景的产品。

## 快速开始

正式支持 Linux + Docker Compose v2。公网部署建议准备 DNS 名称和 HTTPS。

```sh
git clone https://github.com/gaoLfun/D-API.git
cd D-API
cp .env.example .env
openssl rand -base64 32
```

将生成值写入 `DAPI_MASTER_KEY`，并设置独立的 `POSTGRES_PASSWORD`、至少 12 位的
`DAPI_ADMIN_PASSWORD` 和 `DAPI_DOMAIN`：

```sh
docker compose up -d --build
docker compose ps
curl -fsS https://api.example.com/healthz
```

首次登录后台后，按以下顺序配置：

1. 添加 NewAPI 或 Sub2API 上游，使用“获取上游模型”检查模型列表。
2. 创建分组并绑定一个或多个上游；数字越小的优先级越高。
3. 创建客户端 API Key 并绑定分组，按需限制协议和模型。
4. 可选：为上游绑定价格档案，用于官方价格估算；该估算不是供应商账单。
5. 使用测试 Key 发起一次低成本请求，再开放给真实客户端。

完整部署、备份、恢复和升级步骤见[部署文档](docs/deployment.md)。

## 客户端请求

使用后台创建的客户端 Key，不要使用上游 API Key：

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

支持的客户端路由：

- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/messages`

D-API 原样转发 JSON 和流式响应，不在协议之间转换。认证、重试、流式限制、响应头
和用量采集详见[API 兼容性](docs/api-compatibility.md)。

## 路由模型

```text
客户端 Key -> 所属分组 -> 按优先级筛选上游 -> 健康/余额/能力检查 -> 请求与故障切换
                         |
                         +-> PostgreSQL（配置、日志、用量、审计和告警）
```

同一个 Base URL 的多个上游 Key 会在后台聚合为一个集群展示，但路由和统计仍按各个
Key 独立处理；详情抽屉会分别显示余额、请求和成本。拓扑视图只是运维摘要，不改变实际
分组成员和优先级规则。详见[架构文档](docs/architecture.md)。

## 常见问题

**客户端 Key 绑定分组后，优先级是否仍然生效？**

生效。网关先读取客户端 Key 绑定的分组，再只在该分组成员中按上游 Key 的数字优先级
从小到大尝试。相同 Base URL 只影响界面聚合，不改变 Key 级路由顺序。

**余额耗尽后会怎样？**

开启余额保护后，自动检查连续两次确认可用余额不大于零时暂停该上游；手动刷新只需一次
确认。余额恢复为正数或无限额度后自动恢复。查询失败不会误判为余额耗尽。

**官方价格估算等于实际账单吗？**

不等于。成本来自请求 Token 用量与管理员绑定的 LiteLLM/手动价格档案，只用于运营估算。
未知价格或缺少 Token 时保留未知并显示覆盖率；历史成本不会因价格更新自动重算。

**可以同时使用 `/v1` 和不带 `/v1` 的 Base URL 吗？**

可以。D-API 会避免重复添加 `/v1`，但仍会对完整 URL 做 SSRF 校验。

## 文档

- [文档索引](docs/README.md)
- [架构与路由](docs/architecture.md) · [English](docs/architecture.en.md)
- [部署、备份和恢复](docs/deployment.md) · [English](docs/deployment.en.md)
- [配置](docs/configuration.md) · [English](docs/configuration.en.md)
- [API 兼容性](docs/api-compatibility.md) · [English](docs/api-compatibility.en.md)
- [管理 API](docs/admin-api.md)
- [故障排查](docs/troubleshooting.md)
- [更新日志](CHANGELOG.md) · [安全策略](SECURITY.md) · [贡献指南](CONTRIBUTING.md)

## 本地开发

需要 Go 1.26、Node.js 26 和 PostgreSQL 17：

```sh
cd web && npm ci && npm run build && cd ..
export DAPI_DATABASE_URL='postgres://dapi:password@127.0.0.1:5432/dapi?sslmode=disable'
export DAPI_MASTER_KEY="$(openssl rand -base64 32)"
export DAPI_ADMIN_USERNAME=admin
export DAPI_ADMIN_PASSWORD='local-password-at-least-12-chars'
go run ./cmd/dapi
```

检查命令：

```sh
go test ./...
go vet ./...
go test -race ./...
(cd web && npm test && npm run build)
docker build -t dapi:local .
```

## 许可证与声明

项目采用 [Apache License 2.0](LICENSE)。D-API 是独立兼容项目，与 NewAPI、Sub2API、
CCSwitch、OpenAI 或 Anthropic 不存在隶属或官方背书关系。
