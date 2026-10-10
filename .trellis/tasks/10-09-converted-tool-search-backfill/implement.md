# Implement: 转换路由:defer_loading 与 tool_search 声明一致性

## 执行清单

1. **改转换构造** `internal/execution/bifrost/converted.go`
   - 在 `buildConvertedResponsesRequest` 中 `applyConvertedFastMode` 之后调用 `stripOrphanedDeferredToolFlags(request)`（同一 RouteConverted 门禁内或函数自判均可，保持与前一改动风格一致）。
   - 新增三个小函数（递归处理 `ResponsesToolNamespace.Tools`）：
     - `responsesToolsDeclareToolSearch(tools []schemas.ResponsesTool) bool`：任一工具 `Type == schemas.ResponsesToolTypeToolSearch`。
     - `responsesToolsHaveDeferred(tools []schemas.ResponsesTool) bool`：任一工具 `DeferLoading != nil && *DeferLoading`。
     - `clearResponsesToolDeferLoading(tools []schemas.ResponsesTool)`：置空所有 `DeferLoading`。
   - 触发条件严格为"有 defer_loading 且无 tool_search 声明"，否则不改动。
2. **加单测** `internal/execution/bifrost/converted_deferred_tools_test.go`
   - 复用与 `converted_fast_mode_test.go` 相同的骨架（直构 spec + `openai.ToOpenAIResponsesRequest` 捕获真实上游 body）。
   - 用例：
     - 孤儿 `defer_loading`（无 tool_search）→ 上游 tools 不含 `defer_loading`；
     - `defer_loading` + `tool_search_tool_regex_20251119` 声明 → 上游仍含 `defer_loading`（不回归）；
     - 无 `defer_loading` 请求 → 与现状逐字节一致；
     - （可构造时）namespace 子工具携带 `defer_loading` → 一并剥离。
3. **回归** `go test ./internal/execution/bifrost/... -p 1`（同类既有失败与任务 A 相同，排除）。
4. **清理** 本任务规划期的临时探针 `zz_probe_deferred_test.go`（删除；内容已存档于 `research/tool-search-findings.md`）。

## 验证命令

```bash
go test ./internal/execution/bifrost/ -run TestConvertedDeferredTools -v -p 1
go test ./internal/execution/bifrost/... -p 1
```

## 评审门

- 单测必须打到真实上游 body；断言"孤儿被剥离"与"声明共存不回归"两侧。
- 确认 `converted_fast_mode_test.go` 的既有用例仍绿（同文件同区域改动）。

## 回滚点

- 单个函数 + 一处调用，回滚 = 删除两处 diff。

## 同文件串行约束

本任务与 `10-09-gateway-force-service-tier` 改同一函数区域（`buildConvertedResponsesRequest` 尾部），
必须等该任务检查通过并提交后再实施，避免同文件并发编辑冲突。
