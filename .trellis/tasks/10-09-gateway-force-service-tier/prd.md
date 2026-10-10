# 网关强制 fast mode:协议转换路由透传 service_tier

## Goal

让 anthropic 客户端(Claude Code / `claude-` 前缀别名)经 bifrost 协议转换路由访问 openai-responses 上游时,fast mode 能真正生效:客户端声明或分组参数覆盖注入的 `speed:"fast"`,最终以上游 `service_tier:"priority"` 到达上游,不再在转换层被丢弃。

## 背景与现状

实测探针(anthropic 客户端 body → `buildConvertedResponsesRequest` → `openai.ToOpenAIResponsesRequest`,打印上游出站 body)：

1. body 带 `speed:"fast"`：中立请求 `Params.ExtraParams["speed"]="fast"`，上游 body 里 **speed 与 service_tier 都不存在**（passthrough 开关未对该路径开启，extras 被丢弃）。
2. body 带 `service_tier:"fast"`（分组参数覆盖注入，`ConfiguredParameters=["service_tier"]`）：SDK 的 anthropic 结构认识 `service_tier`（语义为 `auto`/`standard_only`），经 `MapAnthropicRequestServiceTierToBifrost` 映射后 `Params.ServiceTier="auto"`，上游收到 `"service_tier":"auto"`——值错误，且覆盖赋值被语义映射吞掉。
3. 对照：Codex(CPA) 路径的 claude→codex 翻译器已内置 `speed:"fast" → service_tier:"priority"`（CLIProxyAPI `codex_claude_request.go`），因此该路径无需改动。

结论：bifrost 转换路径缺一个与 CPA 对齐的 `speed:"fast" → service_tier` 映射；anthropic 的 `service_tier` 字段不是 fast 载体，不能作为配置面。

## Requirements

### R1 客户端驱动

- anthropic 客户端请求体带 `speed:"fast"`（Claude Code `/fast` 的实际载荷）时，转换到 openai-responses 上游的请求体必须包含 `"service_tier":"priority"`，且不含 `speed` 字段。
- 仅作用于转换路由（客户端协议与上游协议不同）；原生 openai-responses 客户端的 `service_tier` 处理保持现状。

### R2 网关强制(分组参数覆盖)

- 分组 `parameter_overrides` 配 `{"set": {"speed": "fast"}}` 时，效果与 R1 完全一致，客户端无需感知（Claude Code 模型名不满足 fast 前置条件时走这条）。
- 不允许把 `service_tier` 作为 fast 配置面（anthropic 语义会映射成 `auto`，见背景）。

### R3 值语义

- `speed` 取值 `"fast"`（大小写不敏感、去首尾空白）→ 上游 `service_tier:"priority"`，与 Codex 路径的既有映射保持一致。
- `speed` 其它取值（如 `"standard"`）或非字符串：不设置 tier；`speed` 字段本身不得出现在上游请求体中。
- 客户端同时带 `service_tier:"auto"` 与 `speed:"fast"` 时，`speed` 胜出（覆盖 SDK 的语义映射结果）。

### R4 边界

- 不改 anthropic `service_tier` 字段的既有语义映射（`auto`/`standard_only`）。
- 不做 `ultrafast`：anthropic 侧没有对应语义，需要时另开任务。
- 不改计费模式识别：anthropic 客户端的 fast 请求仍按既有规则计价；日志/计价显示 fast 属后续任务。

## Acceptance Criteria

- [ ] 单测：anthropic → openai-responses，body `speed:"fast"` → 上游 body 含 `"service_tier":"priority"`、不含 `speed`。
- [ ] 单测：`speed:"standard"` → 上游 body 无 `service_tier`、无 `speed`。
- [ ] 单测：参数覆盖注入 `speed:"fast"`（`ConfiguredParameters` 含 `speed` 或不含均可）→ 与客户端直发等效。
- [ ] 单测：客户端带 `service_tier:"auto"` + `speed:"fast"` → 上游为 `priority`。
- [ ] 原生 openai-responses 客户端（RouteNative）行为不变（现有 `ultrafast_test.go` 等保持绿）。
- [ ] `go test ./internal/execution/bifrost/... ./internal/gateway/...` 全绿。
- [ ] 手工验证：curl 打 `/v1/messages` 带 `speed:"fast"`，上游收到 `service_tier:"priority"`。

## Notes

- 上游模型名不含 gpt-5.4+/gpt-6 时，bifrost 的 `keepDeferLoading`/tier 过滤策略不同；本任务的验证模型用 `gpt-6.1-sol` 量级（`ResolveModelCaps` 无记录 → tier 保留）。
- 探针测试已验证的三个事实记录在 `research/`（见 implement.md）。

