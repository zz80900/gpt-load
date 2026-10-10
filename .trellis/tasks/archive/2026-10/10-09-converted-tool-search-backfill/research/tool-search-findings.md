# 调研记录：Deferred tools require tools.tool_search

日期：2026-10-09（规划阶段）

## 报错原文

`Invalid Value: 'tools.defer_loading'. Deferred tools require tools.tool_search.`

（上游 400，ChatGPT/Codex 风格错误文案：带 defer_loading 的工具要求同请求包含 tool_search 声明。）

## 探针实测（gpt-load bifrost 转换路径）

anthropic 客户端 body 带 `tool_search_tool_regex_20251119` 工具 + 一个 `defer_loading:true` 的 function 工具，
经 `buildConvertedResponsesRequest` + `openai.ToOpenAIResponsesRequest` 后，上游 body tools 为：

```json
[
  {"defer_loading":true,"description":"d","name":"lookup","parameters":{...},"strict":false,"type":"function"},
  {"name":"tool_search","type":"tool_search"}
]
```

结论：**客户端发了 tool_search，转换链不会丢**（bifrost 的 responses 工具类型白名单含 tool_search）。
因此报错链路必须是"请求到达上游时就没有 tool_search 声明"的其它场景。

## gpt-load 现状盘点

- 全库（含 third_party）没有 `supports_search_tool` 的处理：Codex 客户端模型目录
  `internal/catalog/codex_client_models.go` 从内置模板全量克隆（`cloneCodexModelMap`），
  模板 `codex_client_models_0.159.2.json` 的 11 个模型全部 `supports_search_tool:true`、
  `tool_mode:"code_mode_only"`、`use_responses_lite:true`（gpt-5.5 除外，tool_mode 为 null）。
- bifrost responses 出站：`hasAnthropicOnlyResponsesToolFlags` 在 `keepDeferLoading=false` 时剥离
  `defer_loading`（`providers/openai/types.go:713`）；`keepDeferLoading` 由模型名判定
  （gpt-5.4/5.5/5.6/6 前缀，`providers/openai/utils.go:78`）。没有"补 tool_search"逻辑。
- CPA/Codex 路径：CLIProxyAPI 的 codex 执行链不碰 tool_search/defer_loading（原样透传）；
  claude→codex 翻译器会删除 function 工具的 `defer_loading`（`codex_claude_request.go:373`），
  即 anthropic 入口不会把 defer_loading 带给 Codex 上游。

## Sub2API 参考实现（Wei-Shaw/sub2api）

1. `c7538cd256 fix(codex): advertise search capability for bridged routes`
   - 位置：`backend/internal/service/openai_codex_models_service.go`（模型清单构建）。
   - 行为：当模型的**全部候选账号**都走 Responses→Chat 桥接（`shouldForwardOpenAIResponsesViaRawChatCompletions`）时，
     在 Codex 客户端模型清单里 advertise `supports_search_tool: true`；
     原生 Responses 路由不推断（以上游清单为准）。
   - 即：让 Codex 客户端知道"工具搜索由客户端侧执行"，属于**目录层面**的修复。
2. `backend/internal/pkg/apicompat/responses_client_tools.go:147`
   `stripResponsesDeferredToolFlags`：当最终工具声明列表**不再包含** tool_search 时，
   删除工具上的 `defer_loading`（桥接把 tool_search 降级为同名 function 代理或丢弃时的一致性保证）。
3. 同文件 `toolSearchProxyName = "tool_search"`：桥接路径把 tool_search 服务端工具降级为同名 function 代理，
   回程再把调用还原为 `tool_search_call` 项。

## Claude Code 实际形态（2026-10-10，本机二进制核验）

- Claude Code 的工具搜索是**客户端侧**实现，请求体里**不含** `tool_search_tool_regex_20251119` 服务端工具
  （该形态是 API 文档的另一种接入方式，CC 未使用）。
- 实际信号：
  - MCP 工具带 `"defer_loading": true`；
  - 另有本地工具 `ToolSearch` 与占位工具 `DeferredToolPlaceholder`（描述：Reserved placeholder that keeps
    deferred tool loading active; never call this tool，同样带 `defer_loading:true`）；
  - beta 头：`advanced-tool-use-2025-11-20`（新）或 `tool-search-tool-2025-10-19`（旧/受限部署回退）。
- 触发：`ENABLE_TOOL_SEARCH` 三态（true / auto[:N] / false），且**代理守卫**——`ANTHROPIC_BASE_URL`
  非 api.anthropic.com 时默认关闭，需显式启用（提示语：Set ENABLE_TOOL_SEARCH=true (or auto / auto:N)
  if your proxy forwards tool_reference blocks）。
- 对不支持 tool_search 的模型，CC 自己会清洗剥离 `defer_loading`/`strict`/占位工具；对代理上游它无法预判，
  因此 gpt-load 必须在出站侧兜底。

结论：gpt-load 收到的就是"只有 `defer_loading`、没有服务端 tool_search 声明"的形态；
OpenAI 系 responses 上游要求的是自己的 `{"type":"tool_search"}` 共存声明，两者语义不可互转。

## 复现（2026-10-10，已定位）

bifrost 探针：anthropic 客户端只带 `defer_loading:true` 的 function 工具、不带 tool_search 声明时，
上游收到的 tools 为：

```json
[{"defer_loading":true,"description":"d","name":"mcp__foo__bar","parameters":{...},"strict":false,"type":"function"}]
```

- `keepDeferLoading` 因模型名（gpt-6.1-sol 命中 `gpt-6` 前缀）为 true，defer_loading 被保留直发上游；
- 无 tool_search 声明 → 上游校验 400：`Invalid Value: 'tools.defer_loading'. Deferred tools require tools.tool_search.`

即：**报错的直接成因是"客户端带了 defer_loading 而未带 tool_search"，gpt-load 原样放行**。

CPA（Codex 渠道）探针：claude→codex 翻译器（`codex_claude_request.go`）把所有非 web_search 工具强制转为
`type:"function"` 并删除 `defer_loading`；tool_search 声明被降级为同名 function 工具。
**CPA 路径不会把 defer_loading 发出去，不会触发该报错。**

## 决策（2026-10-10，用户确认）

修复策略：**剥离孤儿 defer_loading**——出站工具集存在 `defer_loading` 且无 tool_search 声明时，
剥离各工具的 `defer_loading`（工具立即加载）。理由：客户端侧工具搜索在 OpenAI 上游无对应机制，
补齐 `{"type":"tool_search"}` 会让模型开始调用客户端未预期、也未声明过的服务端工具；
剥离与 SDK 在模型不支持 tool search 时的既有行为一致，语义安全。


