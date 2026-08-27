# D-API 文档

D-API 是面向 NewAPI、Sub2API 和 OpenAI/Anthropic 兼容上游的轻量级 AI API 网关。
本索引按“接入 → 配置 → 运维 → 自动化”组织，默认以中文为主；核心文档提供英文备用版本。

## 从这里开始

| 你的目标 | 推荐阅读 |
| --- | --- |
| 先部署一个可用网关 | [部署、备份和恢复](deployment.md) |
| 接入 OpenAI / Anthropic 客户端 | [API 兼容性](api-compatibility.md) |
| 配置分组、Key 和优先级路由 | [架构与路由](architecture.md)、[管理 API](admin-api.md) |
| 配置环境变量和运行限制 | [配置](configuration.md) |
| 排查 401、502、504、余额或证书问题 | [故障排查](troubleshooting.md) |

## 核心文档

| 中文 | English |
| --- | --- |
| [架构与路由](architecture.md) | [Architecture](architecture.en.md) |
| [部署、备份和恢复](deployment.md) | [Deployment](deployment.en.md) |
| [配置](configuration.md) | [Configuration](configuration.en.md) |
| [API 兼容性](api-compatibility.md) | [API Compatibility](api-compatibility.en.md) |

## 管理与维护

- [管理 API](admin-api.md)：自动化管理上游、分组、客户端 Key、价格、用量和告警。
- [故障排查](troubleshooting.md)：服务、入口、请求、上游、模型、余额和密钥问题。
- [截图与界面说明](screenshots/README.md)：总览、拓扑和后台操作界面。
- [更新日志](../CHANGELOG.md)
- [贡献指南](../CONTRIBUTING.md)
- [安全策略](../SECURITY.md)
- [支持策略](../SUPPORT.md)
- [发布流程](../RELEASING.md)
- [第三方声明](../THIRD_PARTY_NOTICES.md)

## 文档约定

- 文档描述 `main` 分支当前行为；发布版本以 [CHANGELOG](../CHANGELOG.md) 为准。
- 管理 API 在 v1.0 前不承诺向后兼容；客户端 API 的兼容范围见[API 兼容性](api-compatibility.md)。
- 示例中的域名、密码、Token 和密钥均为占位值，不能直接用于生产环境。
- 截图只使用脱敏的示例数据；提交问题、日志或截图前请移除 API Key、密码、Cookie、
  Authorization 和个人数据。
