# Paseo 中转站用量插件

Paseo 侧边栏的「中转站用量」面板。已在 Paseo 0.7.2 验证，支持分组筛选、D-API 排序、隐藏禁用上游、窄屏和行详情。

## 数据来源与刷新

默认「上游数据」展示 D-API 保存的上游余额、今日输入/输出 Token 和实际费用；「网关记录」单独展示 D-API 本地统计，不把两种口径混合。未知数据显示破折号，明确的零用量显示 0。上游与网关日界分别以接口返回的时区为准；网关费用为估算，不能等同于上游实际扣款。

面板加载或点击「刷新」时请求 `/api/readonly/usage`，目前没有定时刷新。每次查询同步已授权站点的启用状态、分组和排序；新增站点需加入查询凭据的白名单。余额及上游今日用量由 D-API 定时探测，默认间隔 10 分钟，面板刷新不会触发上游探测。接口限流时遵循 Retry-After，认证失败清空旧数据。

完整协议及凭据创建方式见 [只读用量接口](../../docs/readonly-usage.md)。

## 安装与验证

需要 Node.js 22.18+、npm、Paseo 0.7.2，以及已启用只读接口的 D-API。在 D-API 仓库根目录执行：

```sh
cd plugins/paseo-relay-usage
npm ci --legacy-peer-deps
npm run typecheck
npm test
paseo plugin install "$PWD"
```

安装目录必须长期保留；这是本地目录插件，Paseo 从该路径加载源码。CLI 连接使用已有 Paseo 认证方式，不把密码写入脚本。未来 Paseo 插件 API 变化时需重新验证兼容性。

## 配置凭据

D-API 管理员创建限制站点范围的专用只读凭据，安全交付到运行 Paseo 用户的 `~/.config/paseo-relay-usage/credential`，目录权限 0700、文件权限 0600。文件只包含凭据原文；不要写入仓库、前端或日志。

插件每次查询读取该文件，不需要为更换文件中的凭据而重启 daemon。也支持以下服务端环境变量：

- `PASEO_RELAY_DAPI_USAGE_CREDENTIAL`：专用只读凭据，优先于文件。
- `PASEO_RELAY_DAPI_URL`：D-API 地址，当前默认 `https://dapi.gaozhiyi.com`；远程必须 HTTPS。

环境变量必须由运行 Paseo 的服务进程获得，在另一个终端 export 不会改变已运行 daemon 的环境。不使用管理员登录凭据或模型调用密钥。

## 更新与恢复

仓库中的此目录保存插件的完整源码和依赖锁文件；不保存 node_modules、构建产物、历史临时备份及认证配置。Paseo 或 D-API 的常规更新不会主动覆写原有独立插件目录，但手动替换源码或更新此 Git 工作区会改变其中的文件。

当前生产插件仍从 `/home/paseo/workspace/Others/paseo-relay-usage` 加载。本次归档没有改动生产加载路径；提交仓库不等于自动更新正在运行的独立副本。

若要从仓库恢复，先把此目录复制到一个长期保留的新目录，按上述步骤安装依赖、验证，再使用 `paseo plugin install` 注册恢复目录。覆盖原目录或切换插件配置前先备份原目录及 Paseo 配置。凭据文件独立保存，须沿用现有文件或由管理员重新交付。

已使用仓库目录安装的用户，在更新源码并完成验证后执行：

```sh
paseo plugin reload relay-usage
```

本地目录插件不通过 `paseo plugin update` 自动拉取代码。回退时从指定历史提交取出本目录到新的恢复目录，验证后重新注册即可，无需回退整个 D-API 服务。
