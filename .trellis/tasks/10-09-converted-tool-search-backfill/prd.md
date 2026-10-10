# 转换路由:defer_loading 与 tool_search 声明一致性

## Goal

消除上游 400：`Invalid Value: 'tools.defer_loading'. Deferred tools require tools.tool_search.`

当 gpt-load 转发到 openai-responses 上游的工具集里出现"有 `defer_loading` 工具、却没有 `tool_search` 声明"的不一致形态时，网关必须自行修正（补齐声明或剥离标记），不再把该请求原样放行。

## 复现（已定位）

anthropic 客户端（Claude Code）只带 `defer_loading:true` 的 function 工具、不带 tool_search 声明时：

- bifrost 转换路径因 `keepDeferLoading=true`（模型名命中 `gpt-6` 前缀）把 `defer_loading` 原样发给上游；
- 上游收到 `tools:[{"defer_loading":true,...}]`，无 tool_search → 400，文案与线上一致。

对照事实：

- 客户端同时带 tool_search + defer_loading 时，bifrost 路径两者都正常到达上游（不丢字段）。
- CPA（Codex 订阅渠道）的 claude→codex 翻译器会删除全部 `defer_loading`，**不会**触发该报错。

## 背景参照

Sub2API（Wei-Shaw/sub2api）社区修复：

- 请求层：`responses_client_tools.go` 的 `stripResponsesDeferredToolFlags`——最终工具声明列表不含 tool_search 时，删除各工具的 `defer_loading`。
- 目录层：`c7538cd256` 在模型清单里对桥接路由 advertise `supports_search_tool`。

## Requirements

### R1 出站一致性修正（策略见 design.md）

- 出站工具集存在 `defer_loading` 且**没有** tool_search 声明时，网关修正为一致形态。
- 修正必须发生在转换路由的出站构造层（bifrost converted）；原生透传路由不介入。

### R2 有声明时零改动

- 工具集已含 tool_search 声明时，`defer_loading` 保持原样，不改变现有成功路径（含 `keepDeferLoading=false` 时 SDK 既有的剥离行为）。

### R3 不引入客户端未知协议面

- 修正不得让上游产生客户端（Claude Code）未声明、也未准备好处理的交互（例如新增可被模型调用的服务端工具）。

### R4 覆盖范围

- 覆盖 Responses 目标的转换路由（anthropic/openai-completions/gemini → openai-responses）。
- Codex(CPA) 路径不加改动（其翻译器已删除 defer_loading）。

## Acceptance Criteria

- [ ] 单测：客户端只带 `defer_loading` → 出站请求不再出现"孤立 defer_loading"（按 design 策略断言）。
- [ ] 单测：客户端带 `defer_loading` + tool_search → 出站与现状完全一致（不回归）。
- [ ] 单测：无 `defer_loading` 的普通请求逐字节不受影响。
- [ ] `go test ./internal/execution/bifrost/... -p 1` 与网关包全绿。
- [ ] 手工验证：Claude Code 触发场景下上游不再 400。

## Notes

- 上游校验细节（哪些模型/后端执行 "deferred require tool_search"）以 `gpt-6.1-sol` 量级为准；判定条件对齐 SDK 既有的 `SupportsToolSearch`（gpt-5.4+/5.5/5.6/6）以避免在不需要的上游上改动请求。
- Claude Code 触发该形态的完整条件（是否发 tool_search、何时不发）由 research 记录，不在本任务修改客户端行为。

