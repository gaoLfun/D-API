# 第三方声明

D-API 的源码采用 Apache License 2.0，第三方组件仍按各自许可证提供。以下
列表覆盖运行时和前端直接依赖；完整许可证文本以依赖发行包为准。

| 组件 | 用途 | 许可证 |
| --- | --- | --- |
| `github.com/lib/pq` | PostgreSQL 驱动 | MIT |
| `golang.org/x/crypto` | bcrypt 等密码学实现 | BSD-3-Clause |
| Vue | 管理端 UI | MIT |
| Chart.js | 用量图表 | MIT |
| lucide-vue-next | 图标 | ISC |
| Vite、Vitest、TypeScript | 前端构建和测试 | MIT / Apache-2.0（以各包 LICENSE 为准） |
| Playwright | 视觉测试 | Apache-2.0 |
| Noto Sans CJK 子集字体 | 中文界面字体 | SIL Open Font License 1.1 |

字体来源和 Debian 打包版权信息见
[`web/src/assets/NotoSansCJK-LICENSE.txt`](web/src/assets/NotoSansCJK-LICENSE.txt)。
新增依赖时，请在此表补充名称、用途和许可证，并确认许可证与 Apache 2.0
分发方式兼容。
