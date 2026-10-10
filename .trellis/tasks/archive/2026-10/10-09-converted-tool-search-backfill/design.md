# Design: 转换路由:defer_loading 与 tool_search 声明一致性

> 对应 `prd.md` R1-R4。策略：**剥离孤儿 defer_loading**（用户已确认）。

## 1. 范围与边界

| 层 | 改动 | 不碰 |
|---|---|---|
| 转换构造(`internal/execution/bifrost/converted.go`) | 新增 `stripOrphanedDeferredToolFlags`，在 `applyConvertedFastMode` 之后调用 | 工具白名单/历史保真/参数覆盖既有分支 |
| 测试(`internal/execution/bifrost/`) | 新增一致性单测 | 既有断言 |
| CPA(Codex) 路径 | 无（翻译器已删 defer_loading） | 所有 |
| 原生路由 | 无（不经本函数） | 所有 |

## 2. 事实依据

- Claude Code 的工具搜索是客户端侧实现：请求只带 `defer_loading:true`（MCP 工具 + `DeferredToolPlaceholder` 占位工具）+ beta 头，`tools` 里**没有** `tool_search` 服务端声明（详见 `research/tool-search-findings.md`）。
- OpenAI 系 responses 上游要求 `defer_loading` 必须伴随自己的 `{"type":"tool_search"}` 声明，否则 400。
- bifrost 对内置 OpenAI 渠道（provider=`schemas.OpenAI`，含自定义 base_url）按模型名（gpt-5.4/5.5/5.6/6）保留 `defer_loading`（`keepDeferLoading`，`providers/openai/responses.go:848`）；
  openai-compatible 渠道 provider 为 `gptload-custom-*`，SDK 默认不保留——两条路径的差异不影响本方案。
- `buildConvertedResponsesRequest` 的产物只服务转换路由；anthropic 客户端 → anthropic 上游是原生路由，不经过这里。

## 3. 方案

在 `buildConvertedResponsesRequest` 调用链尾部（`applyConvertedFastMode` 之后）新增：

```go
// 上游（OpenAI 系 responses）要求 defer_loading 必须伴随 tools.tool_search 声明；
// Claude Code 的客户端侧工具搜索只发 defer_loading、不发服务端声明，
// 直接转发会被上游以 "Deferred tools require tools.tool_search" 拒绝。
// 转为一致形态：无 tool_search 声明时剥离 defer_loading（工具立即加载）。
func stripOrphanedDeferredToolFlags(request *schemas.BifrostResponsesRequest) {
    if request == nil || request.Params == nil || len(request.Params.Tools) == 0 {
        return
    }
    if responsesToolsDeclareToolSearch(request.Params.Tools) || !responsesToolsHaveDeferred(request.Params.Tools) {
        return
    }
    clearResponsesToolDeferLoading(request.Params.Tools)
}
```

- `responsesToolsDeclareToolSearch` / `responsesToolsHaveDeferred` / `clearResponsesToolDeferLoading` 递归处理 `ResponsesToolNamespace.Tools`（namespace 子工具与顶层同构）。
- 触发条件严格限定为"有 defer_loading 且无 tool_search"：两者共存（正常 OpenAI 工具搜索请求）与无 defer_loading 的请求逐字节不受影响。

## 4. 被否方案

1. **补齐 `{"type":"tool_search"}` 声明**：模型会开始调用工具搜索（服务端语义），Claude Code 侧未声明该能力、也未配对 `ToolSearch` 的本地检索闭环，响应里会出现它未预期的 `tool_search_call`/结果块。语义风险大于收益，否。
2. **按 provider/模型判定后剥离**：需要复刻 SDK 的 `keepDeferLoading` 判定（provider 白名单 + 模型名 + datasheet），与 SDK 内部行为耦合、易随升级漂移；而无条件剥离在"有 tool_search 声明"时本就不动作，误伤面为零。否。
3. **删除 `DeferredToolPlaceholder` 占位工具**：删除工具会改变上游可见工具集合，可能触发客户端侧工具索引错位；占位工具在剥离 defer_loading 后是无害的普通 function。否（Non-goal）。
4. **在目录层 advertise（Sub2API 的另一种做法）**：那是让 Codex CLI 客户端侧执行搜索；Claude Code 的工具搜索开关由 base URL/环境变量决定，不经模型目录，无法复用。否。

## 5. 兼容与回滚

- 只作用于转换路由构造；原生路由与 CPA 路径不变。
- 请求对象由本次构造新建，直接改工具切片不污染调用方。
- 回滚 = 删除一个函数与一处调用。

## 6. 验证

- 单测坐标与骨架同任务 A（直构 spec + `openai.ToOpenAIResponsesRequest` 打上游 body）。
- 用例：
  - 孤儿 defer_loading → 上游 tools 无 `defer_loading`；
  - `defer_loading` + tool_search 声明 → 上游仍带 `defer_loading`（不回归）；
  - 无 defer_loading → body 逐字节不变；
  - namespace 子工具携带 defer_loading → 同样被剥离（若上游形态可构造）。
- 全量：`go test ./internal/execution/bifrost/... -p 1`。

