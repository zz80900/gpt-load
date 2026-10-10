# Design: 网关强制 fast mode:协议转换路由透传 service_tier

> 对应 `prd.md` R1-R4。只讲技术设计：边界、契约、数据流、取舍、兼容与回滚。

## 1. 范围与边界

| 层 | 改动 | 不碰 |
|---|---|---|
| 转换构造(`internal/execution/bifrost/converted.go`) | 新增一个 fast 映射步骤，放在参数覆盖合并之后 | 现有参数覆盖/工具兼容/历史保真的既有分支 |
| 测试(`internal/execution/bifrost/`) | 新增 fast 映射单测 | 现有测试断言 |
| 网关/路由/计费 | 无 | 所有 |
| CPA(Codex)路径 | 无（已内置同等映射） | 所有 |

## 2. 事实依据（全部经探针实测）

- `bifrost` 的 anthropic 请求结构 `AnthropicMessageRequest` 识别两个字段：
  - `Speed *string`（`"fast"`，Anthropic fast mode 官方载荷）→ `ToBifrostResponsesRequest` 写入 `Params.ExtraParams["speed"]`（`providers/anthropic/responses.go:4107`）。
  - `ServiceTier *string`（语义仅 `auto`/`standard_only`）→ `MapAnthropicRequestServiceTierToBifrost`（`providers/anthropic/utils.go:3084`）：非 `standard_only`/`auto` 一律映射为 `auto`。
- 转换构造里 `Params.ExtraParams` 只有开启 `BifrostContextKeyPassthroughExtraParams` 才会合并进上游 JSON（`providers/utils/utils.go:2081`）；该开关当前仅在 DeepSeek+Anthropic 与 Probe 场景开启（`internal/execution/bifrost/executor.go:1471-1479`）。因此 `speed` 现状到不了上游。
- 上游出站对 tier 的白名单为 `auto/default/flex/priority/ultrafast/provisioned`（`schemas/chatcompletions.go:1773`），不含 `fast`。
- Codex(CPA) 路径既有行为：`speed:"fast" → service_tier:"priority"`（CLIProxyAPI `internal/translator/codex/claude/codex_claude_request.go:427-432`），本设计对齐它。
- 模型无 datasheet 记录时 `ServiceTierSupported` 走 fallback=true（`schemas/modelcaps.go:383`），`gpt-6.1-sol` 量级模型不会被过滤。

## 3. 方案

在 `buildConvertedResponsesRequest` 的 `stripResponsesControlParams` 之后新增一步：

```go
// Anthropic fast mode：speed="fast" 映射为上游 service_tier=priority。
// anthropic 的 service_tier 字段语义为 auto/standard_only，不是 fast 载体；
// speed 只存在于中立 ExtraParams，且该路径不合并 extras，因此在转换层显式提取。
func applyConvertedFastMode(request *schemas.BifrostResponsesRequest) {
    if request == nil || request.Params == nil || request.Params.ExtraParams == nil {
        return
    }
    raw, ok := request.Params.ExtraParams["speed"].(string)
    if !ok {
        return
    }
    delete(request.Params.ExtraParams, "speed")
    if strings.EqualFold(strings.TrimSpace(raw), "fast") {
        tier := schemas.BifrostServiceTierPriority
        request.Params.ServiceTier = &tier
    }
}
```

- 覆盖写入 `Params.ServiceTier`：客户端同时带 `service_tier:"auto"`（SDK 已写入）时，`speed` 胜出，与 prd R3 一致。
- `speed:"standard"` 或非字符串：仅清理 `ExtraParams["speed"]`，不动 tier。
- 放在 `stripResponsesControlParams` 之后：两者互不交集，顺序仅为可读性。

## 4. 被否方案

1. **把 `service_tier` 作为参数覆盖配置面**：SDK 的 anthropic 语义映射会把 `fast` 变成 `auto`（探针 2 实测），且 `ConfiguredParameters` 的 extras 恢复路径在此字段上不会触发（它是 SDK 已知字段，不算扩展参数）。语义混乱，否。
2. **对该路径全局开启 `BifrostContextKeyPassthroughExtraParams`**：会把所有配置过的扩展字段（含历史遗留）一起泄到上游，且 `speed` 仍只是 `speed`，上游依旧不认识；影响面大于收益，否。
3. **在 handler 层重写客户端 body**：转换路由的上游请求体由 SDK 重建，handler 改的是客户端 body，无法直接决定出站 `service_tier`；要透传还得回到转换层，多一层间接，否。
4. **支持 `ultrafast`**：anthropic 侧没有对应语义（客户端不会发），且上游 `ultrafast` 属于另一档；YAGNI，否。

## 5. 兼容与回滚

- 只作用于 `RouteConverted` 的 responses 构造；原生路由（openai-responses 客户端直发 `service_tier`）不经此函数，行为不变。
- 回滚即删除一个函数调用与函数体，无数据/配置迁移。

## 6. 验证

- 单测坐标：仿 `parameter_overrides_test.go` 的直构 spec + `openai.ToOpenAIResponsesRequest` 打上游 body（探针已验证该手法可行）。
- 覆盖：`speed:fast` / `speed:standard` / `service_tier:auto`+`speed:fast` / 参数覆盖注入。
- 全量：`go test ./internal/execution/bifrost/... -p 1`，再跑网关包。

