# 探针实测记录：anthropic → openai-responses 转换字段去向

日期：2026-10-09（规划阶段临时探针，跑完已删除；骨架可直接复用为正式单测）

## 方法

直构 `execution.AttemptSpec`，调用 `buildConvertedResponsesRequest` 后经
`providerUtils.CheckContextAndGetRequestBody` + `openai.ToOpenAIResponsesRequest` 取真实上游 body。

```go
spec := execution.AttemptSpec{
    RouteMode: execution.RouteConverted, ClientProtocol: protocol.Anthropic,
    Operation: execution.OperationChatCompletion, UpstreamModel: "gpt-6.1-sol",
    Body: []byte(body),
}
request, err := buildConvertedResponsesRequest(spec, schemas.OpenAI)
runtime := &Runtime{}
ctx := runtime.newSDKContext(context.Background(), spec, schemas.Key{})
body, sdkErr := providerUtils.CheckContextAndGetRequestBody(ctx, request,
    func() (providerUtils.RequestBodyWithExtraParams, error) {
        return openai.ToOpenAIResponsesRequest(ctx, request), nil
    })
```

## 结果 1：`speed:"fast"`

- 中立：`Params.ServiceTier=nil`，`Params.ExtraParams={"speed":"fast"}`
- 上游 body：`{"input":[...],"max_output_tokens":64,"model":"gpt-6.1-sol"}` —— speed 与 service_tier 均缺失。

## 结果 2：`service_tier:"fast"`（参数覆盖注入）

- 中立：`Params.ServiceTier=auto`，`Params.ExtraParams={}`
- 上游 body：`{...,"service_tier":"auto"}`
- 原因：`AnthropicMessageRequest.ServiceTier` 是 SDK 已知字段（语义仅 auto/standard_only），
  经 `MapAnthropicRequestServiceTierToBifrost` 把 `fast` 归一为 `auto`；
  同时因为是已知字段，`ConfiguredParameters` 的 extras 恢复也不会带上它。

## 结果 3：`tool_search_tool_regex_20251119` + `defer_loading:true` 工具

- 上游 body tools：
  ```json
  [
    {"defer_loading":true,"description":"d","name":"lookup","parameters":{"properties":{},"type":"object"},"strict":false,"type":"function"},
    {"name":"tool_search","type":"tool_search"}
  ]
  ```
- 两个字段都正常到达上游（与任务 B 的报错链路无关，见该任务 research）。

## 关键代码位置

- `internal/execution/bifrost/converted.go:62` `buildConvertedResponsesRequest`
- `providers/anthropic/responses.go:4107` speed → `ExtraParams["speed"]`
- `providers/anthropic/responses.go:4211` service_tier → `Params.ServiceTier`（经映射）
- `providers/utils/utils.go:2081` extras 合并需 `BifrostContextKeyPassthroughExtraParams`
- `schemas/chatcompletions.go:1773` tier 白名单（无 `fast`）
- CLIProxyAPI `internal/translator/codex/claude/codex_claude_request.go:427` fast→priority（对齐基准）

