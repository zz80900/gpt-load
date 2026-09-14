# 执行计划：分组模型多别名

## 提交切分

| Commit | 范围 | 可否单独部署 |
|---|---|---|
| 1 | Go 全量（数据模型 + 契约 + 消费点 + 测试字面量） | ❌ 服务端已改响应、前端未改 |
| 2 | 前端（类型/projector + TagInput + 编辑器 + 连带点 + i18n） | ❌ 依赖 Commit 1 |
| 3 | 周边（发布冒烟脚本 + 路由审计测试请求体） | — |

Commit 1 与 2 **必须同批合并**（design.md 风险 3），分两个 commit 只为便于 review。

## Commit 1：Go 全量

按依赖顺序改，每步保持可编译：

1. `internal/state/snapshot.go`
   - `ModelConfig{ID, Aliases}`；新增并导出 `ExternalModelNames`；**删除** `externalModelName`
   - `compileExecutionTargets` 改为「预计算注册表 → 遍历注册」
   - `validateCompileInput` 改所有权判定
2. `internal/state/loader/loader.go`
   - `modelDTO` 加 `Aliases`；新增 `effectiveAliases()`；`:632` 改用它
3. `internal/control/group_write.go`
   - `GroupModel` 改字段；`UnmarshalJSON` 加兼容分支
   - 简化 `optionalGroupModels`，删 `groupModelRequestWire`
   - 重写 `normalizeGroupModels`（去重/去空/去 ID/上限 10/长度 255）+ 冲突检测改集合相交
4. `internal/control/group_models.go`
   - 响应 DTO 改 `Aliases` + `ClientModels`；`mapGroupModelsResponse` 同步
5. 其余消费点：`group_create.go`、`group_update.go`、`group_options.go`、`group_collection.go`、`home.go`、`project_model_collection.go`
6. 同步修完所有 Go 测试字面量（清单见下）

**门禁**：`go build ./...` 通过；`go test -count=1 ./internal/state/... ./internal/control/... ./internal/gateway/...` 通过。
（本机需 `-p 1` 规避火绒 HIPS 与 Go 工具链的 `CreateProcess` 冲突。）

## Commit 2：前端

1. `web/src/api/control/types.ts`、`web/src/app/resources/groups.ts`
   - DTO 与 projector 白名单字段改 `aliases` / `client_models`
   - 加不变量：`aliases` 无重复且不含 ID；`client_models[0] === id` 且长度匹配
   - `projectGroupModels` 的同名判定改**跨条目扁平化**取重
2. 新建 `web/src/components/ui/TagInput.vue`（交互规格见 design.md §6）
3. `web/src/features/models/model-draft.ts`
   - `ModelDraftValue.aliases: string[]`；新增 `normalizeAliases`；删除 `indexesWithEmptyAliases`；冲突校验改集合相交
4. `web/src/features/models/ModelAliasEditor.vue`（改造点见 design.md §6）
5. 连带点：`model-diff.ts`、`GroupSettingsBaseForm.vue`、`GroupModelSyncDialog.vue`、`import/model-draft.ts`、`NewGroupImport.vue`、`import-recovery.ts`
6. `zh-CN/group.ts` 文案增删（en-US / ja-JP **不动**）

**门禁**：`pnpm --dir web run lint` + `format` + `build`（build 内含 `vue-tsc`）。

## Commit 3：周边

- `.github/scripts/release-docker-smoke.sh:310` 请求体 → `{id,aliases:[]}`
- `internal/control/mutation_audit_routes_test.go:1127/1145/1243` 请求体改新形态

## 测试更新清单

### 必须重写（语义变化）

- `internal/state/snapshot_test.go:298` — `{ID:"b",Alias:"a"}` → `Aliases: []string{"a"}`，仍是跨 ID 同名错误
- `internal/state/snapshot_test.go:25-26` — 存量代表性用例改 `Aliases: []string{...}`
- `internal/control/group_models_test.go:94` — `TestNormalizeGroupModelsAppliesAliasSwitchAndReportsStableConflicts` 逐条改写（去重、大小写、trim、同 ID 合并、跨 ID 冲突、indexes 顺序）
- `internal/control/group_models_test.go:41-57` — 响应映射改 `aliases`/`client_models`
- `internal/control/server_test.go:1534` — `TestGroupModelsHTTPRejectsMissingAliasEnabledWithoutMutation` 改名并改为断言「旧 wire 形态仍被接受」

### 必须新增

- `snapshot_test.go`：多别名各注册一次且 `UpstreamModelID` 一致；**同 ID 多条目合并**（守住去重不变量）；`Aliases:["a"]` 与 ID 相同 → 单名；重复别名去重
- `loader_test.go`：**旧 JSON `alias` 单字段读取兼容**（唯一的存储迁移守卫）
- `group_models_test.go`：更新后落库 JSON 只含 `aliases`
- `internal/gateway/models_test.go`：含 ID + 2 别名的模型在 `/v1/models` 出现 3 次，且 3 个名字都能路由

### 只做字面量替换（`Alias:` → `Aliases: []string{...}`）

`group_update_test.go`、`channel_group_foundation_test.go`、`group_idempotency_phase1_test.go:58`（**语义不要改**）、`credential_stages_test.go`、`validation_test.go`、`route_inspect_test.go`、`internal/gateway` 约 25 处。

### 有意保留旧格式

存储层 fixture 里保留 `alias` 字段的测试（`home_test.go`、`model_price_test.go`、`project_model_collection_test.go`、`model_prices_http_test.go`、`group_options_test.go`、`price_reconcile_test.go`、`group_collection_query_test.go`、`group_collection_http_test.go`、`catalog_sync_test.go`）**不要改成新格式**——它们是存储兼容与存量数据的回归覆盖。在提交信息中写明这是有意为之，避免 review 时被"顺手修正"。

## 端到端验证

1. 启动前后端（`go build -p 1` 后运行二进制 + `pnpm --dir web run dev`）
2. 给某模型配 2 个别名，保存
3. 分别用 **ID / 别名1 / 别名2** 请求 `/v1/chat/completions`，确认三者都路由到同一上游（对照请求日志的 `upstream_model`）
4. 复核 PRD 的 AC3–AC9

## 手工验收清单（前端）

回车生成标签 / `a,b,c` 粘贴拆 3 个 / 空输入退格删末标签 / 重复标签被拒 / 与 ID 相同的别名被丢弃 / 跨行同名冲突在正确列高亮 / 失焦提交未提交文本 / 保存后刷新顺序不变
