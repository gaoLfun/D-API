# 发布流程

本文供 D-API 维护者准备 GitHub Release 使用。当前项目仍处于 v0.x，公开
管理 API 和配置可能在小版本中发生不兼容变化。

## 发布前

1. 从 `main` 创建发布分支，确认工作区没有 `.env`、数据库 dump、生产日志
   或构建产物。
2. 更新 `CHANGELOG.md`，将已验证的 `Unreleased` 内容归入新版本，并同步
   README 中的版本和兼容性说明。
3. 检查 `go.mod`、`web/package.json`、Docker 基础镜像和第三方声明是否需要
   更新。依赖升级优先通过 Dependabot PR 完成。
4. 执行完整检查：

   ```sh
   go test ./...
   go vet ./...
   go test -race ./...
   (cd web && npm ci && npm test && npm run build && npm audit --audit-level=high)
   docker compose config --quiet
   docker build --tag dapi:release-candidate .
   git diff --check
   ```

   PostgreSQL 集成测试需要设置 `DAPI_TEST_DATABASE_URL`；CI 会在 PostgreSQL
   服务上运行它们。

5. 在干净环境用 `.env.example` 做一次启动、登录、创建上游、模型测试、
   创建客户端密钥、请求 `/v1/models` 和备份恢复演练。测试凭据必须是临时值。

## 标记和发布

使用 SemVer 标签，例如 `v0.1.1`：

```sh
git tag -a v0.1.1 -m "Release v0.1.1"
git push origin v0.1.1
```

在 GitHub 创建同名 Release，粘贴 changelog 内容，说明数据库/配置兼容性、
升级注意事项和已知限制。不要把 `.env`、镜像导出文件或数据库备份上传到
Release。

## 发布后

- 观察 GitHub Actions 的 CI、Dependabot 和安全告警。
- 在一台非生产主机执行 `git pull --ff-only && docker compose up -d --build`，
  验证 `/healthz`、HTTPS、管理登录和一条真实但低额度的测试请求。
- 生产升级前先备份 PostgreSQL，并保留上一个可用镜像和配置。嵌入式 schema
  迁移没有自动回滚；升级失败时按[恢复文档](docs/deployment.md)回滚数据库
  和镜像。
- 在下一轮迭代开始时清空 `Unreleased`，仅保留尚未发布的条目。
