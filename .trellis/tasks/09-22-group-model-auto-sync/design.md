# Design: 分组模型自动同步开关与 claude- 前缀别名

> 对应 `prd.md` R1-R4。本文只讲技术设计：边界、契约、数据流、取舍、兼容与回滚。

## 1. 范围与边界

| 层 | 改动 | 不碰 |
|---|---|---|
| 存储 | `groups` 表加一列 + 一个 fork 迁移 | 现有列语义、`groups.models` 的 JSON 形状 |
| 控制面 | 新调度器 + 新 modern 端点 + 分组模型合并写入 | `UpdateGroupModels` 的对外契约、settings 通道 |
| 网关/编译 | 无 | `state.Compile`、路由索引、定价 |
| modern 前端 | 开关 UI、同步对话框别名生成、抽屉尺寸档 | 面板的草稿/脏检查语义 |
| classic 前端 | **零改动** | 全部 |

## 2. 三个关键取舍（含被否方案）

### 2.1 开关持久化：新增独立列，不进 `groups.overrides`

**被否：把开关做成分组可覆盖的 runtime setting（`overrides` 里的新键）。** 三处硬阻塞：

1. classic 对 `overrides` / `effective` 的**内部键**做白名单断言（`web/src/frontends/classic/app/resources/groups.ts:356` → `projector.ts:134-144` 的 `assertNoSecretLikeFields`），未知键直接 `invalidResponse()`，classic 分组设置页会整页打不开。要塞进去就必须改 classic 白名单。
2. modern 的 `RuntimeSettings` 类型与 `readRuntime`（`web/src/frontends/modern/api/group-detail.ts:34-42,78-102`）只认识 `runtimeNumbers` / `runtimeSwitches` 里的键，不登记就会被静默丢弃；而 `GroupAdvancedPanel` 保存 `overrides` 时按服务端读回的值**整体重建**，丢掉的键会在下一次保存时被删除。登记它又要连锁改 `readSettings` 的 `effective` 完备性断言（`:107-112`），进而要求 `GroupEffectiveConfigResponse` 多一个字段，而该结构同样受 classic 白名单管辖。
3. 语义错位：`ResolvedGroupSettings` 描述的是影响请求处理的运行时语义（超时、亲和性、参数覆写）。一个只驱动后台管理任务的开关放进去没有对应概念，只能"加 case 但不消费"，属于为绕过校验而写代码。

**采纳：`groups.auto_sync_models BOOLEAN NOT NULL DEFAULT false`。** 语义独立、读写路径独立、classic 完全无感。

代价：需要一个 fork 迁移。按仓库既有规范用 `<上游tip号>_zz_<name>` 锚定式编号（见 `.trellis/spec/backend/database-guidelines.md`），当前上游 tip 是 `0020`，故为 `0020_zz_group_model_auto_sync`。

### 2.2 暴露通道：modern 专属端点

**被否：并入 `GroupModelsResponse` 或 `GroupSettingsResponse`。** 两者都是共享 DTO，`internal/control/classic_api_contract_test.go:37-40` 要求它们的每个导出 JSON 字段出现在 classic 白名单里，加字段就必须动 classic。

**采纳：`/api/modern/groups/:group_id/model-auto-sync`（GET / PUT）。** 依据：

- modern 已有同名空间先例（`/modern/groups/:group_id/credentials`，`internal/control/http_routes.go:194`）。
- 路由契约测试只筛选 `strings.HasPrefix(route.Path, "/groups")` 的 GET 路由（`internal/control/server_test.go:95-97`），`/modern` 前缀不在其断言范围内。
- 不进入 classic 的任何读取路径，满足"classic 零改动"。

代价：面板打开时多一个请求；开关在加载完成前必须禁用（不能让用户在未知状态上切换）。

### 2.3 调度器：照抄 `CatalogSyncCoordinator` 的形状，挂进 `Runtime`

不新建调度框架，也不复用 `CatalogSyncCoordinator` 本体（那是 models.dev 定价目录的职责）。新增 `GroupModelAutoSyncCoordinator`，与既有协调器同构：

- `Run(ctx)` + `newTicker` / `newTimer` 注入字段（`internal/control/runtime.go:46-49` 的 `runtimeTicker` 接口可直接复用）。
- 由 `control.NewRuntime` 持有（`internal/control/runtime.go:78-119`），在 `Runtime.Run` 里起 goroutine（`runtime.go:149-155` 旁）。
- 通过 `internal/container/container.go` 的 dig provider 列表注册（`:194-195` 旁）。
- 服务停止由 `App.Stop` 的 `cancelRuntime` + 等待 `runtimeDone` 覆盖（`internal/app/app.go:224-238,285-325`），无需新代码。

## 3. 数据契约

### 3.1 存储

```
groups.auto_sync_models  BOOLEAN NOT NULL DEFAULT false
```

- `internal/storage/models/group.go` 加 `AutoSyncModels bool` 字段。
- 迁移 `internal/storage/migrations/0020_zz_group_model_auto_sync.go`，三件套 `Up0020ZZ` / `Validate0020ZZ` / `ValidateRecoverable0020ZZ`（`migration.go:179-181` 要求三者非 nil）。
- 注册与清单同步（详见 `implement.md` 的清单）。

该列**不参与** `state.CompileInput`。改组该列的写入走 `writeGroupConfig` 只是为了复用写锁与事务，不引入快照重编译。

### 3.2 HTTP

```
GET  /api/modern/groups/:group_id/model-auto-sync  -> 200 {"enabled": false}
PUT  /api/modern/groups/:group_id/model-auto-sync  <-  {"enabled": true}
                                                    -> 200 {"enabled": true}
```

- 请求体严格解码（`bindStrictJSON`），字段缺失或类型不符返回 `ErrValidation`。
- 分组不存在返回 `ErrResourceNotFound`。
- PUT 是幂等写：值未变化时不更新 `updated_at_ms`（与"无变化不写"的取向一致），仍返回当前值。

### 3.3 自动同步一轮的算法

```
for each group where auto_sync_models = true:      -- 单轮串行，逐组隔离错误
    1. discovery 能力判定：channel 不支持则跳过（不发请求）
    2. upstream := DiscoverGroupModels(ctx, groupID)   -- 不持 writeMu；内部 30s 超时
    3. live := upstream 中 Sources 含 "live" 的候选      -- 排除 catalog-only
    4. current := 解码 group.models
    5. merged := mergeAdditions(current, live)
    6. merged 与 current 完全相等 -> 跳过（不写库、不 RequestGroupSync）
    7. UpdateGroupModels(ctx, groupID, merged)          -- 复用既有写路径
```

关键点：

- **第 2 步不持锁**：`DiscoverGroupModels` 本身不取 `writeMu`（`internal/control/discover_group_test.go:742-802` 有此断言），只有第 7 步才进入写路径。
- **第 7 步复用 `UpdateGroupModels`**（`internal/control/group_models.go:96-163`）：它已包含 `normalizeGroupModels` 冲突检测、事务内 `validateGroupRowCandidate` 编译校验、以及"模型 ID 集合变化时 `RequestGroupSync`"的定价重算触发。该接口是全量替换语义，而这里传入的就是"旧值 + 新增"的完整列表，语义正确。
- **单组失败隔离**：任一组返回错误只记录日志并 `continue`，不中断本轮；`ctx` 取消则整轮退出。

### 3.4 新增合并与 `claude-` 前缀别名

`mergeAdditions(current, live)`：

```
claimed := {}   // name -> 认领它的模型 ID；用 state.RoutableModelNames 得到，与写路径同口径
for row in current: for name in RoutableModelNames(row): claimed[name] = row.ID

merged := current
for candidate in live where candidate.id 不在 current 的 ID 集合中:
    id := trim(candidate.id)
    aliases := []
    if alias, ok := claudePrefixedName(id); ok && !claimedByOther(alias, id):
        aliases = [alias]
    if claimedByOther(id, "")  -> continue          // ID 本身被别的模型认领，跳过该模型
    merged += {ID: id, Aliases: aliases}
    更新 claimed
```

`claudePrefixedName`：

- `id` 去空白后为空 -> 不加。
- `id` 已以 `claude-` 开头（大小写不敏感）-> 不加。与 `normalizeModelAliases` 丢弃"等于 ID 的别名"同源语义。
- `id` 以 `[1M]` / `[1m]` 结尾 -> 不加。带上下文后缀的名字会额外认领基名（`internal/modelname/modelname.go:20-30`），前缀叠加后与别的条目撞基名的概率不可控，保守跳过。
- `id` 超过 255 字节 -> 不加（`maxModelNameBytes`）。
- 该行已有 10 个别名 -> 不加（存量行不受影响，此规则只对新行生效，新行总是从空别名开始）。
- 否则返回 `claude-<id>`。

**冲突跳过的粒度是名称，不是行**：别名冲突时该行以空别名加入，模型本身照常同步；只有模型 ID 本身与组内既有名称冲突时才整行跳过。这与 PRD AC8 一致，也保证自动同步在无人值守时不会因为一条冲突而卡死。

**为什么不做"整批失败"**：`normalizeGroupModels` 遇到冲突会拒绝整批写入（`group_write.go:269-274`）。自动同步若直接把发现结果交给它，一条冲突会让该分组永远同步不进去。因此合并阶段就必须按 Routable 口径逐行试算，保证交给写路径的列表一定无冲突。

### 3.5 前端别名生成（手动同步对话框）

同一套规则的前端副本，落在 `web/src/frontends/modern/features/groups/GroupModelSyncDialog.vue:57-69` 的 `next` computed：

- 新增行从 `aliases: []` 改为按 `claudePrefixedName` 生成，并在追加前用已认领名称集合判断冲突，冲突则退回空别名。
- 纯函数放 `web/src/shared/models/model-aliases.ts`（spec `.trellis/spec/backend/model-name-contract.md:235-245` 要求前端别名纯逻辑只有一份实现）。classic 只是 re-export，不调用即无影响。
- 生成结果必须是**新数组**：面板与对话框共享行对象（`GroupModelSyncDialog.vue:18` 是浅拷贝），就地改写会污染 `signature()` 基线。
- 前端沿用 External 口径（`findModelNameConflicts` 的现状），不做后缀派生建模，只靠"ID 带后缀就不加别名"这条保守规则避开后端 Routable 口径的差异。已知残余：`claude-<id>` 与组内既有的 `claude-<id>` 别名同名时，对话框的预检会照旧禁用「确认同步」，用户需手工清理 —— 这是既有的冲突交互，不因本次改动而变差。

### 3.6 抽屉宽度

新增一个尺寸档，不动共享的 `660px`：

- `web/src/frontends/modern/styles/tokens.css` 新增 `--modern-dialog-sheet-wide-width`（820px），`--modern-dialog-sheet-width` 保持 660px。
- `AppDialogContent.vue` 的 `size` union 加 `'sheet-wide'` 与新尺寸类。
- `GroupEditorSurface.vue` 的 `size` union 同步加档。
- `GroupWorkspacePanel.vue` 的 `wide?: boolean` 换成 `size?: 'default' | 'sheet' | 'sheet-wide'`，默认 `'default'`，直接透传。
- `GroupModelsPanel.vue` 传 `size="sheet-wide"`。

`HealthDetailPanel`、`ModelCatalogSettingsDialog`、以及不传 wide 的三个面板（`CredentialDetailPanel` / `GroupAdvancedPanel` / `GroupCredentialAddPanel`）行为不变。

### 3.7 工具栏开关

- 位置：`GroupModelsPanel.vue:173-184` 的 `#actions` 插槽内、「同步模型」按钮之后。插槽落点在 `GroupModelPicker.vue:270`，前后是「选择模型」与「手动添加」，插槽内顺序由面板决定。
- `AppSwitch` 的 `label` 只做 tooltip 与 `aria-label`，不渲染文本（`components/ui/AppSwitch.vue:23,31`），因此可见文案要自己排 `<span>`，沿用 `GroupBasicsForm.vue:221-224` 的写法。
- 尺寸：工具栏与 `size="sm"` 按钮同排，开关取 `size="sm"`。
- 状态：独立 `useQuery` + 即时 `PUT`，成功后就地更新缓存并给一次轻量成功反馈（复用 `useMessageSource`）。**不进入** `draft` / `baseline` / `signature()` / `dirty`，与模型草稿完全解耦。
- 保存失败：回滚开关到上一个已知值并给出 danger 提示，不静默停留在一个假状态上。

## 4. 数据流

```
[UI] 开关切换
  -> PUT /api/modern/groups/:id/model-auto-sync {enabled}
  -> Service.UpdateGroupModelAutoSync -> writeGroupConfig: groups.auto_sync_models
  -> 响应 {enabled} -> 面板内使用 v-model 渲染

[后台] GroupModelAutoSyncCoordinator.Run(ctx)
  -> ticker(1h) / 首次延迟(2m)
  -> 查询 auto_sync_models = true 的分组
  -> 逐组：DiscoverGroupModels -> mergeAdditions -> UpdateGroupModels
       -> normalizeGroupModels -> writeGroupConfig -> (ID 集变化) catalogSync.RequestGroupSync
       -> state.Manager.Publish -> 网关下一请求即读到新 Revision

[UI] 「同步模型」对话框确认
  -> next 里为新增行生成 claude-<id> 别名
  -> 复用既有 save() -> PUT /groups/:id/models
```

## 5. 兼容性与回滚

- **前向兼容**：旧数据该列取默认 `false`，行为与今天完全一致；迁移只加列，不改任何存量行。
- **回滚**：`auto_sync_models` 列留库不影响旧版本运行（旧代码不读它）；关掉所有分组的开关即等于停用功能，无需回滚数据库。回退到不含本功能的镜像时，该列成为孤儿列，被忽略。
- **迁移不可逆**：仓库既有约定是没有 down 迁移，本次同样不提供。
- **运行期风险**：自动同步会按周期向上游发起 `OperationListModels` 请求。默认周期 1 小时、单组 30s 超时、串行执行，规模可控。分组删除后不再参与（查询按 group ID 现取现查）。

## 6. 测试策略

后端（Go 测试是唯一自动化验证手段；前端按 `web/README.md:53-55` 不新增测试，走 `make check`）：

| 对象 | 断言 |
|---|---|
| `mergeAdditions` 纯函数 | 只增不删；`claude-` 开头不加别名；后缀结尾不加别名；别名冲突则该行降级为空别名；ID 冲突则跳过整行；无变化返回相等列表 |
| 调度器 | 伪 ticker 驱动；只处理开关打开且 discovery 支持的分组；单组失败不影响后续组；ctx 取消后 1s 内退出（对齐 `catalog_sync_test.go:682-716` 的 watchdog 写法） |
| 新端点 | GET 默认 false；PUT 往返；非布尔/缺字段 -> `ErrValidation`；未知分组 -> `ErrResourceNotFound`；不改变 `groups.models` 与快照 Revision |
| 迁移 | 三个清单测试 + `database_integration_test.go` 链长；`go vet ./...`（编译测试文件） |
| 契约 | `classic_api_contract_test.go` 不变绿即可（本次不加共享 DTO 字段）；`server_test.go` 的 `/groups` GET 清单不变 |

## 7. 遗留与后续（本次不做）

- 自动同步的"上次执行时间 / 上次结果 / 上次错误码"展示：已有可照抄的先例（`catalogSync.readStatus()` → `ProjectModelCatalogStatusDTO`，`internal/control/project_model_collection.go:355-361`），按 PRD 留到后续迭代。
- 自动清理（删除上游已下线模型）：刻意不做。
- 同步周期可配置：刻意不做。
