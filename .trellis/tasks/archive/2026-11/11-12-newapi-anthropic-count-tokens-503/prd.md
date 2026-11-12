# 修复 Claude Code count_tokens 在 New API 渠道返回 503

## Goal

Anthropic 协议的 `POST /v1/messages/count_tokens` 在「New API」渠道上没有注册路由，
Claude Code 每轮都会调用该端点，于是每次都拿到 `503 no_available_candidate`，
在请求日志里表现为「0ms、无分组、无上游账号」的失败行。

## Background

线上现象（10.18.6.186，近 24 小时请求日志）：

- 成功的 Anthropic 请求：分组 xjc、渠道 New API、模型 `claude-gpt-6.1-sol`、流式、思考开启、
  耗时数十秒。
- 失败请求：同一访问密钥与模型，`HTTP 503`、`no_available_candidate`、耗时 0ms、
  「思考/流式」显示 `— / 否`、分组与上游账号均为空，且成簇出现在同一秒。

代码链路：

1. `internal/dialect/anthropic.go` 把 `/v1/messages/count_tokens` 解析为
   `OperationCountTokens`，强制 `Stream=false`，并在解析 reasoning 之前提前返回，
   因此日志里思考为空、流式为「否」。
2. `internal/channel/modules/newapi.go` 只声明了 Anthropic 的
   `OperationChatCompletion` 与 `OperationProbe`，没有 `OperationCountTokens`。
3. `internal/scheduler/inspect.go` 的 `evaluateTargets` 在
   `byOperation[query.operation]` 缺失时返回 `ReasonOperationUnsupported`，目标集合为空。
4. `internal/gateway/handler.go` 只对 `model_required_by_filter` / `advisor_model_unavailable`
   做专门映射，其余静态原因一律落到最终兜底 `reasonNoCandidate`（503）。

对照：同为多协议网关的 `cliproxyapi` 渠道声明了 Anthropic/Gemini 的
`OperationCountTokens`（`RouteNative`）；`gpt_load`、`claude`、`anthropic` 等渠道也有该路由。
执行层（`internal/execution/bifrost/passthrough.go`）已支持 Anthropic count_tokens 的
原生透传，`providerSupportsPassthrough(ProviderMultiProtocolGateway)` 为真，缺失的只有渠道声明。

外部佐证：Claude Code 每轮都会调用 `/v1/messages/count_tokens`；该端点返回 404 时
Claude Code 会退化为向 `/v1/messages` 发送大量伪计数请求。New API 上游在较新版本
（v1.0.0-rc.31 起）已实现该端点。

## Requirements

- R1：「New API」渠道为 Anthropic 客户端协议声明 `OperationCountTokens`，路由模式为
  `RouteNative`（与同渠道的 Anthropic `OperationChatCompletion` 保持一致）。
- R2：`/v1/messages/count_tokens` 请求经该渠道时能命中路由并透传到上游
  `{base_url}/v1/messages/count_tokens`，上游模型名使用分组配置的上游模型 ID
  （不是客户端别名）。
- R3：上游明确不支持该端点（404/405/501）时，按既有 `UpstreamCountTokensUnsupported`
  语义返回上游错误，不轮换凭据、不降级凭据健康度。
- R4：不改变其他渠道、其他协议、其他操作的既有路由行为。
- R5：不改动 `no_available_candidate` 的既有错误映射契约（本轮不做错误语义重构）。

## Acceptance Criteria

- [x] `internal/channel/modules/newapi.go` 的渠道路由包含
  `(Anthropic, count_tokens) -> RouteNative`。
- [x] `internal/channel/newapi_module_test.go` 的期望路由表同步更新并通过。
- [x] `go test ./internal/channel/... ./internal/scheduler/... ./internal/state/...`（`-p 1`）通过。
- [x] 新增断言：New API 渠道的 Anthropic 客户端在 `count_tokens` 操作下存在路由目标，
  不再落到 `ReasonOperationUnsupported`（`TestInspectRoutesAnthropicCountTokensOnNewAPI`）。
- [x] 既有 `go test ./internal/gateway/` 不回归（两个失败
  `TestSSEScannerRecognizesThreeDialectFixtures`、`TestBufferedWriteDeadlineStopsRealTCPSlowReader`
  在改动前即失败，属 Windows CRLF 夹具与真实 TCP 计时问题）。

## Out of Scope

- 本地估算 token 数（不走上游）的实现；本项目执行层没有通用估算器，且既有设计是透传上游错误。
- Gemini 客户端协议的 count_tokens 路由（无实际需求，且上游支持情况未知）。
- 把静态路由原因（`operation_unsupported`、`no_route_target` 等）映射为更精确的 HTTP 错误码。
- 前端展示改动。
