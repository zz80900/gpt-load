# 技术设计：请求日志记录 Anthropic Beta 能力标记

## 1. 架构与改动边界

信息自下而上穿过六层，每层只做一件事：**加一个字段并向下传递**。没有任何一层需要新增数据通道或旁路。

| 层 | 文件 | 改动性质 |
| --- | --- | --- |
| L1 dialect | `internal/dialect/anthropic_betas.go`（新增）、`anthropic.go`、`dialect.go` | 提取 beta 声明 |
| L2 gateway | `internal/gateway/request_log.go`、`internal/gateway/handler.go` | 录入 recorder |
| L3 telemetry | `internal/telemetry/requestlog.go` | 进入事件 |
| L4 持久化 | `internal/storage/migrations/0015_*.go`（新增）、`migration.go`、`models/request.go`、`internal/requestlog/mapper.go` | 落库 |
| L5 查询与 API | `internal/requestlog/query.go`、`types.go`、`internal/control/request_logs.go` | 读出与透出 |
| L6 前端 | `web/src/app/resources/request-logs.ts`、`features/monitor/LogDetailDrawer.vue`、`LogsTab.vue`、`i18n/locales/*/monitor.ts` | 展示 |

边界约束：**只采集，不干预**。本设计不读取、不修改、不过滤任何出站请求头，出站路径（`internal/gateway/execution_forward.go`、`internal/execution/bifrost/passthrough.go`）零改动。这保证了一个失败模式被排除：采集逻辑永远不可能改变转发行为。

### L1 dialect 层：提取

新增 `internal/dialect/anthropic_betas.go`，只暴露两个符号：

```go
// AnthropicContext1MBeta 是 1M 上下文 beta 标识符。
const AnthropicContext1MBeta = "context-1m-2025-08-07"

// inspectAnthropicBetas 收集客户端声明的 beta，请求头与请求体取并集。
func inspectAnthropicBetas(header http.Header, body []byte) []string
```

选择独立函数而非在共享的 `inspectJSONRequestFields`（`internal/dialect/request_fields.go:12`）里加 `betas` 分支，原因有二：

1. 该函数被 7 个 dialect 共用（`anthropic.go:45`、`openai.go:26`、`openai_embeddings.go:54`、`openai_images.go:77`、`openai_responses.go:41`、`rerank.go:34`）。`betas` 是 Anthropic 专属语义，塞进公共解析器会污染其余六条协议路径。
2. 本包已有「按协议独立解析 body」的既有模式：`inspectPromptAffinityPrefix`（`internal/dialect/prompt_affinity.go:43`）与 `inspectAnthropicReasoning`（`internal/dialect/request_reasoning.go:37`）都是独立函数。

代价是多一次 `json.Unmarshal`。实测可接受：`inspectPromptAffinityPrefix`（`prompt_affinity.go:47-50`）已在对同一 body 做整对象反序列化，本条与它同构；且解码目标只有 `betas` 一个字段，`encoding/json` 对 `messages`/`system`/`tools` 等未知字段走 `d.skip()`，不会为它们构造 Go 对象。

解析规则：

- 请求头：`header.Values("Anthropic-Beta")` 取全部出现（Claude Code 会发多个值），每个值按 `,` 切分。`http.Header.Values` 内部做 MIME 规范化，`Anthropic-Beta` 与 `anthropic-beta` 等价。
- 请求体：顶层 `betas` 数组。`json.Unmarshal` 失败时静默丢弃体来源，仍返回头来源的结果——解析失败不能影响请求本身。
- 逐项 `strings.TrimSpace`，丢弃空串。
- 去重后 `sort.Strings`。排序是为了让同一组声明无论出现顺序如何都产生同一个字符串，否则日志比对与断言都不成立。
- **不做大小写归一**。beta slug 由上游定义，客户端发 `Context-1M-...` 与发 `context-1m-...` 是两种事实（后者生效、前者不生效），日志应当忠实记录客户端实际发出的字节。

`RequestMetadata`（`internal/dialect/dialect.go:21`）新增 `AnthropicBetas []string`。把协议专属字段放在共享结构里是本代码库既有模式——同结构中的 `ResponsesStorePreference`、`PreviousResponseID` 已属 OpenAI Responses 专属。`Anthropic.InspectRequest`（`anthropic.go:40`）在返回前填充该字段，其余 dialect 不填，自然为零值。

### L2 gateway 层：采集

`requestRecorder`（`internal/gateway/request_log.go:54`）新增 `anthropicBetas []string` 与同构 setter：

```go
func (recorder *requestRecorder) setAnthropicBetas(betas []string) {
    if recorder == nil {
        return
    }
    recorder.anthropicBetas = append([]string(nil), betas...)
}
```

切片必须复制，模式对照 `setReasoning`（`request_log.go:224-233`）对 `BudgetTokens` 的深拷贝：`metadata.AnthropicBetas` 的所有权在调用方，recorder 生命周期长于该次调用。

`emit()`（`request_log.go:121`）把该字段写入 `RequestEvent`。

注入点在 `internal/gateway/handler.go:604` 的 setter 序列中，追加一行 `recorder.setAnthropicBetas(metadata.AnthropicBetas)`。此处 `metadata` 已由 `selectedDialect.InspectRequest(parsed)`（`handler.go:569`）产出，无需二次解析。

### L3 telemetry 层：事件

`RequestEvent`（`internal/telemetry/requestlog.go:134`）新增 `AnthropicBetas []string`。

**不加到 `Attempt`（同文件 `:84`）**。beta 是客户端在一次 HTTP 请求上的声明，不随渠道切换或重试而改变；放进 Attempt 会让重试请求重复记录同一份数据，并诱导出「第 2 次尝试用了不同的 beta」这种不存在的语义。

保持 `[]string` 而非 `string`：结构化数据在 telemetry 层，展平推迟到持久化层——这与 `Reasoning` 的处理一致（telemetry 层是 `reasoning.Config`，落库时才展平成 `ReasoningMode`/`ReasoningEffort`/`ReasoningBudgetTokens` 三列）。

### L4 持久化层：落库

**迁移**：新增 `internal/storage/migrations/0015_anthropic_betas.go`，严格套用 `0014_affinity_kind.go` 的三函数模板：

- `Up0015`：先 `ValidateRecoverable0015`，再幂等 `ALTER TABLE request_logs ADD COLUMN anthropic_betas VARCHAR(1024) NOT NULL DEFAULT ''`，最后 `Validate0015`。
- `ValidateRecoverable0015`：表不存在时返回可恢复错误；列已存在则直接进入校验。保证半途中断的升级可重入。
- `Validate0015`：用 `db.Migrator().ColumnTypes` 校验列类型、非空、长度与默认值。

注册点两处必须同步：`internal/storage/migration.go:107`（追加一条 migration 记录）与 `internal/storage/migration_test.go:28`（期望列表）。

`VARCHAR(1024)` 的取值依据：Claude Code 2.1.258 实测的完整 beta 集合（CLIProxyAPI `claude_executor_request.go:84-105` 记录的 20 项）拼接后为 566 字节。512 会截断正常请求，1024 留出约 1.8 倍余量。

**模型**：`RequestLog`（`internal/storage/models/request.go:4`）新增

```go
AnthropicBetas string `gorm:"column:anthropic_betas;size:1024;not null;default:''"`
```

**映射**：`mapper.mapEvent`（`internal/requestlog/mapper.go:25`）新增 `AnthropicBetas: projectAnthropicBetas(event.AnthropicBetas)`。辅助函数与同文件 `projectModel`（`:407`）同构，负责拼接与安全截断：

```go
const maxAnthropicBetasBytes = 1024

func projectAnthropicBetas(betas []string) string
```

截断必须回退到 UTF-8 边界，沿用 `projectModel`（`mapper.go:407-418`）的逐字节回退写法，避免在多字节字符中间切断而产生非法串。

### L5 查询与 API 层：透出

- `requestlog.Record`（`internal/requestlog/types.go:117`）新增 `AnthropicBetas string`。
- `query.go` 的行恢复点（`:335` 一带，紧邻 `AffinityKind: row.AffinityKind`）新增 `AnthropicBetas: row.AnthropicBetas`。
- `internal/control/request_logs.go` 的 `requestLogItemResponse`（`:131`）新增 `AnthropicBetas string \`json:"anthropic_betas"\``，组装点（`:930` 一带）新增一行。

**不新增筛选条件**。`requestlog.ListQuery` 与 `parseRequestLogQuery`（`internal/control/request_logs.go:343`）不动。理由见第 4 节。

访问密钥自作用域无需特殊处理：`sanitizeAccessKeyRequestLog`（`internal/control/request_logs.go:327`）清理的是 `requestlog.Record` 上的内部字段，beta 不是内部字段，且两侧共用同一构造函数，自动一致。

### L6 前端：展示

- `web/src/app/resources/request-logs.ts`：`RequestLogItemDto` 类型新增 `anthropic_betas: string`；`itemFields`（`:272`）新增 `'anthropic_betas'`；`projectItemRecord` 新增 `anthropic_betas: projectString(record.anthropic_betas, { allowEmpty: true })`。空串合法，故必须带 `allowEmpty`。
- `web/src/features/monitor/LogDetailDrawer.vue`：在 `request` section（`:437`）的「客户端模型」行加 1M 徽标，展示模式复用同一行已有的 `<small class="log-detail__reasoning">`（`:458`）；再新增一行完整 beta 串（`v-if` 非空时渲染）。
- `web/src/features/monitor/LogsTab.vue`：列表客户端模型列加 1M 徽标。
- i18n：`web/src/i18n/locales/{zh-CN,en-US,ja-JP}/monitor.ts` 三个语言各加键。

前端判定 1M 的常量集中在 `web/src/features/monitor/log-format.ts`，与后端 `AnthropicContext1MBeta` 的值保持一致。

## 2. 数据流与契约

```
客户端                       Anthropic-Beta 头 + body.betas
  │
  ├─ L1 dialect.Anthropic.InspectRequest   → RequestMetadata.AnthropicBetas []string
  │                                          （头∪体，去重，排序，不归一大小写）
  ├─ L2 recorder.setAnthropicBetas         → requestRecorder.anthropicBetas []string（复制）
  │                                          emit()
  ├─ L3 RequestEvent.AnthropicBetas        → []string
  ├─ L4 mapper.mapEvent                    → projectAnthropicBetas（join + 截断）
  │     models.RequestLog.AnthropicBetas   → string，VARCHAR(1024) NOT NULL DEFAULT ''
  ├─ L5 query 行恢复                        → requestlog.Record.AnthropicBetas string
  │     requestLogItemResponse             → JSON "anthropic_betas"
  └─ L6 前端白名单 + 投影                   → LogDetailDrawer / LogsTab
```

**跨层契约的唯一硬约束**：`anthropic_betas` 在 API 响应中恒为字符串，无声明时为 `""`，永不为 `null`、永不缺省。这决定了前端必须用 `projectString(..., { allowEmpty: true })` 而非 `projectNonBlankString`。

## 3. 兼容性与迁移

### 前端字段白名单是硬门槛

`web/src/app/resources/request-logs.ts` 对每条记录调用 `assertNoSecretLikeFields(record, itemFields)`（`:658`），其实现（`projector.ts:134-143`）是**严格白名单**：任何不在白名单中的字段一律触发 `invalidResponse()`。这意味着新增 `anthropic_betas` 而不同步 `itemFields`，会让**整个日志接口**在前端被判为非法响应，日志页全面不可用——不只是新字段不显示。

因此：**后端与前端必须同版本部署**。这是本任务唯一的破坏性兼容风险，也是实施时的首要检查项。

### 老库升级

迁移只做加列，非破坏性。历史行取 `DEFAULT ''`，前端投影为空串、不渲染徽标，列表与详情照常打开。

### 旧后端 + 新前端

旧后端不返回该字段，`projectString(undefined)` 抛 `invalidResponse`。与上一条同源：前后端必须一起发布，不支持交叉版本。

## 4. 权衡

**记录的是「客户端声明」，不是「上游生效」。** Anthropic 不在 `/v1/messages` 响应中回执生效的 beta，网关侧无法观测上游行为（此点已在 PRD 的 Out of Scope 中）。因此在 converted 路由下——beta 被协议转换丢弃——日志仍会显示客户端声明的值。

这是有意选择，不是疏漏：

1. 「客户端声明了 1M 但走的是 converted 路由」本身就是高价值诊断信号，隐去它反而损失信息。
2. 记录实际生效值需要等待 attempt 的 `RouteMode` 落地，而 recorder 在 attempt 之前就已生成事件，会引入时序耦合。
3. 前端文案明确写「客户端声明的 Beta」，不写「生效的 Beta」，歧义在展示层被消解。

**全量串而非布尔标记**（PRD R3 已定）。让未来新增的 beta 能力无需再改 schema——已有独立计费列的 `extended-cache-ttl-2025-04-11` 就是先例，它现在不必再走一次迁移。

**不加筛选**（用户已定）。现有日志筛选全部是等值匹配配索引（如 `client_model = ?` 配 `idx_request_logs_model_completed_id`，`models/request.go:13`）。按 beta 筛选只能退化为 `LIKE '%context-1m-2025-08-07%'`，对逗号分隔列无法走索引，等于全表扫描；而为子串匹配建索引在 MySQL 中不成立。列表徽标已能让用户在页面上直接辨识，筛选的边际价值不足以抵消这个代价。

**时间范围可选不影响本设计**。`requestlog.ListQuery` 的 `FromMS`/`ToMS`（`query.go:34-38`）本就可选，新字段是被动投影，不参与任何查询条件。

## 5. 运维与回滚

**回滚路径**：迁移为加列，回滚无需删列。停止写入（回退代码）后该列保留且始终为 `DEFAULT ''`，旧代码对其无感知，不影响任何既有查询。前端回滚必须与后端一起，原因见第 3 节。

**可观测性**：写入失败不新增失败面——`mapper` 层是纯内存投影，beta 截断与拼接不产生新的错误分支，`RequestLog` 插入语句的成功与否只取决于既有因素。

**需要盯的点**：迁移在存量库上的执行时长。`ADD COLUMN` 带 `DEFAULT ''` 且列可空性为 `NOT NULL`，MySQL 8 对常量默认值的加列是 instant DDL，不重建表；但需在实际存量库上确认版本，避免长时间锁表。

**不影响的部分**（实施时不应触碰）：出站请求头处理（`execution_forward.go:740-772` 的 HeaderRules 应用）、`sanitizeUpstreamRequestHeaders`（`forward.go:339-345`）、`platformheader` 的禁止名单（`httpheader/policy.go:16-28`）以及 bifrost/cpa 两个执行器。本设计全程只读入站数据。
