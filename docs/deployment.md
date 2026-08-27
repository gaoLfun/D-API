# 部署、备份和恢复

[English](deployment.en.md)

D-API 当前推荐部署在单台 Linux 主机的 Docker Compose v2 上。服务由 D-API、
PostgreSQL 和可选的 Caddy HTTPS 代理组成，适合个人或小团队运行 NewAPI/Sub2API 前置网关。

## 前置条件

- Docker Engine 与 Compose v2 插件；
- 指向服务器的 DNS A/AAAA 记录；
- 公网 HTTPS 开放 TCP 80、443；
- 允许访问上游和证书服务的出站 HTTPS；
- 足够的 PostgreSQL 持久化空间；
- 防火墙不向公网暴露 PostgreSQL，并限制 D-API 直连端口。

## 首次部署

```sh
git clone https://github.com/gaoLfun/D-API.git
cd D-API
cp .env.example .env
openssl rand -base64 32
```

在 `.env` 中设置 `DAPI_MASTER_KEY`、独立的 `POSTGRES_PASSWORD`、至少 12 位的
`DAPI_ADMIN_PASSWORD` 和 `DAPI_DOMAIN`，然后启动：

```sh
docker compose up -d --build
docker compose ps
curl -fsS https://api.example.com/healthz
```

首次登录后，建议按“上游 → 分组 → 客户端 Key → 模型测试 → 真实请求”的顺序配置。
模型测试会发送真实的小请求，可能消耗上游额度。

`postgres_data` 保存数据库，`caddy_data` 和 `caddy_config` 保存证书与 Caddy 状态。
普通升级不要删除这些命名卷。本地默认访问 `https://localhost` 时，浏览器可能需要信任
Caddy 的本地 CA。

## 代理与直连调试

默认 Caddy 使用 80/443，D-API 仅映射到 `127.0.0.1:18083`。Caddy 是唯一公网入口时
才设置 `DAPI_TRUST_PROXY=true`，并禁止公网访问 18083。

临时直连可设置 `DAPI_BIND=0.0.0.0:18083` 和 `DAPI_TRUST_PROXY=false`。
这是明文 HTTP，只适合排障，不适合录入生产凭据。

## 日常运维

```sh
docker compose ps
docker compose logs -f dapi
docker compose logs -f caddy
curl -fsS https://api.example.com/healthz
```

`/healthz` 只表示 D-API 可访问 PostgreSQL，不代表所有上游健康。日志可能包含模型、
上游、客户端 IP、请求 ID 和耗时，应按敏感运维数据处理。

## 管理员密码重置

```sh
docker compose run --rm \
  -e DAPI_ADMIN_USERNAME=admin \
  -e DAPI_ADMIN_PASSWORD='new-password-at-least-12-chars' \
  dapi reset-password
```

该命令只更新已有管理员并撤销其会话。

## 备份与恢复

```sh
mkdir -p /secure/backups
./deploy/backup.sh /secure/backups/dapi-$(date -u +%Y%m%dT%H%M%SZ).sql.gz
```

数据库 dump 不包含 `.env`、PostgreSQL 角色或主密钥。必须另外保存精确的
`DAPI_MASTER_KEY`、环境配置和外部 DNS/防火墙配置，并定期测试恢复。

备份脚本使用 `pg_dump`、gzip、受限文件权限和原子重命名；失败时不会替换目标文件。
即使应用凭据在数据库中已加密，也应把 SQL 压缩包视为敏感数据并保存异地副本。

恢复前停止服务、重建数据库并导入 dump：

```sh
docker compose stop dapi caddy
docker compose exec -T postgres dropdb --if-exists --force -U dapi dapi
docker compose exec -T postgres createdb -U dapi -O dapi dapi
gzip -dc /secure/backups/dapi-TIMESTAMP.sql.gz \
  | docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U dapi -d dapi
docker compose up -d dapi caddy
```

必须使用备份时的同一主密钥，否则加密上游、通知和客户端 Key 副本无法解密。

恢复完成后依次检查 `docker compose ps`、`/healthz`、公共 HTTPS 入口，并使用测试 Key
发起一次低成本请求。

## 升级与卸载

升级前备份并阅读 [CHANGELOG](../CHANGELOG.md)：

```sh
git pull --ff-only
docker compose up -d --build
docker compose ps
```

启动时会应用幂等数据库结构，但没有自动回滚。保留升级前备份和旧镜像，并验证健康接口、
公共代理以及一次低成本测试请求。

`docker compose down` 保留命名卷；`docker compose down -v` 会删除数据库和 Caddy
数据，仅在确认永久数据丢失时使用。
