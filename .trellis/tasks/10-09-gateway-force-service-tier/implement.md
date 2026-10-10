# Implement: 网关强制 fast mode:协议转换路由透传 service_tier

## 执行清单

1. **改转换构造** `internal/execution/bifrost/converted.go`
   - 在 `buildConvertedResponsesRequest` 末尾（`stripResponsesControlParams` 之后）调用新函数 `applyConvertedFastMode(request)`。
   - 新增函数体（见 `design.md` 第 3 节），保持文件既有注释语言与命名风格。
2. **加单测** `internal/execution/bifrost/converted_fast_mode_test.go`
   - 复用 `execution.AttemptSpec{RouteMode: RouteConverted, ClientProtocol: protocol.Anthropic, Operation: OperationChatCompletion, UpstreamModel: "gpt-6.1-sol"}` 直构 spec。
   - 上游出站 body 捕获用 `providerUtils.CheckContextAndGetRequestBody` + `openai.ToOpenAIResponsesRequest`（`research/probe-findings.md` 有可复用的探针骨架）。
   - 用例：
     - `speed:"fast"` → body 含 `service_tier:"priority"`、不含 `speed`。
     - `speed:"standard"` → 无 `service_tier`、无 `speed`。
     - `service_tier:"auto"` + `speed:"fast"` → `priority`。
     - 参数覆盖：`ConfiguredParameters=["speed"]` 且 body 含 `speed:"fast"` → 与直发等效。
3. **回归** 相关包全量测试。
4. **清理** 调研用临时探针文件（`zz_probe_toolsearch_test.go`），已在本任务规划阶段删除。

## 验证命令

```bash
go test ./internal/execution/bifrost/ -run TestConvertedFastMode -v -p 1
go test ./internal/execution/bifrost/... -p 1
go test ./internal/gateway/... -p 1
```

## 评审门

- 单测必须真实打到上游 body（非中间结构断言），防止再次出现"转换层吞字段"回归。
- 确认原生路由测试（`ultrafast_test.go`）未受影响。

## 回滚点

- 单个函数新增 + 一处调用，回滚 = 删除两处 diff。

