# 名称集合调用点全图（模型别名 1M 后缀兼容）

本文件是实施与检查阶段的**逐项核对清单**。改动点集中，但「名称集合」在本仓库有 9 个消费点，其中只有一部分需要纳入派生名。遗漏与误改的代价不对称：遗漏会让某个入口仍然 503 或白名单误拒；误改会破坏前端 `client_models` 断言或给用户展示假别名。

## 1. 名称集合的两个层次

| 函数 | 返回 | 语义 |
| --- | --- | --- |
| `state.ExternalModelNames` | `[id, ...aliases]` | **对客户端可见**的名称。形状被 `web/src/app/resources/groups.ts:443-454` 硬断言锁死 |
| `state.RoutableModelNames`（本次新增） | 上者逐项 + 其后紧跟该项后缀基名 | **可路由**的名称。供索引、写路径与编译期冲突检测 |

两者在无后缀配置下返回同一集合，逐项相等。这是「无后缀模型完全无感」的判据。

## 2. `ExternalModelNames` 全部消费点

| 位置 | 用途 | 处置 |
| --- | --- | --- |
| `internal/state/snapshot.go:379` | `appendExecutionTargets` 建 `registrations`，喂 `ExecutionCandidates` 与 `ExecutionRouteCatalog` | **改** `RoutableModelNames`（核心） |
| `internal/state/snapshot.go:527` | `validateCompileInput` 编译期重名检测 | **改** `RoutableModelNames` |
| `internal/control/group_write.go:238` | `normalizeGroupModels` 保存期重名检测（写法是 `append([]string{id}, aliases...)`，不是函数调用） | **改** `RoutableModelNames` |
| `internal/control/project_model_collection.go:257` | 模型页列表逐名称建记录 | **改** `RoutableModelNames` |
| `internal/control/project_model_collection.go:493` | 上游模型详情的客户端模型关联 | **改** `RoutableModelNames` |
| `internal/control/group_collection.go:449` | `validateGroupCollectionModels` 读路径防御校验（调用方 `group_collection.go:349`、`group_options.go:109`） | **不改**（见第 4 节） |
| `internal/control/group_models.go:83` | `GroupModelsResponse.ClientModels` | **不改**（R7） |
| `internal/control/group_options.go:124` | 分组模型候选集（前端选择器数据源） | **不改**（R7） |
| `internal/control/home.go:285` | `modelMatchesNameFilter` 的并集判断 | **不改**（见第 5 节） |

## 3. `AccessKey.Filters.Models` 全部使用点

| 位置 | 用途 | 处置 |
| --- | --- | --- |
| `internal/scheduler/inspect.go:113-120` | 调度侧白名单判定，精确等值 | **改** `modelname.Allows` |
| `internal/gateway/models.go:99-103` | `/v1/models` 列表侧白名单过滤，精确等值 | **改** `modelname.Allows` |
| `internal/control/home.go:328,331,361,362` | 经 `modelMatchesNameFilter` 的并集判断 | **不改** |
| `internal/control/project_model_collection.go:413,416` | 同上 | **不改** |
| `internal/control/access_key_update_idempotency.go:61` | 仅做集合拷贝，不做成员判定 | **不改** |

两处「白名单为空即不限制」的外层判断（`len(...) > 0`）必须保留：`modelname.Allows` 刻意不对空集合特判。

## 4. 读路径为何保持 `ExternalModelNames`

`validateGroupCollectionModels`（`internal/control/group_collection.go:439-462`）与写路径、编译期共享同一段「跨 ID 认领同名即冲突」注释，但性质不同——它是**从库里读出已保存的 models JSON 后**做的防御校验，失败会经 `groupCollectionDataError` 包装成 `ErrInternalServer`（500），打挂分组列表页与分组选项接口。

升级前的库中可能已存在「模型 A 别名 `xxxx[1M]`」与「模型 B id `xxxx`」并存的配置。若读路径也纳入派生名，用户将看到 500 页面，既查不到冲突内容也无法在界面上修复。保持原集合让管理后台正常渲染真实配置，用户在页面上删除冲突别名即可修复。

代价是存在一个窄窗口：读路径放过、编译期拒绝的存量数据会让「保存成功但新配置不生效」。该状态可观测且可恢复——`Publish` 编译失败时不发布新快照（`internal/state/manager.go:84-97`），旧配置继续服务。

## 5. `modelMatchesNameFilter` 为何无需改动

`internal/control/home.go:284-291` 是**并集**判断：模型任一对客户端可见的名称命中白名单即通过。模型名集合含 `claude-deepseek-flash[1M]`，白名单里存的也是它，现有语义已成立。它不涉及「请求名是否被放行」，因此与后缀归一无关。

## 6. 索引链路（改一处、两处生效）

```
state.Compile
  ├─ appendExecutionTargets → snapshot.ExecutionCandidates   (:291)
  └─ appendExecutionTargets → snapshot.ExecutionRouteCatalog (:246)
                                  │
        ┌─────────────────────────┼─────────────────────────┐
        ▼                         ▼                         ▼
  /v1/models 列表           请求路由查找                巡检路径
  gateway/models.go         scheduler/inspect.go       （同一函数）
  遍历索引 key              byModel[modelKey] 精确匹配
```

`/v1/models` 直接遍历索引 key，因此派生名进入索引后**列表自动同时返回两个名称**，无需改列表代码（R3）。这是「改索引注册」相对「改查找回退」的决定性优势。

`byModel` 是精确匹配的 map 查找，无回退：索引里没有的名字必然落到 `ReasonNoRouteTarget`，最终由 `internal/gateway/handler.go:1456` 写为 `reasonNoCandidate`（503 `no_available_candidate`）。

## 7. 关键行号锚点（实施时核对）

| 锚点 | 内容 |
| --- | --- |
| `internal/state/snapshot.go:75-89` | `ExternalModelNames` 定义，注释声明「ID 恒在首位」是前端断言与索引去重的前提 |
| `internal/state/snapshot.go:369-377` | `claimed` map 与去重注释（「放大该目标的候选权重」） |
| `internal/state/snapshot.go:521-533` | `validateCompileInput` 的 `claimed`（name → 上游 ID） |
| `internal/control/group_write.go:190-215` | `normalizeModelAliases`：trim、丢空项与等于 ID 的项、按首现去重、施加上限 |
| `internal/control/group_write.go:221-271` | `normalizeGroupModels`：`ownersByName` / `nameOrder` / `indexesByName` → `ModelNameConflict` |
| `internal/scheduler/inspect.go:97-180` | `evaluateTargets`，路由查找唯一入口 |
| `internal/gateway/models.go:70-131` | `collectVisibleModelIDs` |
| `internal/parameteroverride/rules.go:326-337` | `rule.matches`，`entry.model == ""` 提前返回 true |
| `web/src/app/resources/groups.ts:437-461` | `projectGroupModelItem` 硬断言（本任务不可触碰的契约） |

## 8. 已确认的事实

1. **源码中 `[1M]` / `[1m]` 字面量当前零匹配**（`rg '\[1M\]|\[1m\]' --glob '*.go' --glob '*.ts' --glob '*.vue'` 无结果）。这是本任务新引入的概念，不存在既有部分实现或命名冲突。
2. **`state` 包 import `parameteroverride`**（`internal/state/snapshot.go:18`），但 `parameteroverride` 不 import `state`。后缀工具若放在 `state` 会被 `parameteroverride` 反向依赖，故必须独立成包。
3. **`control` 包已 import `state`**（`project_model_collection.go` 等大量使用），因此 `group_write.go` 若尚未 import，补一行即可，不构成新依赖方向。
4. **`group_models_test.go:361-368` 的 `len(targets) != 4`** 断言涉及 `public-a` / `provider-a` / `public-b` / `provider-b` 四个无后缀名，`RoutableModelNames` 结果与改动前相同，断言值不应变化——这是「无后缀无感」最有价值的回归哨兵。
5. **`claimed` 去重让派生名天然幂等**：`id=xxxx`、`alias=xxxx[1M]` 时，派生基名 `xxxx` 与 ID 同名，只会注册一次（R2）。

