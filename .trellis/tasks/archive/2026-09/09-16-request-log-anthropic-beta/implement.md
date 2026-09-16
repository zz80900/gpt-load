# 实施计划：请求日志记录 Anthropic Beta 能力标记

## 前置约束

- 顺序为**自下而上**（L1 → L6）。每一层单独看都可编译，但 L4 之后才端到端可用。
- **后端与前端必须作为一次变更提交并同版本部署**：前端 `assertNoSecretLikeFields` 是严格白名单，后端多返回一个字段而前端 `itemFields` 未同步，会导致日志接口整体被判非法（详见 `design.md` 第 3 节）。这是本任务唯一的破坏性风险点。
- 不要触碰出站请求路径（`execution_forward.go`、`passthrough.go`、`forward.go`、`httpheader/policy.go`）与两个执行器（bifrost / cpa）。本任务全程只读入站数据。

## 实施清单

### L1 — dialect 层提取

1. 新增 `internal/dialect/anthropic_betas.go`：
   - 导出常量 `AnthropicContext1MBeta = "context-1m-2025-08-07"`。
   - 实现 `inspectAnthropicBetas(header http.Header, body []byte) []string`：
     - 头来源 `header.Values("Anthropic-Beta")`，逐值按 `,` 切分。
     - 体来源 `json.Unmarshal(body, &struct{ Betas []string \`json:"betas"\` }{})`，失败只丢体来源，不返回错误。
     - 逐项 `strings.TrimSpace`、丢空串、去重、`sort.Strings`。
     - 不归一大小写。
2. `internal/dialect/dialect.go`：`RequestMetadata` 新增 `AnthropicBetas []string`。
3. `internal/dialect/anthropic.go`：`InspectRequest`（`:40`）在 `return metadata, nil` 之前填充 `metadata.AnthropicBetas = inspectAnthropicBetas(req.Header, req.Body)`。
4. 新增 `internal/dialect/anthropic_betas_test.go`：头来源；体来源；头体并集去重；带空格与空项的值；大小写不被改写；`betas` 为非数组或 body 非 JSON 时静默降级；两者皆无时返回空。

### L2 — gateway 层采集

5. `internal/gateway/request_log.go`：
   - `requestRecorder`（`:54`）新增 `anthropicBetas []string`。
   - 新增 `setAnthropicBetas(betas []string)`，`nil` 接收者短路，内部 `append([]string(nil), betas...)` 复制，模式对照 `setReasoning`（`:224`）。
   - `emit()`（`:121`）把字段写入 `RequestEvent`。
6. `internal/gateway/handler.go`：在 `:604` 的 setter 序列中追加 `recorder.setAnthropicBetas(metadata.AnthropicBetas)`。

### L3 — telemetry 层事件

7. `internal/telemetry/requestlog.go`：`RequestEvent`（`:134`）新增 `AnthropicBetas []string`。**不要加到 `Attempt`（`:84`）**，理由见 `design.md` 第 1 节 L3。

### L4 — 持久化层

8. 新增 `internal/storage/migrations/0015_anthropic_betas.go`，套用 `0014_affinity_kind.go` 的三函数模板：
   - `ID0015 = "0015_anthropic_betas"`
   - `Up0015`：`ValidateRecoverable0015` → 幂等 `ALTER TABLE request_logs ADD COLUMN anthropic_betas VARCHAR(1024) NOT NULL DEFAULT ''` → `Validate0015`
   - `ValidateRecoverable0015`：表不存在返回可恢复错误；列已存在直接进入校验
   - `Validate0015`：`db.Migrator().ColumnTypes` 校验类型、非空、长度 1024、默认值 `''`
9. `internal/storage/migration.go:107` 追加 migration 注册记录。
10. `internal/storage/migration_test.go:28` 同步期望列表。
11. 新增 `internal/storage/migrations/0015_anthropic_betas_test.go`，覆盖幂等重入与校验失败分支，模式参考同目录既有 migration 测试。
12. `internal/storage/models/request.go`：`RequestLog`（`:4`）新增 `AnthropicBetas string \`gorm:"column:anthropic_betas;size:1024;not null;default:''"\``。
13. `internal/requestlog/mapper.go`：
    - 新增 `maxAnthropicBetasBytes = 1024` 常量。
    - 新增 `projectAnthropicBetas(betas []string) string`：`strings.Join(betas, ",")` 后按 UTF-8 边界安全截断，截断写法抄 `projectModel`（`:407-418`）。
    - `mapEvent`（`:25`）新增 `AnthropicBetas: projectAnthropicBetas(event.AnthropicBetas)`。
14. 在 `internal/requestlog` 既有测试文件中补充：空输入得空串；超长输入被截断且仍是合法 UTF-8；正常输入原样保留。

### L5 — 查询与 API 层

15. `internal/requestlog/types.go`：`Record`（`:117`）新增 `AnthropicBetas string`。
16. `internal/requestlog/query.go`：行恢复点（`:335` 一带，紧邻 `AffinityKind`）新增 `AnthropicBetas: row.AnthropicBetas`。
17. `internal/control/request_logs.go`：`requestLogItemResponse`（`:131`）新增 `AnthropicBetas string \`json:"anthropic_betas"\``；组装点（`:930` 一带）新增一行。
18. `requestlog.ListQuery`、`parseRequestLogQuery`（`:343`）、`request-log-filters.ts` **保持不动**——本任务不加筛选。
19. 在 `internal/control/request_logs_test.go` 既有用例中确认新字段出现在响应 JSON 中。

### L6 — 前端

20. `web/src/app/resources/request-logs.ts`：
    - `RequestLogItemDto` 类型新增 `anthropic_betas: string`
    - `itemFields`（`:272`）新增 `'anthropic_betas'`（**漏掉此项会使日志页整体报错**）
    - `projectItemRecord` 新增 `anthropic_betas: projectString(record.anthropic_betas, { allowEmpty: true })`
21. `web/src/features/monitor/log-format.ts`：新增 1M 判定辅助（对照后端 `AnthropicContext1MBeta`）。
22. `web/src/features/monitor/LogDetailDrawer.vue`：
    - `request` section（`:437`）客户端模型行加 1M 徽标，展示模式复用同行的 `<small class="log-detail__reasoning">`（`:458`）
    - 新增一行完整 beta 串，非空时渲染
23. `web/src/features/monitor/LogsTab.vue`：客户端模型列加 1M 徽标。
24. `web/src/i18n/locales/{zh-CN,en-US,ja-JP}/monitor.ts`：三个语言各补键。文案表述为「客户端声明的 Beta」，不写「生效的 Beta」。

## 验证命令

分层快速验证（每层完成后）：

```bash
go build ./...
go test -count=1 ./internal/dialect/... ./internal/requestlog/... ./internal/storage/...
```

L5 完成后：

```bash
go test -count=1 ./internal/gateway/... ./internal/control/...
```

L6 完成后：

```bash
corepack pnpm --dir web run type-check
corepack pnpm --dir web run lint
```

最终全量门禁（必须通过，含 gofmt / go mod tidy -diff / go vet / prettier / web build / go test / git diff --check）：

```bash
make check
```

手工验收（对应 PRD 验收标准）需真实发一次请求，逐条核对列表与详情的行为——尤其是「非 Anthropic 协议行为与改动前一致」与「存量库升级后历史行可正常打开」两项，自动化测试覆盖不到。

## 风险文件与回滚点

| 风险点 | 说明 | 回滚 |
| --- | --- | --- |
| `web/src/app/resources/request-logs.ts` 的 `itemFields` | 漏加字段会让日志页整体不可用（白名单严格，非仅新字段不显示） | 与后端同版本回退 |
| `internal/storage/migrations/0015_*.go` | 存量库 DDL。MySQL 8 对常量默认值加列为 instant DDL，仍需在真实库上确认版本与锁表时长 | 加列非破坏性，回退代码即可，列可留 |
| `internal/storage/migration.go` / `migration_test.go` | 注册点与期望列表必须同步，漏一处会导致启动期校验失败 | 撤销注册记录 |
| `internal/dialect/anthropic_betas.go` | 与出站路径无关，但位于请求入口，解析必须永不返回错误、永不 panic | 单文件删除，L1 其余两处改动随之回退 |

`internal/gateway/handler.go` 只加一行 setter 调用，`internal/telemetry/requestlog.go` 只加一个字段，二者均无逻辑分支，风险可忽略。

## 启动前跟进检查

在运行 `task.py start` 之前确认：

1. PRD 的验收标准已通过收敛检查，无残留 Open Question。
2. `design.md` 与 `implement.md` 已就位（复杂任务必需）。
3. `implement.jsonl` 与 `check.jsonl` 中的条目为真实内容，非种子 `_example` 行。
4. 手工验收所需的 Anthropic 渠道与分组已就绪，可发出真实请求验证双来源与徽标。

## 实施后待补

- 若后续需要按 beta 筛选，应先评估把逗号分隔串改为独立关联表的成本，而不是在现有列上加 `LIKE` 查询。
- 若上游将来开始回执生效的 beta，可在不改 schema 的前提下，把「客户端声明」与「上游生效」并列为两个字段。
