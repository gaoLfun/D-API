# D-API 文档

这里的文档按读者分为三组：

| 文档 | 适合阅读时机 |
| --- | --- |
| [API 兼容性](api-compatibility.md) | 接入客户端、确认协议、流式和故障切换行为 |
| [管理 API](admin-api.md) | 自动化管理上游、客户端密钥、用量和告警 |
| [配置](configuration.md) | 修改环境变量、资源限制或数据库连接 |
| [部署、备份和恢复](deployment.md) | 首次部署、升级、迁移和灾备 |
| [故障排查](troubleshooting.md) | 服务无法启动、上游失败或证书异常 |
| [架构](architecture.md) | 理解路由、探测、持久化和安全边界 |
| [第三方声明](../THIRD_PARTY_NOTICES.md) | 查看运行时依赖和捆绑字体许可证 |
| [贡献指南](../CONTRIBUTING.md) | 提交代码、文档或测试 |
| [安全策略](../SECURITY.md) | 私下报告安全漏洞 |
| [发布流程](../RELEASING.md) | 维护者准备版本和 GitHub Release |

## 文档约定

- 文档描述的是 `main` 分支当前行为；发布版本以 [CHANGELOG](../CHANGELOG.md) 为准。
- 管理 API 在 v1.0 前不承诺向后兼容。客户端 API 的兼容范围见 [API 兼容性](api-compatibility.md)。
- 示例中的域名、密码、Token 和密钥均为占位值，不能直接用于生产环境。
- 任何日志、截图和问题复现步骤都必须移除 API Key、密码、Cookie、Authorization 和个人数据。
