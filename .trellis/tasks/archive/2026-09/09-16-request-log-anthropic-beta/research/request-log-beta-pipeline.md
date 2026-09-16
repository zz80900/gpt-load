# 调研：请求日志链路与 Anthropic beta 采集

本文件是 `09-16-request-log-anthropic-beta` 任务的技术证据留存，供实施与检查阶段直接引用，避免重复检索。

## 1. 请求日志六层链路与精确落点

数据自下而上穿过六层，每层都是「加一个字段并向下传递」。

| 层 | 文件与行 | 现状 | 本任务改动 |
| --- | --- | --- | --- |
| L1 dialect | `internal/dialect/dialect.go:21` | `RequestMetadata` 无 beta 字段 | 新增 `AnthropicBetas []string` |
| L1 dialect | `internal/dialect/anthropic.go:40` | `InspectRequest` 已解析 body（affinity / reasoning / operation） | 填充 beta 字段 |
| L1 dialect | `internal/dialect/request_fields.go:12` | `inspectJSONRequestFields` 被 7 个 dialect 共用 | 不动 |
| L2 gateway | `internal/gateway/handler.go:560-605` | `requestHeaders`（含 `Anthropic-Beta`）与 `parsed.Body` 同作用域；`:604` 起是 setter 序列 | 追加一行 setter |
| L2 gateway | `internal/gateway/request_log.go:54` | `requestRecorder` 有 `clientModel`/`stream`/`reasoning` 等字段 | 新增 `anthropicBetas []string` |
| L2 gateway | `internal/gateway/request_log.go:224` | `setReasoning` 含深拷贝模式（`BudgetTokens`） | 同构 setter |
| L2 gateway | `internal/gateway/request_log.go:121` | `emit()` 组装 `RequestEvent` | 传字段 |
| L3 telemetry | `internal/telemetry/requestlog.go:134` | `RequestEvent` 无 beta；`:84` 的 `Attempt` 亦无 | `RequestEvent` 加字段 |
| L4 持久化 | `internal/requestlog/mapper.go:25` | `mapEvent` 落库投影 | 加映射 |
| L4 持久化 | `internal/requestlog/mapper.go:407` | `projectModel` 的 UTF-8 安全截断 | 抄此写法 |
| L4 持久化 | `internal/storage/models/request.go:4` | `RequestLog` 表结构 | 加列 |
| L4 持久化 | `internal/storage/migrations/0014_affinity_kind.go` | 三函数模板（`Up`/`ValidateRecoverable`/`Validate`） | 套用 |
| L4 持久化 | `internal/storage/migration.go:107` | migration 注册表，已到 `ID0014` | 注册 `ID0015` |
| L4 持久化 | `internal/storage/migration_test.go:28` | 期望列表 | 同步 |
| L5 查询/API | `internal/requestlog/query.go:335` | 行→Record 恢复点（邻 `AffinityKind`） | 加一行 |
| L5 查询/API | `internal/requestlog/types.go:117` | `Record` DTO | 加字段 |
| L5 查询/API | `internal/control/request_logs.go:131` | `requestLogItemResponse` | 加 JSON 字段 |
| L5 查询/API | `internal/control/request_logs.go:930` | 组装点 | 加一行 |
| L6 前端 | `web/src/app/resources/request-logs.ts:272` | `itemFields` 白名单 | 加字段名 |
| L6 前端 | `web/src/app/resources/projector.ts:134` | `assertNoSecretLikeFields` | 不改，但约束前进 |
| L6 前端 | `web/src/features/monitor/LogDetailDrawer.vue:451-462` | 客户端模型行已有 reasoning 徽标模式 | 复用 |
| L6 前端 | `web/src/features/monitor/LogsTab.vue` | 列表 | 加徽标 |
| L6 前端 | `web/src/i18n/locales/{zh-CN,en-US,ja-JP}/monitor.ts` | 三语言 | 各加键 |

## 2. beta 的两个来源

| 来源 | 形态 | 证据 |
| --- | --- | --- |
| 请求头 | `Anthropic-Beta: a, b, c`，可多次出现 | Claude Code 2.1.258 实测发 19 项 |
| 请求体 | 顶层 `betas: ["a", "b"]` 数组 | CLIProxyAPI `claude_executor_request.go:531-548` 的 `extractAndRemoveBetas` 从 body 提取并从 body 删除 |

CLIProxyAPI 两处都认，只用请求头会漏。`http.Header.Values("Anthropic-Beta")` 内部做 MIME 规范化，大小写形式等价。

**上游不回执**：Anthropic 不在 `/v1/messages` 响应中回报生效的 beta，网关侧无法观测上游是否真正启用 1M。

## 3. 1M beta 标识符

`context-1m-2025-08-07`，定义于 CLIProxyAPI `claude_executor_request.go:41` 的 `claudeContext1MBeta`。

CPA 的完整 beta 列表见 `claude_executor_request.go:84-105`（注释列 20 项），全量拼接约 566 字节——这是 `VARCHAR(1024)` 而非 `VARCHAR(512)` 的直接依据。

CPA 的 `claudeRequestedBetas`（`:493-505`）用 map 精确匹配，**区分大小写**。因此采集侧不应做大小写归一，否则会掩盖「客户端发错大小写导致 beta 不生效」这一事实。

## 4. 前端白名单是硬约束

`web/src/app/resources/projector.ts:134-143` 的实现是严格白名单：

```ts
for (const field of Object.keys(record)) {
  if (allowed.has(field)) continue
  if (secretLikeField.test(field)) invalidResponse()
  invalidResponse()
}
```

不在白名单中的任何字段都会触发 `invalidResponse()`。调用点见 `web/src/app/resources/request-logs.ts:658`（列表项）与 `:664`（详情，白名单为 `[...itemFields, 'attempts']`）。

**后果**：后端新增 `anthropic_betas` 而前端 `itemFields` 未同步，会让整个日志接口被判为非法响应，日志页全面不可用——不只是新字段不显示。因此前后端必须同版本部署，这是本任务唯一的破坏性兼容风险。

空串合法性：后端恒返回 `""` 而非 `null`，前端必须用 `projectString(..., { allowEmpty: true })`。

## 5. 与既有字段的边界澄清

- `ContextThresholdTokens`（`internal/requestlog/types.go:148`，来源 `internal/pricing/quote.go:90` 的 `selectSchedulePrices` 与 `rule.ContextTiers`）是**计价分档阈值**，属价格维度，不是能力声明，与本需求无关。
- `projectModel`（`internal/requestlog/mapper.go:407-418`）只做长度截断、不剥离 `[1m]` 后缀，所以模型名带后缀的场景当前已可见；本任务覆盖的是「模型名不变、只有请求头带 beta」的场景。
- `CacheWrite1HTokens`（`internal/storage/models/request.go`）是 `extended-cache-ttl-2025-04-11` 已有独立计费列的先例，也是选择「全量串存 beta」而非布尔标记的理由。

## 6. 出站路径不受影响（实施时不应触碰）

- `internal/gateway/execution_forward.go:740-772`：HeaderRules 应用（`headers.Set` 覆盖语义）
- `internal/gateway/forward.go:339-345`：`sanitizeUpstreamRequestHeaders`
- `internal/platform/httpheader/policy.go:16-28`：禁止名单，不含 `anthropic-beta`
- `internal/execution/bifrost/passthrough.go:190`：`sanitizeNativePassthroughRequest` 返回 header 零改动
- `internal/execution/cpa/adapter.go`：订阅渠道执行器

本任务全程只读入站数据，出站路径零改动，因此采集逻辑不可能改变转发行为。
