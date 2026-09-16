# 请求日志记录 Anthropic Beta 能力标记

## Goal

让请求日志能够体现「本次请求声明了哪些 Anthropic beta 能力」，首要目标是把 1M 上下文（`context-1m-2025-08-07`）变成可观测、可核对的事实，用于排障、成本审计与用量核对。

用户价值：

- **排障**：出现上下文超限或 400 时，能判断是否与 1M beta 声明相关。
- **成本**：1M 上下文超过 200K 的部分按溢价计费，需要能定位走 1M 的请求。
- **审计**：能回答「谁、在什么时间、对哪个模型用了 1M」。

## Background

### 现状

请求日志链路自下而上为：

```
gateway/handler.go:560  requestHeaders（含 Anthropic-Beta）
  → requestRecorder                         internal/gateway/request_log.go:54
  → telemetry.RequestEvent / Attempt        internal/telemetry/requestlog.go:84-156
  → mapper.mapEvent                         internal/requestlog/mapper.go:25
  → models.RequestLog                       internal/storage/models/request.go:4
  → API DTO                                 internal/control/request_logs.go
  → 前端 web/src/features/monitor/LogDetailDrawer.vue / LogsTab.vue
```

### 已确认事实

1. **链路上没有任何 beta 采集点**。`Attempt`（`internal/telemetry/requestlog.go:84-113`）与 `RequestEvent`（`:134-156`）均无 beta 或能力标记字段，`RequestLog` 表（`internal/storage/models/request.go:4-41`）同样没有。
2. **注入点现成**。`internal/gateway/handler.go:604` 一带已有 `recorder.setClientModel` / `setOperation` / `setStream` / `setReasoning` 的同构模式；同作用域内 `requestHeaders`（`:560`，含 `Anthropic-Beta`）与 `parsed.Body`（`:562`，含 `betas` 数组）可直接取用，不需要新增数据通道。
3. **beta 有两个来源**。请求头 `Anthropic-Beta`，以及请求体顶层 `betas` 数组。CLIProxyAPI 两处都认（`claude_executor_request.go:531-548` 的 `extractAndRemoveBetas`），只用请求头会漏。
4. **模型名后缀场景当前已可见**。`internal/requestlog/mapper.go:407-418` 的 `projectModel` 只做长度截断，不剥离后缀，因此 `client_model` 会完整保留 `claude-sonnet-4-5[1m]`。本任务要覆盖的是「模型名不变、只有请求头带 beta」的场景。
5. **`ContextThresholdTokens` 与本需求无关**。它是计价分档阈值（`internal/requestlog/types.go:148`，来源 `internal/pricing/quote.go:90` 的 `selectSchedulePrices` 与 `rule.ContextTiers`），属于价格维度，不是能力声明。
6. **项目专属规范尚未就绪，通用指南可用**。`.trellis/spec/backend/` 下各文件仍为空模板（`00-bootstrap-guidelines` 任务未完成），但 `.trellis/spec/guides/` 的跨层指南与代码复用指南已有实质内容，且其触发条件（「新增事件字段」「在多个位置加同一字段」）与本任务完全吻合。

## Requirements

- **R1 采集**：gateway 层解析 Anthropic 请求的 beta 声明，来源为请求头 `Anthropic-Beta` 与请求体顶层 `betas` 数组，二者取并集。
- **R2 范围**：仅对 Anthropic 协议采集；其他协议不产生额外解析开销，字段留空。
- **R3 存储**：`request_logs` 新增列 `anthropic_betas`（varchar，逗号分隔存全部 beta 并安全截断）承载该信息；历史行默认空串，老库升级不中断。选择全量串而非布尔标记，是为了让未来新增的 beta 能力（如已有独立计费字段 `CacheWrite1HTokens` 的 `extended-cache-ttl-2025-04-11`）无需再改 schema。
- **R4 透出**：请求日志 API（列表与详情）返回该信息。
- **R5 展示**：前端日志界面可辨识「本次请求声明了 1M 上下文」——列表页以徽标标识，详情抽屉展示完整 beta 串。文案表述为「客户端声明的 Beta」，不表述为「生效的 Beta」。
- **R6 安全与体积**：beta 串需有长度上限并安全截断；除 beta 外不采集任何其他请求头内容。

## Acceptance Criteria

- [ ] 发送一条 Anthropic 协议、`Anthropic-Beta` 含 `context-1m-2025-08-07` 的请求，日志详情可看到该值。
- [ ] 请求头不带、但请求体 `betas` 数组含该值时，同样可见（双来源均生效）。
- [ ] 未声明任何 beta 的请求，该字段为空且不显示 1M 标记。
- [ ] 非 Anthropic 协议请求（如 OpenAI chat completions）日志行为与改动前一致。
- [ ] 存量库升级：migration 成功，历史日志行该字段为默认值，列表与详情正常打开。
- [ ] 超长 beta 串被安全截断，日志写入不失败。
- [ ] 前端日志列表以徽标、详情抽屉以完整 beta 串分别标识客户端声明的 beta。

## Out of Scope

- **上游是否真正接受并生效 1M**。Anthropic 不在 `/v1/messages` 响应中回报生效的 beta，网关侧不可观测。
- **纯前端派生方案**（从 `client_model` 的 `[1m]` 后缀推断）。已作为方案 A 评估，覆盖不全，不采用。
- **其他非 Anthropic 协议的能力标记**。
- **按 beta 筛选日志**。现有筛选全部是等值匹配配索引，逗号分隔列上的子串匹配无法走索引，代价大于收益（详见 `design.md` 第 4 节）。
- **基于 beta 的统计报表与聚合分析**。

