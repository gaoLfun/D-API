# 故障排查

[返回文档索引](README.md)

先确认运行目录是包含 `compose.yaml` 的项目目录。命令默认使用 Docker
Compose v2。

## 服务无法启动

```sh
docker compose ps
docker compose logs --tail=100 dapi
docker compose logs --tail=100 postgres
```

- `DAPI_MASTER_KEY is required`：复制 `.env.example` 中的主密钥生成步骤，
  设置为 Base64 编码且恰好 32 字节的值。不要在问题单中粘贴它。
- `first start requires ...`：首次启动必须同时设置管理员用户名和密码；
  密码至少 12 个字符。已有管理员不会被环境变量覆盖。
- `DAPI_DATABASE_URL is required` 或数据库连接失败：检查 PostgreSQL
  是否 healthy、主机/端口/密码/`sslmode` 是否匹配。
- 容器反复重启：查看完整启动日志，确认 `.env` 没有引号错误、变量名拼写
  正确，并检查数据库卷空间。

## 健康检查和入口

```sh
curl -fsS http://127.0.0.1:18083/healthz
```

`/healthz` 只代表 D-API 能访问 PostgreSQL，不代表所有上游健康。默认
`DAPI_BIND=127.0.0.1:18083`，公网请求应通过 Caddy 的 80/443 端口。直连
IP 调试时临时设置 `DAPI_BIND=0.0.0.0:18083`，并保持
`DAPI_TRUST_PROXY=false`；这是明文 HTTP，不适合录入生产密钥。

域名 HTTPS 异常时检查 DNS、80/443 防火墙、Caddy 日志和证书存储卷：

```sh
docker compose logs --tail=100 caddy
docker compose exec caddy caddy validate --config /etc/caddy/Caddyfile
```

使用 Caddy 时才设置 `DAPI_TRUST_PROXY=true`，并确保 18083 端口无法从公网
绕过代理访问。Caddy 会覆盖 `X-Real-IP`；不可信代理下开启该选项会让审计
来源和登录限流可被伪造。

## 请求错误

- `401`：客户端密钥缺失、错误、已禁用，或管理员会话已过期。
- `403`：客户端密钥不允许请求的协议/模型，或管理修改请求的 Origin 不匹配。
- `404`：路由、模型或上游返回了不可重试的 404；检查 Base URL 是否已经
  包含 `/v1`，D-API 不会重复添加 `/v1`。
- `429`：客户端密钥触发每分钟限制，或所有候选上游都返回限流。查看
  `Retry-After` 和管理日志。
- `502`：候选上游全部失败或返回不可用错误。查看日志中的 `attempts`，
  按优先级、健康状态和最后错误逐个测试上游。
- `504`：所有尝试都超时。适当增加上游首包/空闲超时，但先确认上游确实
  在响应；全局最长请求时长仍受 `DAPI_MAX_REQUEST_DURATION` 限制。

流式响应开始后无法安全重放；首字节之前失败才会切换上游。中途断流会
记录失败，但不会拼接或恢复已发送的内容。

## 上游、模型和余额

- 保存上游时报“地址不允许访问内网”：这是出站 SSRF 防护，回环、私网、
  链路本地、组播、CGNAT 和云元数据地址均会拒绝；请使用公开的 HTTPS
  服务地址或配置网络出口，而不是关闭防护。
- 模型列表为空：先在上游编辑页执行“获取模型”。手动选择后模型列表会
  被锁定；取消锁定才允许健康探测更新它。空模型列表表示路由时不限制模型，
  但不会出现在网关 `/v1/models` 列表中。
- 模型测试失败：确认协议与模型匹配、API Key 有权限，并注意测试会消耗
  真实额度。Sub2API 的算术 challenge 需要响应正文包含正确答案。
- 余额显示 unknown/unavailable：NewAPI/Sub2API 的余额接口并非标准协议，
  服务会尝试多个已知路径；接口不存在不会阻止正常请求转发。

## 密钥、备份和恢复

列表只显示客户端密钥前缀。新建密钥后使用复制按钮或 CCSwitch 导入；若旧
密钥返回 `secret_unavailable`，只能重新创建。不要把密钥放入浏览器同步、
截图、Shell history 或监控日志。

数据库备份不包含 `.env`。恢复时必须同时提供创建备份时使用的
`DAPI_MASTER_KEY`，否则数据库可以启动但加密上游、通知和客户端密钥副本
无法解密。完整流程见[部署文档](deployment.md)。
