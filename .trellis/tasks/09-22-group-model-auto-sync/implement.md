# Implement: 分组模型自动同步开关与 claude- 前缀别名

> 对应 `prd.md` 与 `design.md`。按阶段执行，每阶段末尾跑该阶段的验证命令再进入下一阶段。

## 0. 前置约束（本机环境）

- Go 命令**一律加 `-p 1`**，且**只跑受影响的包**：`go build -p 1 ./...`、`go vet -p 1 ./...`、`go test -p 1 -count=1 ./internal/control/... ./internal/storage/...`。全量 `go test` 在本机有既有崩溃与既有 FAIL，不作为验收口径。
- `gofmt -l .` / `pnpm run format` 在本机因 CRLF 检出全仓误报，判定方式见 `.trellis/tasks` 之外的既有约定：逐文件 `tr -d '\r' < f | diff -q - <(gofmt f)`，前端用 LF 镜像目录跑 prettier。
- 前端命令用 corepack 缓存里的 pnpm 11.17.0，并前置 node 路径：
  `PATH="/c/Program Files/nodejs:$PATH" node "C:/Users/xiejincheng/AppData/Local/node/corepack/v1/pnpm/11.17.0/bin/pnpm.cjs" run <script>`
- 本任务**不进行 git 操作**（提交由用户决定）。

## 阶段 1：存储与迁移

- [ ] 1.1 `internal/storage/models/group.go`：`Group` 加字段
      `AutoSyncModels bool \`gorm:"column:auto_sync_models;not null;default:false"\``
      放在 `Enabled` 附近，保持结构体字段成组。
- [ ] 1.2 新建 `internal/storage/migrations/0020_zz_group_model_auto_sync.go`，照 `0005_proxy_config.go` 的三件套模板：
      `ID0020ZZ = "0020_zz_group_model_auto_sync"`、`Up0020ZZ`（幂等 `HasColumn` → `AddColumn`）、
      `ValidateRecoverable0020ZZ`（表不存在 → 错；列存在 → 走 Validate）、
      `Validate0020ZZ`（列必须存在、类型为布尔、不可空、默认 false）。
      本机方言断言以 SQLite 为准，类型判定用 `DatabaseTypeName()` 含 `bool` / `boolean` / `tinyint`。
- [ ] 1.3 `internal/storage/migration.go`：在 `ID0020` 之后追加注册项（顺序即 ID 字典序）。
- [ ] 1.4 三个清单测试同步：
      `internal/storage/migration_test.go` 的 `wantIDs`、
      `internal/storage/db_test.go` 的 `wantMigrationIDs`、
      `internal/storage/database_integration_test.go:143-144` 的链式断言（加 `migrationIDs[21]`，并把错误文案里的 `21-entry` 改成 `22-entry`）。
- [ ] 1.5 验证：`go test -p 1 -count=1 ./internal/storage/...`
      注意：本机 `internal/storage` 的 SQLite 集成用例可能因环境报既有失败，先确认失败项与本改动无关再继续。

## 阶段 2：控制面纯逻辑

- [ ] 2.1 新建 `internal/control/group_model_auto_sync.go`，放四个东西：
      - `claudePrefixedName(id string) (string, bool)`：`design.md` §3.4 的五条跳过规则。
      - `mergeAutoSyncModels(current []GroupModel, discovered []ModelCandidate) ([]GroupModel, bool)`：
        用 `state.RoutableModelNames` 维护 `claimed`，只增不删，返回合并结果与"是否有变化"。
        无变化时返回**原始切片**（同一底层数组），让调用方能用 `sameAutoSyncModels` 做廉价判断。
      - `sameAutoSyncModels(left, right []GroupModel) bool`：逐行比 ID 与别名序列（有序比较），
        不能复用只比 ID 集合的 `sameGroupModelIDs`。
      - `Service.GetGroupModelAutoSync` / `Service.UpdateGroupModelAutoSync`：读/写 `groups.auto_sync_models`。
        PUT 走 `s.writeGroupConfig`，事务内 `tx.Model(&models.Group{}).Where("id = ?", groupID).Update("auto_sync_models", value)`；
        值未变化时不发 UPDATE。分组不存在 → `app_errors.ErrResourceNotFound`。
- [ ] 2.2 新建 `internal/control/group_model_auto_sync_test.go`：
      - `claudePrefixedName`：正常 id → `claude-x`；`claude-x` / `Claude-X` → 跳过；`x[1m]` / `x[1M]` → 跳过；
        空串 / 超 255 字节 → 跳过。
      - `mergeAutoSyncModels`：只增不删；新增行带 `claude-` 别名；别名被别的 ID 认领 → 该行降级为空别名；
        ID 被别的 ID 认领 → 整行跳过；别名与 ID 同 ID 的多行认领互不冲突（Routable 口径）；
        无变化 → changed 为 false。
      - Service 读写：默认 false；PUT 往返；PUT 相同值不更新 `updated_at_ms`；未知分组报 `ErrResourceNotFound`；
        写开关**不改变** `groups.models` 与 `state.Manager` 的 Revision。
- [ ] 2.3 验证：`go test -p 1 -count=1 ./internal/control/ -run 'AutoSync|ClaudePrefix' -v`

## 阶段 3：调度器

- [ ] 3.1 新建 `internal/control/group_model_sync_coordinator.go`，与 `CatalogSyncCoordinator` 同构但更薄：
      - 常量：`groupModelAutoSyncInterval = time.Hour`、`groupModelAutoSyncStartupDelay = 2 * time.Minute`。
      - 字段：`service *Service`、`newTicker func(time.Duration) runtimeTicker`、`newTimer func(time.Duration) catalogSyncTimer`（复用既有 timer 接口）、`now func() time.Time`、日志字段。
      - `Run(ctx)`：`newTimer(startupDelay)` 等首次（`ctx.Done()` 可打断）→ 跑一轮 → `newTicker(interval)` 循环；`defer` 两者 Stop。
      - `syncOnce(ctx)`：查 `auto_sync_models = true` 的分组 ID → 逐组调用（见 3.2），任一组错误只 `logrus.WithError(...).WithField("group_id", id).Warn(...)` 后 `continue`；`ctx.Err()` 非空则立即返回。
- [ ] 3.2 Service 侧的一轮执行方法 `Service.syncGroupModelsOnce(ctx, groupID) (changed bool, err error)`：
      - 读分组行取 `channel_id`；channel 不支持 discovery（`channelRegistry.Capabilities` 的 `ModelDiscovery`）→ 直接返回 `false, nil`（不发请求）。
      - `DiscoverGroupModels(ctx, groupID)`；只取 `Sources` 含 `"live"` 的候选。
      - 解码当前 `group.models`（`decodeGroupDiscoveryJSON`）→ `mergeAutoSyncModels`。
      - 未变化 → 返回 `false, nil`（不写库）。
      - 变化 → `UpdateGroupModels(ctx, groupID, GroupModelsUpdateRequest{Models: optionalGroupModels{Set: true, Values: merged}})`。
        注意该结构体的构造方式需与 `group_write.go:125-161` 的 `UnmarshalJSON` 语义一致：直接构造 Go 值时 `Set` 必须显式为 true。
- [ ] 3.3 新建 `internal/control/group_model_sync_coordinator_test.go`：
      - 用 `newServiceFixture(t)` + `newFakeRuntimeTicker()` + 伪 timer 注入，驱动一轮并断言写入结果。
      - 用 `newRecordingDiscoveryExecutor`（`internal/control/discover_executor_test.go:43`）伪造上游模型列表。
      - 断言：开关关闭的分组不动；不支持 discovery 的 channel 不发请求；单组上游报错不影响同轮的第二组；
        ctx 取消后 `Run` 在 1s 内返回（watchdog 写法照 `catalog_sync_test.go:682-716`）。
      - 断言：一轮无变化时不产生写入（Revision 不变）。
- [ ] 3.4 验证：`go test -p 1 -count=1 ./internal/control/ -run 'GroupModelSync|AutoSync' -v`

## 阶段 4：modern 端点与装配

- [ ] 4.1 `internal/control/group_model_auto_sync.go` 末尾加 handler：
      `handleGetGroupModelAutoSync`、`handleUpdateGroupModelAutoSync`，请求体用严格绑定（与 `server.go:444` 的 `bindStrictJSON` 同款）。
      响应结构 `GroupModelAutoSyncResponse{ Enabled bool \`json:"enabled"\` }`，只在本文件内使用。
- [ ] 4.2 `internal/control/http_routes.go`：在 `/modern/...` 区块内注册
      `control.modern.group-model-auto-sync.get`（GET）与 `control.modern.group-model-auto-sync.update`（PUT）。
      PUT 必须带 `s.auditMutation(newMutationDescriptor("group_model_auto_sync_update", "group", groupMutationLocator))`
      （与 `control.groups.settings.update` 同款），并在 `internal/control/mutation_audit_routes_test.go` 的表格里补条目。
- [ ] 4.3 装配：
      - `internal/control/runtime.go`：`Runtime` 加字段 `groupModelSync catalogSyncRuntime`（复用既有接口，接口方法就是 `Run(context.Context)`），
        `NewRuntime` 加参数 `groupModelSync *GroupModelAutoSyncCoordinator`，`Run` 里加一个 goroutine（照 `:149-155` 的 `catalogSync` 写法）。
      - `internal/container/container.go`：在 `control.NewCatalogSyncCoordinator` 之后注册 `control.NewGroupModelAutoSyncCoordinator`。
- [ ] 4.4 端点测试：
      - GET 默认 `{"enabled":false}`；PUT 往返；体缺字段 / 非布尔 → `ErrValidation`；未知分组 → `ErrResourceNotFound`。
      - 断言 `/modern` 前缀不进入 `server_test.go` 的 `/groups` GET 清单断言（既有测试保持绿）。
      - 断言 `classic_api_contract_test.go` 保持绿（本次不加任何共享 DTO 字段）。
- [ ] 4.5 验证：`go build -p 1 ./... && go vet -p 1 ./... && go test -p 1 -count=1 ./internal/control/...`

## 阶段 5：modern 前端

- [ ] 5.1 `web/src/shared/models/model-aliases.ts`：新增纯函数
      `claudePrefixedAlias(id: string): string | null`（大小写不敏感的 `claude-` 前缀跳过、`[1M]`/`[1m]` 后缀跳过、空串跳过）
      与 `withClaudePrefixedAliases(rows, additions)`（按已认领名称集合逐行判定，冲突降级/跳过）。
      保持该文件"纯逻辑、零前端依赖"的既有性质。
- [ ] 5.2 `web/src/frontends/modern/api/group-detail.ts`：
      加 `groupModelAutoSyncKey(id)`、`getGroupModelAutoSync`、`saveGroupModelAutoSync`，读响应只认 `enabled` 布尔。
- [ ] 5.3 `web/src/frontends/modern/features/groups/GroupModelsPanel.vue`：
      - `#actions` 插槽内在「同步模型」按钮之后加 `<span>` + `AppSwitch`（`size="sm"`），显隐条件与同步按钮一致（`channel?.discovery`）。
      - 开关状态走独立 `useQuery`，加载中或保存中 disabled。
      - 切换即 `saveGroupModelAutoSync`；成功后更新缓存 + `useMessageSource` 成功提示；失败回滚到上一个已知值 + danger 提示。
      - **不得**接入 `draft` / `baseline` / `signature()` / `dirty`。
      - 抽屉尺寸改成新的 `sheet-wide` 档（见 5.4）。
- [ ] 5.4 抽屉尺寸档：
      `styles/tokens.css` 加 `--modern-dialog-sheet-wide-width: 820px`（保留 `--modern-dialog-sheet-width: 660px`）→
      `components/ui/AppDialogContent.vue` 的 `size` union 与尺寸类加 `'sheet-wide'` →
      `features/groups/GroupEditorSurface.vue` union 同步 →
      `features/groups/GroupWorkspacePanel.vue` 的 `wide?: boolean` 换成 `size?: 'default' | 'sheet' | 'sheet-wide'`（默认 `'default'`）→
      `GroupModelsPanel.vue` 传 `size="sheet-wide"`。
      改完确认 `CredentialDetailPanel` / `GroupAdvancedPanel` / `GroupCredentialAddPanel` / `GroupCreatePanel` 行为不变。
- [ ] 5.5 `web/src/frontends/modern/features/groups/GroupModelSyncDialog.vue:57-69`：
      `next` 里新增行的 `aliases` 改为经 5.1 的纯函数生成；结果必须是新数组。
- [ ] 5.6 `web/src/frontends/modern/i18n/locales/group-workflows.ts`：三个语言对象同步加同名 key
      （开关可见文案、给 `AppSwitch.label` 的 tooltip/aria 文案、保存成功/失败提示）。
- [ ] 5.7 验证（顺序执行，全部通过才算完成）：
      - `pnpm run type-check`
      - `pnpm run lint`（含 `check:styles`：新样式不得出现裸 px/rem，颜色必须取 `--modern-*`）
      - LF 镜像目录跑 `prettier --check`（判定方式见 §0）
      - `pnpm run build`

## 阶段 6：整体验收

- [ ] 6.1 对照 `prd.md` 的 AC1-AC11 逐条核对，把结论写进 `check.md` 或任务 notes。
- [ ] 6.2 回归面确认：
      - classic 相关 Go 契约测试绿（`go test -p 1 -count=1 ./internal/control/ -run Classic`）。
      - `server_test.go` 的 `/groups` GET 路由清单测试绿。
      - `/v1/models` 与路由索引未受影响：`go test -p 1 -count=1 ./internal/state/... ./internal/gateway/...`。
- [ ] 6.3 已知不做验证的项（需在 wrap-up 里如实说明）：浏览器端实际点击验收、自动同步真实周期到时验证（周期 1 小时，静态测试用伪 ticker 覆盖）。

## Review gate

- 进入 Phase 2（`task.py start`）前，需要大哥确认：`prd.md`、`design.md`（尤其 §2 三个取舍）、本文件的阶段划分。
- 阶段 4 结束时做一次中途 review：端点契约与调度器行为是否与预期一致，再决定是否继续前端。

## 回滚点

| 阶段 | 回滚方式 |
|---|---|
| 1 之后 | 迁移一旦在本地库应用即不可逆；回滚需删列或换库。开发期用内存 SQLite 夹具，影响仅限开发机。 |
| 2-4 之后 | 代码回滚即可；`auto_sync_models` 列被旧代码忽略。 |
| 5 之后 | 前端回滚即可；开关停留在服务端的值不影响旧前端（旧前端根本不读它）。 |
