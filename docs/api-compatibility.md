# API 兼容性

[English](api-compatibility.en.md)

D-API 提供 OpenAI 和 Anthropic 兼容协议的轻量转发子集。兼容表示按原协议路由请求和响应，
不代表实现供应商全部端点，也不在协议之间转换。

## 支持的路由

| 路由 | 协议族 | 行为 |
| --- | --- | --- |
| `GET /v1/models` | Models | 从客户端 Key 所属分组的可用上游本地构建模型列表 |
| `POST /v1/chat/completions` | OpenAI Chat | 原样转发 |
| `POST /v1/responses` | OpenAI Responses | 原样转发 |
| `POST /v1/messages` | Anthropic Messages | 原样转发 |

其他 `/v1/*` 路由未实现。

## 客户端认证与分组

所有路由接受 `Authorization: Bearer <DAPI_KEY>`；Messages 也接受 `x-api-key`。
必须使用后台创建的客户端 Key，上游 API Key 不能直接作为 D-API 客户端凭据。

每个客户端 Key 绑定一个启用且至少包含一个上游的分组。请求只在该分组内路由，不会回退
到其他分组或全局池；Key 还可以限制协议和模型。

## 请求与模型处理

代理请求必须是包含非空顶层 `model` 的 JSON 对象，请求体上限 32 MiB。D-API 保留查询参数
和端到端请求头，替换客户端认证头，并移除逐跳头。

模型别名只改写顶层 `model`。上游 Base URL 可以带或不带 `/v1`，D-API 会避免重复添加。
配置固定 User-Agent 后，转发、健康/余额探测、模型发现和模型测试使用同一值。

`GET /v1/models` 返回排序后的 OpenAI 风格列表，并应用客户端 Key 的分组和模型限制。

## 优先级与故障切换

候选上游按数字优先级从小到大尝试，受 `max_attempts` 限制。以下情况会尝试下一个上游：

- 连接、首包、响应空闲超时；
- 首字节发出前的传输或读取失败；
- HTTP 401、403、404、429 或 5xx。

所有上游限流时返回 429，全部超时时返回 504，其他耗尽路径返回 502。已停用、余额暂停、
熔断中或能力不匹配的上游不会进入候选。

相同 Base URL 的 Key 在后台聚合显示，但仍是独立候选，优先级也继续按 Key 生效。

## 流式响应

使用协议原生的 `stream: true`。首字节发出前可以故障切换；一旦响应已提交，中途断流不能
安全重放。D-API 不重建事件、不恢复流，也不合并多个上游的部分输出。

## 响应头

| 响应头 | 含义 |
| --- | --- |
| `X-DAPI-Request-ID` | 日志关联请求 ID |
| `X-DAPI-Upstream` | 最终或最后尝试的上游名称 |
| `X-DAPI-Attempts` | 已尝试上游数量 |

## 用量、缓存和成本

D-API 从非流式 JSON 或 SSE 事件中的常见 `usage` 字段读取输入、输出、缓存读、缓存写 Token。
后台记录总耗时、TTFB、可观测的流式 TTFT 和尝试链，并支持按上游、客户端 Key、分组、协议
或模型分析日/周/月用量、平均/P95 延迟和缓存命中率。

绑定价格档案后，按请求发生时的价格估算 `cost_usd`。价格或 Token 未知时成本保持未知，
并以覆盖率表达数据完整度。官方价格估算不是供应商账单，不进行客户端扣费。

## 余额发现

余额接口不是 OpenAI/Anthropic 标准。D-API 会尝试已知的 NewAPI/Sub2API 兼容路径；上游不支持
时显示未知，但不影响正常模型请求。NewAPI 可选 Access Token 和 User ID 仅用于余额查询，
正常转发仍使用上游 API Key。

## 错误格式

Chat/Responses 网关错误使用 OpenAI 风格 `error` 对象；Messages 使用 Anthropic 风格错误。
非重试的上游响应原样返回，不合成供应商专有字段。
