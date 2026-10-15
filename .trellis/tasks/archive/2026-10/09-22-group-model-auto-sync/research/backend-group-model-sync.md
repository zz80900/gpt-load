# Research: 分组级模型自动同步（后端调研）

- **Query**: 为「分组级持久化自动同步开关 + 周期拉取上游模型 + 自动补 `claude-<id>` 别名」做后端链路调研
- **Scope**: internal（Go 后端）；附带 frontend 契约约束
- **Date**: 2026-09-22

---

## 1. 分组模型的持久化结构

### 1.1 表列

`internal/storage/models/group.go:19-20`

```go
Params JSON `gorm:"type:json;not null"`
Models JSON `gorm:"type:json;not null"`
```

- `Models` 是 `models.JSON`（`[]byte` 别名，`internal/storage/models/json.go:11`），DB 列为 `json`。
- 没有独立的 model 表；模型列表整体以 JSON 数组存在 `groups.models`。
- `BeforeSave` 只归一化 `Params`（`internal/storage/models/group.go:34-40`），**不碰 `Models`**。

### 1.2 JSON 元素的确切形状

落库元素是 `control.GroupModel`（`internal/control/group_write.go:32-37`）：

```go
type GroupModel struct {
	ID      string   `json:"id"`
	Aliases []string `json:"aliases"`
	AliasEnabled bool `json:"-"`   // 仅解码期兼容旧客户端，不落库、不出响应
}
```

即磁盘上只有两个字段：`{"id":"...","aliases":["..."]}`。`aliases` **没有** id/key 之外的任何包装结构，就是字符串数组（可以为 `null`，读路径会把 `nil` 归一成 `[]`，见 `internal/control/group_models.go:75-78`）。

### 1.3 解析方：严格解码 + 旧格式兼容

`GroupModel.UnmarshalJSON`（`internal/control/group_write.go:39-68`）：

- `DisallowUnknownFields()`，拒绝未知字段与尾随 JSON 值。
- 兼容单数旧字段 `alias`（`alias_enabled` 被读取但**刻意不参与**决策，注释说明见 `group_write.go:60-62`）：`aliases` 为空且 `alias` 非空时，把 `alias` 提升为 `[]string{alias}`。
- **含义**：任何往 `groups.models` 里写数据的代码，都必须产出 `{"id","aliases"}` 两字段；写 `name`/`enabled`/`weight` 之类的字段会让后续读路径（严格解码）直接失败。

### 1.4 其它解码点（口径一致性）

| 位置 | 用途 |
|---|---|
| `internal/control/group_models.go:56-59` | GET 分组模型 |
| `internal/control/group_update.go:33-47` | `mapGroupRowToState`：DB 行 → `state.GroupConfig.Models`（喂给编译） |
| `internal/control/group_settings.go:397-404` | `groupProbeModel`：取第一条模型的 ID 当默认探测模型 |
| `internal/control/group_collection.go:344-351` | 分组列表的 `model_count` |
| `internal/control/group_options.go:126` | 分组选项候选名 |
| `internal/control/project_model_collection.go:413-414` | 访问密钥范围过滤 |

统一入口是 `decodeGroupDiscoveryJSON`（`internal/control/discover_group.go:247-261`）：`UseNumber()` + 拒绝多值。

---

## 2. 保存分组模型的完整链路

### 2.1 路由与 handler

`internal/control/http_routes.go:243-259`

| Route name | Method | Path | Handler |
|---|---|---|---|
| `control.groups.models.get` | GET | `/groups/:group_id/models` | `Server.handleGetGroupModels`（`internal/control/server.go:456-467`） |
| `control.groups.models.update` | PUT | `/groups/:group_id/models` | `Server.handleUpdateGroupModels`（`internal/control/server.go:438-454`），带 `auditMutation("group_update_models","group")` |
| `control.group-models.discover` | POST | `/groups/:group_id/models/discover` | `Server.handleDiscoverGroupModels`（`internal/control/server.go:849-864`） |

PUT 用 `bindStrictJSON` 严格绑定（`server.go:444`）。

### 2.2 请求 DTO

`internal/control/group_models.go:16-18`

```go
type GroupModelsUpdateRequest struct {
	Models optionalGroupModels `json:"models"`
}
```

`optionalGroupModels.UnmarshalJSON`（`internal/control/group_write.go:125-161`）：
- `null` → 返回 `ErrValidation`（不是「清空」）；
- 逐元素走 `GroupModel.UnmarshalJSON` 的严格语义；
- `Set` 标志用于区分「字段缺失」和「空数组」；缺失时 `UpdateGroupModels` 报 `ErrValidation`（`group_models.go:104-106`）。

**注意：PUT 是全量替换语义**（`TestUpdateGroupModelsReplacesAuthoritativeListAndPublishesOnce`，`internal/control/group_models_test.go:279-347`）。要做「只增不删的合并」，必须自己读现有的 `group.models`、在内存里合并、再整体 PUT/写库。

### 2.3 响应 DTO

`internal/control/group_models.go:20-33`

```go
type GroupModelResponse struct {
	ID string `json:"id"`
	Aliases []string `json:"aliases"`      // 必须序列化为 [] 而非 null
	ClientModels []string `json:"client_models"` // == state.ExternalModelNames，即 [id, ...aliases]
	PricingStatus PricingStatus `json:"pricing_status"`
}
type GroupModelsResponse struct { Items []GroupModelResponse; Total int; Pending int }
```

`client_models` 的顺序不变量由 `state.ExternalModelNames` 保证（`internal/control/group_models.go:82-83`，定义在 `internal/state/snapshot.go:81-95`）。

### 2.4 保存路径的三道校验（口径**故意不一致**）

保存路径 = `UpdateGroupModels`（`internal/control/group_models.go:96-163`）：

1. **写入前** `normalizeGroupModels(request.Models.Values)`（`internal/control/group_write.go:226-276`）→ 规范化 + 冲突检测，冲突返回 `app_errors.ErrModelNameConflict` 并带 `ModelNameConflictData`（`ModelNameConflict{ClientModel, Indexes}`，`group_models.go:35-42`）。
2. **事务内** `validateGroupRowCandidate`（`internal/control/group_update.go:69-`）→ 把整行编译成 `state.CompileInput` 并跑 `state.Compile`，能编译过才算合法。此处有意做了「存量行坏、本次修复」的特殊处理，注释在 `group_models.go:128-133`。
3. **事务内** 写 `tx.Model(&models.Group{}).Where("id = ?").Update("models", group.Models)`（`group_models.go:141-145`）。

`validateGroupCollectionModels`（`internal/control/group_collection.go:456-475`）**不是保存路径**，它只在读路径用：分组列表（`group_collection.go:352`）与分组选项（`group_options.go:126`），口径是 **External 名称集**、允许存量脏数据通过，避免 500 掉整个列表页。三者分工在 `.trellis/spec/backend/model-name-contract.md:100-127` 有表格化说明。

### 2.5 冲突规则与 `claude-*[1m]` 语义

`normalizeGroupModels`（`group_write.go:226-276`）的认领集合是 `state.RoutableModelNames`：

- `RoutableModelNames`（`internal/state/snapshot.go:115-135`）= `ExternalModelNames` 每项之后紧跟其「剥掉上下文后缀的基名」，去重、保序。
- 上下文后缀由 `modelname.ContextSuffixes = [...]string{"[1M]", "[1m]"}` 定义（`internal/modelname/modelname.go:14`），`Base()` 在 `modelname.go:20-25`：名字必须**严格长于**后缀才剥离，所以别名 `[1M]` 单独存在时不产生派生名。
- 判定规则：**同一分组内、字符串精确相等、被两个不同上游 ID 认领**才算冲突；同一个 ID 的多行重复认领同名是合法的「一个模型两个名」（`group_write.go:229-231` 注释 + `model_collection.go` 里的重复行基线）。
- 因后缀派生的冲突必须连来源与占用者一起报（`internal/state/snapshot.go:171-196`，`modelNameConflictError` / `derivedNameSource`）。
- `*` 通配符以字面量参与派生：别名 `claude-*[1m]` 额外认领 `claude-*`（`snapshot.go:110-114` 注释；`snapshot_test.go:111-112`）。

**`claude-*[1m]` 与本次 `claude-<id>` 的关系**：前者是模式（`modelname.IsPattern`），一个分组内互相冲突（`claude-*` vs `claude-*`）；后者是普通字面别名。`claude-<id>` 与 `claude-*` 是不同字符串，按「不同字符串不冲突」的规则可以共存（`model-name-contract.md:118`）。两者语义互不干扰，但注意若某分组已有 `claude-*[1m]`，它并不妨碍 `claude-<id>` 写入。

### 2.6 别名规范化函数

`normalizeModelAliases`（`internal/control/group_write.go:195-216`）：

- 逐项 `TrimSpace`；
- 丢弃空串与**等于模型 ID** 的项；
- 按首次出现去重（大小写敏感的精确比较）；
- 保持输入顺序；
- 上限：`maxAliasesPerModel = 10`、`maxModelNameBytes = 255`（`group_write.go:27-30`），超限返回 `ErrValidation`（**是错误，不是静默丢弃**）。

对本次功能的含义：合并新增模型时，如果给某条追加 `claude-<id>` 会让该条别名超过 10 个，整次保存都会 `ErrValidation`，需要按 PRD「冲突则跳过该项别名」处理。

---

## 3. 模型发现链路

### 3.1 `Service.DiscoverModels`（草稿预览路径）

`internal/control/discover.go:33-154`

入参 `ModelDiscoveryRequest`（`discover.go:18-25`）：`channel_id / connection_type / params / credentials / staged_credential_id / proxy`。
返回 `ModelDiscoveryResult{Models []ModelCandidate}`（`discover.go:27-29`），`ModelCandidate` 定义在 `internal/control/model_candidate.go:14-20`（`ID / Name / Sources / PricingStatus / PricingSource`）。

这是「对话框里填参数、还没落库」的路径，会走 `state.Compile` 造一个合成 draft 分组（`discover.go:124-135`）。

### 3.2 `Service.DiscoverGroupModels`（已落库分组路径，自动同步应该用这个）

`internal/control/discover_group.go:23-48`

```go
func (s *Service) DiscoverGroupModels(ctx context.Context, groupID uint) (ModelDiscoveryResult, error)
```

- 读取快照 `readGroupDiscoverySnapshot`（`discover_group.go:82-126`）：`withReadSnapshot` 里查 group、`status = active` 的 credentials、全部 system settings。
- 无凭据 → `app_errors.ErrNoActiveCredential`（`discover_group.go:151-153`）；分组不存在 → `ErrResourceNotFound`（`discover_group.go:148-150`）。
- 内部分叉见 3.3。
- **不写任何状态**：`TestDiscoverGroupModels...` 断言 discovery 前后持久化/运行时状态完全相同（`internal/control/discover_group_test.go:703-737`）。
- **不拿 `writeMu`**：`discover_group_test.go:742-802` 断言 discovery 不会被写锁阻塞、也不阻塞并发写。这对自动同步很关键——拉取阶段不应持锁，只有合并落库阶段才该进 `writeGroupConfig`。

### 3.3 「哪些 channel 支持 discovery」的判定

两套字段，用途不同：

1. 公开投影（给前端）：`channel.CapabilityDescriptor.ModelDiscovery bool`（`internal/channel/channel.go:93-98`），由 `publicCapabilities`（`internal/channel/compiler.go:537-558`）计算：`绑定非空 或 存在任一 OperationListModels 路由` → true。前端 modern 读它是 `web/src/frontends/modern/api/group-create.ts:100`（`discovery: boolean(capabilities.model_discovery)`）。
2. 内部绑定（给 control 用）：`registry.CapabilityBindings(id) (spec.CapabilityBindings, bool)`（`internal/channel/channel.go:462-468`），结构在 `internal/channel/spec/definition.go:288-293`：

```go
type CapabilityBindings struct {
	SubscriptionDriver SubscriptionDriverID
	ModelDiscovery     UtilityID   // 非空 = 走订阅式发现实现
	QuotaObservation   UtilityID
	ResetCreditAction  ActionID
}
```

分叉在 `internal/control/discover_group.go:35-42`：

```go
if bindings.ModelDiscovery != "" {
	return s.discoverSubscriptionGroupModels(ctx, rows)   // 订阅式
}
// 否则走 executeModelDiscovery（API-Key 通用路径）
```

订阅式绑定目前声明在 `internal/channel/modules/{claude,antigravity,codex,grok}.go`（如 `claude.go:54`）；其余渠道靠 `OperationListModels` 路由获得通用发现能力（`internal/channel/modules/*.go` 里大量 `spec.NewRoute(..., execution.OperationListModels, ...)`）。

**运行期的真实支持判定**在 `internal/control/discover_executor.go:62-65`：

```go
clientProtocol, routeMode, supported := target.resolvedTarget.PreferredRoute(execution.OperationListModels, "")
if !supported {
	return ModelDiscoveryResult{}, app_errors.ErrValidation
}
```

### 3.4 `executeModelDiscovery`

`internal/control/discover_executor.go:48-142`，入参是内部结构 `discoveryTarget`（`discover_executor.go:39-46`）：

```go
type discoveryTarget struct {
	channelID channel.ID
	resolvedTarget channel.ResolvedTarget
	credentials []discoveryCredential
	headerRules state.HeaderRules
	timeouts state.TimeoutConfig
	catalogProviderID string
}
```

超时/取消约定：

- 第 71 行 `discoveryCtx, cancel := context.WithTimeout(ctx, s.modelDiscoveryTimeout)`；默认值是 `defaultModelDiscoveryTimeout = 30 * time.Second`（`internal/control/service.go:35`，装配在 `service.go:186`）。
- 父 ctx 取消 → 返回 `ctx.Err()`，优先于其它错误（`discover_executor.go:106-108`、`138-140`）。
- discovery 自身 deadline → 映射成 `ErrBadGateway`（`discover_executor.go:109-111`）。
- 全部凭据都失败 → `fmt.Errorf("discover upstream models: %w", app_errors.ErrBadGateway)`（`discover_executor.go:141`）。
- 分页上限：`maxModelDiscoveryPages = 20`、`maxModelDiscoveryModels = 20_000`、`modelDiscoveryPageSize = 1000`（`discover_executor.go:26-30`）。
- 结果会被 `mergeDiscoveredModels`（`discover_executor.go:334-403`）合并 `catalog` 里该 provider 的模型，并按 `normalizeDiscoveredModels`（`discover_executor.go:405-420`）trim + 去重。

**注意**：`ModelCandidate.Sources` 会含 `"live"` / `"catalog"`，如果自动同步也用这个结果做「上游有哪些模型」，catalog-only（`Sources == ["catalog"]`）的条目并非上游真实返回，需按设计决定是否纳入。

---

## 4. 周期性后台任务基座（照抄对象）

### 4.1 宏观结构

- 协调器类型：`CatalogSyncCoordinator`（`internal/control/catalog_sync.go:145-166`）。
- 构造：`newCatalogSyncCoordinator(service, client, cachePath, metadata, hasLKG)`（`catalog_sync.go:170-196`）与导出的 `NewCatalogSyncCoordinator(service, client, bootstrap)`（`catalog_sync.go:211-226`）；构造时把自己回填到 `service.catalogSync`（`catalog_sync.go:191-194`）。
- 装配：`internal/container/container.go:195` 注册 `control.NewCatalogSyncCoordinator`（依赖注入，由 `control.NewService` → `control.NewCatalogSyncCoordinator` → `control.NewRuntime` 的顺序自动满足）。

### 4.2 启停 lifecycle

```go
// internal/control/runtime.go:32-34
type catalogSyncRuntime interface { Run(context.Context) }
```

- `NewRuntime(..., catalogSync *CatalogSyncCoordinator)`（`runtime.go:78-119`）把它存进 `Runtime.catalogSync`。
- `Runtime.Run(ctx)`（`runtime.go:121-164`）在 `runtime.go:149-155` 起一个 goroutine：

```go
if runtime.catalogSync != nil {
	wait.Add(1)
	go func() { defer wait.Done(); runtime.catalogSync.Run(ctx) }()
}
wait.Wait()
```

- `container.go:126-127` 把 `control.NewRuntime` 绑成 `app.ControlRuntime` 接口。
- `internal/app/app.go:224-238` 建 `runtimeContext` 并 `go a.controlRuntime.Run(runtimeContext)`。
- `internal/app/app.go:264-325`：`Stop` 先 `cancelRuntime()`，再等 `runtimeDone`，超时记 `shutdown.control_runtime_stop` forced 并返回错误。

**结论**：新调度器只要挂进 `Runtime.Run` 并尊重 ctx 取消，就自动获得「服务停止时及时退出、不阻塞关停」的语义。

### 4.3 ticker 模式与测试注入

`catalog_sync.go:27-31`

```go
const (
	modelsDevSyncInterval  = 24 * time.Hour
	modelsDevInitialRetry  = time.Hour
	modelsDevGroupDebounce = 500 * time.Millisecond
)
```

`Run` 的关键骨架（`catalog_sync.go:228-299`）：

```go
func (coordinator *CatalogSyncCoordinator) Run(ctx context.Context) {
	coordinator.processCtx = ctx
	defer coordinator.shutdown()
	periodic := coordinator.newTicker(modelsDevSyncInterval)   // :238
	defer periodic.Stop()
	launch(CatalogSyncStartup)                                  // 启动立即跑一次
	for {
		select {
		case <-ctx.Done(): return
		case <-periodic.C(): launch(CatalogSyncPeriodic)
		case <-coordinator.groupWake: /* 重置 debounce */}
	}
}
```

可注入点（全部是 `CatalogSyncCoordinator` 结构体字段，`catalog_sync.go:160-165`）：

```go
newTicker func(time.Duration) runtimeTicker
newTimer  func(time.Duration) catalogSyncTimer
now       func() time.Time
storeCache    func(string, catalog.SyncResult) error
applySnapshot func(context.Context, *catalog.Snapshot) error
```

- `runtimeTicker` 接口在 `internal/control/runtime.go:46-49`，标准实现在 `runtime.go:51-61`：
  ```go
  type runtimeTicker interface { C() <-chan time.Time; Stop() }
  ```
- `catalogSyncTimer` 与之同形，定义在 `catalog_sync.go:129-139`。
- 测试里直接改 `coordinator.newTicker = ...` 注入伪 ticker，见 `internal/control/catalog_sync_test.go:669-674`、`737`、`926`。

### 4.4 单飞（single-flight）与关停

- `Sync` 用 `inFlight *catalogSyncCall` 保证同一时刻只有一次执行，多余的调用者 `waiters++` 并等待同一个 `done`（`catalog_sync.go:368-421`）。
- `shutdown()` 置 `shuttingDown`、`cancel()` 在飞调用并等它 `done`（`catalog_sync.go:301-315`）。
- 关闭后新调用返回 `errCatalogSyncCoordinatorStopped`（`catalog_sync.go:168`、`376-380`）。
- 状态读取：`readStatus() CatalogSyncStatus`（`catalog_sync.go:105-119`），已通过 `/models` 响应暴露给前端：`ProjectModelCatalogStatusDTO{CheckedAtMS, SuccessfulFetchAtMS, ErrorCode}`（`internal/control/project_model_collection.go:355-361`）——**这是「同步状态可被前端读取」的既有先例**。

### 4.5 触发入口的既有写法

| 触发点 | 位置 |
|---|---|
| 分组创建 | `internal/control/group_create.go:158-160` |
| 分组模型变更 | `internal/control/group_models.go:151-153`（仅当 model ID 集合变化） |
| 幂等创建重放 | `internal/control/group_idempotency.go:206-208` |
| 分组删除 | `internal/control/group_delete.go:110-112` |
| 系统设置里打开自动同步 | `internal/control/settings.go:171-183`（`requestCatalogSyncOnEnable` → `RequestImmediateSync`） |

`RequestGroupSync` / `RequestImmediateSync`（`catalog_sync.go:317-337`）都是非阻塞 `select { case ch <- struct{}{}: default: }`，且先判 `service.modelsDevAutoSyncEnabled()`。

---

## 5. 分组设置持久化

### 5.1 DTO

`internal/control/group_settings.go:24-50`

```go
type GroupSettingsResponse struct {
	PriceMultiplier string; ChannelID channel.ID; ConnectionType models.ConnectionType
	Params json.RawMessage; Name string
	ValidationProtocol *protocol.Protocol; ValidationProtocols []protocol.Protocol
	ValidationModel *string; Enabled bool; WeightManual *int
	Overrides config.Settings; Effective GroupEffectiveConfigResponse
	Proxy outboundproxy.View
}

type GroupSettingsUpdateRequest struct {
	PriceMultiplier optionalField[string]; Name optionalField[string]
	Params optionalField[json.RawMessage]; ValidationProtocol optionalField[protocol.Protocol]
	ValidationModel optionalField[string]; Enabled optionalField[bool]
	WeightManual optionalField[int]; Overrides optionalField[config.Settings]
	Proxy optionalField[outboundproxy.Config]
}
```

`optionalField[T]` 定义在 `internal/control/group_write.go:91-123`（区分未设置/`null`/值）。

### 5.2 设置存到哪个列

- 分组级「零散配置」落在 `groups.overrides`（JSON 列，`internal/storage/models/group.go:24`），内容类型是 `config.Settings = map[string]any`（`internal/platform/config/config.go:127`）。
- 写入链路：`GroupSettingsUpdateRequest.Overrides` → `normalizeGroupSettings`（`internal/control/group_create.go:308-343`）→ `normalized.encodedOverrides` → `updates["overrides"]`（`internal/control/group_settings.go:325-328`）。
- 读取链路：`groupSettingsResponse` 把 `group.Overrides` 解码回 `config.Settings`（`group_settings.go:111-120`），并**显式删除** `state.SettingRetryCount`（`group_settings.go:120`，历史兼容）。

### 5.3 归一化与「分组级布尔开关」既有先例

`normalizeGroupSettings`（`group_create.go:308-343`）：

- 拒绝含 `state.SettingRetryCount`（`group_create.go:309-311`）；
- 对 `state.SettingParameterOverrides` 做 `parameteroverride.Compile` + `ValidateResponsesContinuation` 校验（`group_create.go:315-320`）；
- 其余键值做 JSON 往返 + `canonicalizeGroupSettingNumbers` 数字归一（`group_create.go:321-338`）；
- **它本身不校验键名白名单**。键名合法性在 `state` 层的 `ResolveGroupSettings` 里把关。

布尔开关的既有先例：`state.SettingAffinityEnabled`（`internal/state/runtime_settings.go:27`）。

- 白名单注册：`ResolveGroupRuntimeSettings`（`runtime_settings.go:238-311`），`SettingAffinityEnabled` 分支在 `runtime_settings.go:288-293`，用 `strictBoolean`（`runtime_settings.go:372-378`）。
- **未知键直接报错**：`runtime_settings.go:306-308` `default: return ResolvedGroupSettings{}, fmt.Errorf("unknown group setting %q", key)`。因为 `state.Compile` 会把每个分组都解析一遍，未知键会让整个快照编译失败 → 写入被拒。
- 系统级开关先例是 `state.SettingModelsDevAutoSyncEnabled`（`runtime_settings.go:33`），在 `IsRuntimeSettingKey`（`runtime_settings.go:102-124`）、`ResolveRuntimeSettings`（`runtime_settings.go:225-230`）、`ValidateRuntimeSetting`（`runtime_settings.go:353-355`）三处登记。

**两条可选持久化路线**：

| 路线 | 落点 | 需要改的地方 |
|---|---|---|
| A. 复用 `overrides` | `groups.overrides` JSON，键如 `group_model_auto_sync_enabled` | 新增 key 常量 + `ResolveGroupRuntimeSettings` 分支（否则编译报 unknown group setting）；**无需迁移** |
| B. 新增独立列 | `groups.<new_col>` + `models.Group` 字段 | 需要 `storage/migrations/NNNN_zz_*.go` 迁移 + `migration.go` 注册 + `GroupSettingsResponse/UpdateRequest` 字段 |

注意 `GroupEffectiveConfigResponse`（`internal/control/group_detail.go:17-25`）目前只投影 7 个字段，走路线 A 时若想让它出现在 `effective` 里需要同步加字段。

---

## 6. 数据库迁移

### 6.1 目录组织与命名

`internal/storage/migrations/` 下每号一个文件。命名规范写在 `internal/storage/migrations/doc.go:1-35`：

- 上游迁移：`<NNNN>_<snake_name>`，**必须原样保留，合并上游不得重编号**；
- **fork 自有迁移：`<NNNN>_zz_<snake_name>`，NNNN 取创建时上游的最新号，Go 标识符带 `ZZ` 后缀**（如 `ID0014ZZ / Up0014ZZ / Validate0014ZZ / ValidateRecoverable0014ZZ`）。
- 必须注册在锚点上游迁移的**紧后面**，因为 ledger 用 `ORDER BY id ASC` 与注册表逐位比较（`migration.go:207-222`），且 `_zz_` 在字典序上紧跟锚点（`doc.go:15-26`）。
- 当前上游 tip 是 `0020_client_model_overrides`（`migration.go:118`），所以新的 fork 迁移应命名为 `0020_zz_<name>` 或 `0021_zz_<name>`——**按 doc.go 的规则应取「当前上游 tip 号」，即 `0020_zz_<name>`**，并注册在 `migration.go:118` 之后。

### 6.2 注册位置

`internal/storage/migration.go:39-119` 的 `var migrations = []migration{...}`，每项是：

```go
{ID: migrationfiles.ID0020, Up: migrationfiles.Up0020,
 Validate: migrationfiles.Validate0020, ValidateRecoverable: migrationfiles.ValidateRecoverable0020}
```

`validateMigrationRegistry`（`migration.go:170-184`）强制：ID 匹配 `^\d{4}_[a-z0-9]+(?:_[a-z0-9]+)*$`、**严格字典序递增**、`Up/Validate/ValidateRecoverable` 三者都非 nil。注意 `migrationIDPattern`（`migration.go:21`）允许 `_zz_` 段。

### 6.3 迁移文件三件套写法

以给 `groups` 加一列为例，最贴近的模板是 `internal/storage/migrations/0005_proxy_config.go`：

- `Up00NN`：`if db.Migrator().HasColumn(model, "col") { continue }` 再 `AddColumn`（`0005_proxy_config.go:25-41`），幂等。
- `Validate00NN`：列必须存在 + 类型/可空性断言（`0005_proxy_config.go:63-94`）。
- `ValidateRecoverable00NN`：表不存在时直接返回 nil，列存在时走同一校验（`0005_proxy_config.go:43-61`）。
- 文件内自定义表结构体 + `TableName()` 指定真实表名（`0005_proxy_config.go:12-22`）。
- 建表型模板见 `0020_client_model_overrides.go:13-50`，其中用自定义类型 + `GormDBDataType` 处理 mysql `longtext` / 其它 `text` 的方言差异（`0020:21-28`）。
- **没有 down 迁移**：`migration` 结构体（`migration.go:31-37`）里根本没有 Down 字段，全仓库也无 down 文件。

### 6.4 方言差异

`applyMigrationRegistry`（`migration.go:136-158`）：

- `sqlite` → `applySQLiteMigrationRegistry`，整链在一个 `BEGIN IMMEDIATE` 里、且临时 `PRAGMA foreign_keys = OFF` 再统一校验外键（`internal/storage/migration_sqlite.go:16-72`）；
- `mysql` → `applyMySQLMigration`（DDL 隐式提交，不走事务，见 `migration.go:241-271` 注释）；
- `postgres/postgresql` → 事务化 DDL。

因此迁移实现**只需处理列/表本身**，事务与外键策略由框架统一处理。

---

## 7. 分组模型变更后必须触发的副作用

保存分组模型的唯一安全入口是 `Service.writeGroupConfig` / `writeGroupConfigLocked`（`internal/control/service.go:316-404`）。它会自动完成：

| 步骤 | 位置 |
|---|---|
| 取 `writeMu` 写锁 + `enforceOperationRecoveryBarrierLocked` | `service.go:321-325` |
| 按凭证分片串行（`doCredentialMutations`） | `service.go:336`、`service.go:117-` |
| 事务内跑 `mutate(tx)` | `service.go:352-355` |
| `reconcileReferencedPrices` （重算被引用模型的自动定价） | `service.go:356-358` |
| `cleanupUnreferencedAutomaticPrices`（清理不再被引用的自动价） | `service.go:359-361` |
| `stateloader.BuildCompileInputWithProxy` 重建编译输入 | `service.go:362-364` |
| `state.Compile(input)` 预编译校验 | `service.go:368-373`（`automodel.ErrInvalidConfig` → `ErrValidation`） |
| `loadPriceTable` | `service.go:374-377` |
| 提交后 `s.priceRuntime.Publish(...)` | `service.go:385` |
| 可选 `afterCommitBeforePublish()`（凭证 registry 改写） | `service.go:386-394` |
| `s.publishSnapshot(...)` = `state.Manager.Publish` | `service.go:395`；绑定见 `service.go:281` |

`state.Manager.Publish`（`internal/state/manager.go:84-97`）会 `Compile` → 调 `reconciler.ReconcileConfigSnapshot` → `publishCompiledLocked`（`manager.go:126-140`，递增 `Revision`、`close(updates)` 唤醒订阅者）。

**关于「有没有别的缓存要手动失效」**：

- 网关每请求都读 `handler.manager.Current()`（如 `internal/gateway/downstream_headers.go:28`、`internal/gateway/http_routes.go:100`），**不缓存快照副本**，所以发布新 Revision 就是唯一失效动作。
- `priceRuntime.Publish` 由 `writeGroupConfigLocked` 自动做。
- `catalogRuntime.Publish` 只跟 models.dev 目录有关，与分组模型无关。
- `modelname` 包是**无状态纯函数**（`internal/modelname/modelname.go`），没有缓存。
- 额外需要手动做的只有 **catalog 分组同步触发**：`s.catalogSync.RequestGroupSync()`，只在 model ID 集合真的变了时调用（`internal/control/group_models.go:151-153`；判据是 `sameGroupModelIDs`，`group_models.go:165-182`）。同样的条件式触发在创建（`group_create.go:158-160`）、幂等重放（`group_idempotency.go:206-208`）、删除（`group_delete.go:110-112`）。
- `UpdateGroupSettings` 里在 override/params 变化时还会额外做 `invalidateGroupObservationFlights` + `stats.Reset` + `reconcileRegistryGroup`（`internal/control/group_settings.go:362-374`），那是凭证目标变化才需要的，模型变更用不到。

---

## 8. 相关测试基建

### 8.1 最像的两个既有测试文件

**A. 调度器：`internal/control/catalog_sync_test.go`**

- 服务 fixture：`fixture := newServiceFixture(t)`（`catalog_sync_test.go:658` 等）。
- 假 ticker：`newFakeRuntimeTicker()`（`internal/control/runtime_test.go:15-34`），有 `ticks chan time.Time` + `stopped chan struct{}`。
- 假 timer：`fakeCatalogTimer` / `newFakeCatalogTimer`（`catalog_sync_test.go:1522-1533`）。
- 注入方式：直接赋 `coordinator.newTicker = ...` / `coordinator.newTimer = ...`（`catalog_sync_test.go:669-680`），并断言传入的 interval 字面量（`if interval != 24*time.Hour { t.Fatalf(...) }`）。
- 驱动推进：`periodic.ticks <- time.Now()`（`catalog_sync_test.go:708`）。
- 辅助：`awaitCatalogCall` / `awaitCatalogTimer` / `waitForCatalogShutdown` / `waitForCatalogIdle`（`catalog_sync_test.go:1535-1569`），全部带 1s watchdog。
- 启停断言：起 goroutine 跑 `coordinator.Run(ctx)`，`cancel()` 后要求 `done` 在 1s 内关闭（`catalog_sync_test.go:682-716`）。
- 客户端替身：`catalogSyncClientFunc`（`catalog_sync_test.go:25-32`）把函数适配成接口。

**B. 分组模型保存：`internal/control/group_models_test.go`**

- `TestUpdateGroupModelsReplacesAuthoritativeListAndPublishesOnce`（`group_models_test.go:279-347`）：建分组 → 断言响应结构体 `reflect.DeepEqual` → 断言 `manager.Current().Revision`、`registry.Snapshot()`、凭据行都没被动过。
- `TestUpdateGroupModelsRequiresNonNullModelsField`（`:259-277`）。
- `TestUpdateGroupModelsRejectsCorruptStoredRow`（`:793-822`）：直接往 DB 写坏 JSON 验 `ErrInternalServer`。
- 目录同步触发断言：`TestGroupCatalogSyncTriggerOnlyTracksProviderAndModelIDChanges`（`internal/control/group_update_test.go:70-155`）+ `assertCatalogGroupWake` / `assertNoCatalogGroupWake`（`group_update_test.go:157-173`）——直接非阻塞读 `coordinator.groupWake` 通道。

### 8.2 fixture、内存 DB、时钟

- `newServiceFixture(t)`（`internal/control/test_helpers_test.go:164-167`）→ `newServiceFixtureWithDatabase(t, sqlitetest.OpenMigrated(t))`。
- `newServiceFixtureWithDatabase`（`test_helpers_test.go:190-240`）组装 `state.Manager` / `CredentialRegistry` / `channel.NewRegistry()` / `encryptiontest.Service` / `health.StatsStore` / `catalog.Runtime`，再 `control.NewService(...)`。
- `sqlitetest.OpenMigrated`（`internal/testutil/sqlitetest/sqlitetest.go:25-63`）：从一次 `storage.AutoMigrate` 的 DDL 模板恢复到隔离的内存 SQLite，`MaxOpenConns(1)`，`_txlock=immediate` + `foreign_keys(1)` + `busy_timeout(5000)`。**新增迁移会自动被模板包含**。
- 文件型 DB fixture：`newFileServiceFixture`（`test_helpers_test.go:179-183`）。
- 时钟注入：`Service.now func() time.Time`（`internal/control/service.go:78`）；调度器级时钟是 `CatalogSyncCoordinator.now`（`catalog_sync.go:163`）；另有 `fakeRuntimeClock`（`internal/control/runtime_test.go:59-`）。
- 发现路径的替身：`newRecordingDiscoveryExecutor(...)`（`internal/control/discover_executor_test.go:43`）替换 `fixture.service.executor`，用 `listFn func(ctx, method, path string, rules state.HeaderRules) ([]string, error)` 直接返回模型名列表（用法见 `internal/control/discover_group_test.go:720-725`、`754-760`）。
- 超时可控：`fixture.service.modelDiscoveryTimeout = 3 * time.Second`（`discover_group_test.go:754`；`server_test.go:1951/1985` 用 20ms）。

---

## 9. 对本次设计的直接结论

1. **模型列表是全量替换语义，必须自己实现「读旧 + 合并 + 整体写」**：`UpdateGroupModels`（`internal/control/group_models.go:96-163`）会丢掉请求里没有的项，不能直接拿发现结果调它做「只增不删」。合并后应复用 `normalizeGroupModels`（`internal/control/group_write.go:226-276`）做规范化与冲突检测，再在 `s.writeGroupConfig` 里写 `groups.models`——这条路径自动带齐定价重算、快照发布、编译校验（`internal/control/service.go:316-404`）。

2. **必须复用的既有函数清单**：`Service.DiscoverGroupModels`（`internal/control/discover_group.go:23-48`，唯一已落库分组的发现入口）、`normalizeModelAliases`（`group_write.go:195-216`，别自己写 trim/去重）、`state.RoutableModelNames`（`internal/state/snapshot.go:115-135`，按行判断「加这个别名会不会冲突」的官方口径）、`s.catalogSync.RequestGroupSync()`（`group_models.go:151-153`，模型 ID 集合变化后触发定价重算）。

3. **「新增模型加 `claude-<id>`」的判定必须用 Routable 名称集逐行试算**：PRD AC8 要求「单行冲突则跳过该行别名、其余照写」。做法是对每行先算 `normalizeModelAliases(append(aliases, "claude-"+id), id)`，再用 `state.RoutableModelNames` 与已认领集合比对，冲突就退回原别名集合。注意 `claude-<id>` 若等于该行 ID 会被 `normalizeModelAliases` 静默丢弃（`group_write.go:200-202`），`id` 已以 `claude-` 开头时跳过属于同类语义。大小写不敏感比较需要自己实现（现有 `normalizeModelAliases` 是精确比较）。

4. **别名上限是硬错误，不是丢弃**：`maxAliasesPerModel = 10`、`maxModelNameBytes = 255`（`group_write.go:27-30`）。给一条已有 10 个别名的模型追加 `claude-<id>` 会让整次保存 `ErrValidation`，必须按「跳过该项别名」处理，否则自动同步会整体失败。

5. **持久化开关优先走 `groups.overrides`（路线 A），无需迁移**；但**必须在 `ResolveGroupRuntimeSettings` 注册新键**（`internal/state/runtime_settings.go:238-311`，未注册键会让 `state.Compile` 报 `unknown group setting` 从而拒绝保存）。若选独立列（路线 B），迁移必须命名为 `<当前上游tip号>_zz_<name>`（当前 tip 为 0020）+ Go 标识符带 `ZZ` 后缀，并注册在 `internal/storage/migration.go:118` 之后，且三件套 `Up/Validate/ValidateRecoverable` 一个都不能缺（`migration.go:179-181`）。无论哪条路线，`GroupSettingsResponse/GroupSettingsUpdateRequest`（`internal/control/group_settings.go:24-50`）都要加字段。

6. **调度器照抄 `CatalogSyncCoordinator` 的形状即可**：`Run(ctx)` + `newTicker func(time.Duration) runtimeTicker` 字段（`internal/control/catalog_sync.go:161`、`228-299`），由 `control.NewRuntime` 持有并在 `Runtime.Run` 里起 goroutine（`internal/control/runtime.go:78-119`、`149-155`），`App.Stop` 通过 `cancelRuntime` + 等 `runtimeDone` 保证及时退出（`internal/app/app.go:224-238`、`285-325`）。测试按 `catalog_sync_test.go:669-716` 注入伪 ticker。

7. **串行执行 + 单组失败隔离**：PRD 要求多分组串行且互不影响。`CatalogSyncCoordinator.Sync` 的单飞结构（`catalog_sync.go:368-421`）是单任务版；分组循环只需在单次 `Run` 迭代里 `for _, group := range groups { if ctx.Err() != nil { return }; ... }` 且每组独立 `continue` 错误，不要把一个错误当成整轮退出。注意 discovery 内部已有 30s 超时（`internal/control/service.go:35`），但 `DiscoverGroupModels` **不拿 `writeMu`**（`internal/control/discover_group_test.go:742-802`），所以必须在拉取完成之后再进 `writeGroupConfig`，不能整轮持锁。

8. **必须锁死「无变化则不写入」**：`writeGroupConfig` 每次都会发布新快照（`Revision++`，`internal/state/manager.go:126-140`）并触发定价重算。合并结果与现有列表逐字段相等时应直接跳过（可复用 `sameGroupModelIDs`，`internal/control/group_models.go:165-182` 只比 ID；别名也要比才算「无变化」）。

9. **前端契约测试会拦住新字段**：`TestSharedAPIResponseFieldsMatchClassicContracts`（`internal/control/classic_api_contract_test.go:17-140`）会遍历 `GroupSettingsResponse` / `GroupModelsResponse` / `GroupModelResponse` 的全部导出 JSON 字段，要求它们都出现在 `web/src/frontends/classic/app/resources/groups.ts` 的白名单里（`groupSettingsFields` 在 `groups.ts:62-76`，`groupModelsFields` 在 `:77`，`groupModelItemFields` 在 `:78`）。PRD 说「classic 不动」，但**服务端加字段就必须同步往这个白名单里加名字**，否则该测试失败。

10. **若新增 GET 路由要改路由契约测试**：`internal/control/server_test.go:101-113` 硬断言 `/groups` 前缀下全部 GET 路由的确切列表；新增互斥变更路由还必须带 `s.auditMutation(...)` 描述符（`internal/control/http_routes.go:230-235`、`253-257`），并在 `internal/control/mutation_audit_routes_test.go:1096-1129` 的表格里补条目。把开关放进既有的 `PUT /groups/:group_id/settings` 可以完全避开这条——这也符合 PRD「切换即保存、不走模型草稿」的要求。

**已知口径冲突（需要主 agent 裁决）**：任务描述里提到「有一个失败/成功状态可被前端读取」，但 `prd.md` 的 Out of Scope 明确写了「上次自动同步时间 / 上次同步结果的前端展示」不做。既有先例（`catalogSync.readStatus()` → `ProjectModelCatalogStatusDTO`，`internal/control/project_model_collection.go:355-361`）可直接照抄，但按 PRD 当前范围，这一项应当留到后续迭代。

**未找到**：仓库中没有任何现成的「分组级持久化布尔开关 + 周期任务」组合先例——最接近的是系统级 `SettingModelsDevAutoSyncEnabled`（`internal/state/runtime_settings.go:33`）+ `CatalogSyncCoordinator`，以及分组级的 `SettingAffinityEnabled`（`runtime_settings.go:27`）。两者需要本次组合。
