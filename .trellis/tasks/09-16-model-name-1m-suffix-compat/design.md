# 模型别名 1M 上下文后缀兼容 — 技术设计

## 1. 设计目标与硬约束

目标：让「带 `[1M]`/`[1m]` 后缀的名称」与「其基名」在 gpt-load 内互为等价的可路由名称，同时不触碰任何前端已断言的响应形状。

三个硬约束决定了方案的形状：

| 约束 | 来源 | 后果 |
| --- | --- | --- |
| `client_models` 必须严格等于 `[id, ...aliases]` | `web/src/app/resources/groups.ts:443-454` 抛 `InvalidResponseError` | `ExternalModelNames` 的返回值形状不能变，派生名不能进入该函数 |
| 路由查找只有 `evaluateTargets` 一处入口 | `internal/scheduler/inspect.go:133-140` | 但列表展示要求派生名进入索引，因此选择「改索引注册」而非「改查找回退」 |
| `state` 依赖 `parameteroverride`，反向不可 | `internal/state/snapshot.go:18` | 后缀归一工具不能放在 `state` 包，需下沉到独立包 |

## 2. 核心概念

`[1M]` / `[1m]` 是**客户端侧的上下文窗口声明标记**，不是模型标识的一部分：

- 客户端从 `/v1/models` 读到 `claude-deepseek-flash[1M]`，据此启用 1M 上下文；
- 客户端发起请求时剥离后缀，实际发送 `claude-deepseek-flash`；
- gpt-load 需要同时接受两种写法，并让它们路由到同一上游模型。

因此后缀等价关系是**运行时语义**，不写入数据库（PRD R8）。

## 3. 新增 `internal/modelname` 包

放在依赖图最底层，零内部依赖，供 `state` / `parameteroverride` / `scheduler` / `gateway` 共用。

```go
// Package modelname owns canonicalization of client-facing model names.
package modelname

// ContextSuffixes 是客户端用于声明扩展上下文窗口的模型名后缀。
// 比较大小写不敏感：Claude Code 同时存在 [1M] 与 [1m] 两种写法。
var ContextSuffixes = [...]string{"[1M]", "[1m]"}

// Base 返回剥离上下文后缀后的模型名；不含后缀时原样返回。
func Base(name string) string

// Allows 报告 allowed 集合是否接受 name：精确命中，或按上下文后缀归一后命中。
func Allows(allowed map[string]struct{}, name string) bool
```

关键实现要点：

- `Base` 对长度不超过后缀本身的名称原样返回，因此 `[1M]` 自身不会被剥成空串，与 PRD R1「基名为空时不产生派生名」一致。
- `Allows` 双向覆盖：请求名带后缀时比对其基名，请求名为基名时枚举 `ContextSuffixes` 后的候选。两个方向都是 O(1) map 查询，不遍历白名单。
- `Allows` 不做「空集合即放行」的特判，空集合语义由调用方保留，避免函数职责发散。

## 4. 名称集合的分层

在 `state` 包内定义两个不同用途的名称集合，`ExternalModelNames` 保持不变：

| 函数 | 返回 | 用途 |
| --- | --- | --- |
| `ExternalModelNames(model)` | `[id, ...aliases]`（现状不变） | `client_models`、`group_options` 模型候选、前端选择器 |
| `RoutableModelNames(model)` | `ExternalModelNames` 逐项展开后缀基名，派生名紧跟来源项之后，跳过空基名并去重 | 路由索引注册、两处冲突检测、模型页 |

派生名紧跟来源项（`[id, idBase, alias1, alias1Base, ...]`）而非追加末尾：模型页按该顺序展示，成对出现更易读；`id` 仍在首位。

## 5. 改动点

### 5.1 索引注册（核心）

`internal/state/snapshot.go:377-386` 的 `appendExecutionTargets` 把 `ExternalModelNames` 换成 `RoutableModelNames`。

这一处同时喂给 `ExecutionCandidates`（数据面路由）与 `ExecutionRouteCatalog`（巡检），因此：

- 请求 `claude-deepseek-flash` 命中索引，503 消失（PRD 首要验收项）；
- `/v1/models` 遍历 `ExecutionCandidates` 的 key，天然同时返回两个名称（R3），无需改列表代码；
- 已有的 `claimed` 去重逻辑对派生名同样生效，显式名与派生名同名时只注册一次（R2）。

### 5.2 冲突检测（三处调用、两处改动）

同名检测在代码里共有三处，性质不同：

| 位置 | 性质 | 本次处置 |
| --- | --- | --- |
| `internal/control/group_write.go:238`（`normalizeGroupModels`） | 写路径，保存分组时校验 | **改**为 `RoutableModelNames` |
| `internal/state/snapshot.go:527`（`validateCompileInput`） | 编译期，发布快照前校验 | **改**为 `RoutableModelNames` |
| `internal/control/group_collection.go:439`（`validateGroupCollectionModels`，被 `group_collection.go:349` 与 `group_options.go:109` 调用） | 读路径，从库里读出 models JSON 后做防御性校验 | **不改**，保持 `ExternalModelNames` |

写路径与编译期必须同口径：两处都改成 `RoutableModelNames`，避免「保存通过、编译报错」。派生基名被他条目占用时会在保存阶段返回 `MODEL_NAME_CONFLICT`，冲突项里显示的是派生基名，用户可据此定位（R6）。

读路径刻意不同口径，理由是**存量数据必须仍可读、可修复**：

- 升级前的库中可能已存在「模型 A 的别名是 `xxxx[1M]`」与「模型 B 的 id 是 `xxxx`」这类组合。此时 A 的派生名 `xxxx` 与 B 的显式名冲突。
- 若读路径也改用 `RoutableModelNames`，`groupCollectionDataError` 会把冲突包装成 `ErrInternalServer`（500），**分组列表页与分组选项接口直接打不开**——用户既看不到冲突内容，也无法在界面上修复。
- 保持读路径原样，则管理后台正常渲染库中的真实配置，用户可自行删除冲突别名完成修复；`normalizeGroupModels` 会在下一次保存时拒绝仍然冲突的配置。

代价是存在一个窄窗口：读路径放过、编译期拒绝的存量数据会让「新配置不生效」。该状态是可观测且可恢复的——`Publish` 编译失败时**不发布新快照**（`internal/state/manager.go:84-97`），旧配置继续服务，不会把服务打挂。编译期错误信息需在实现时明确指向派生名冲突（而非仅报 `duplicate external model "xxxx"`），否则用户在配置里找不到重复写过的名字会因为困惑而无法定位。

### 5.3 白名单匹配（两处）

- 调度侧 `internal/scheduler/inspect.go:113-120`：`Filters.Models[*query.externalModel]` 换成 `modelname.Allows`。
- 列表侧 `internal/gateway/models.go:99-103`：同换成 `modelname.Allows`。

两处都不改「白名单为空即不限制」的既有判断，只替换集合成员判断。改完后白名单只勾 `claude-deepseek-flash[1M]` 时，`claude-deepseek-flash` 既被放行也出现在列表里（R4）。

### 5.4 参数覆盖匹配

`internal/parameteroverride/rules.go:326-337` 的 `matches`：两侧都过 `modelname.Base` 后再比较，前缀模式与前缀模式不变。

```go
model := modelname.Base(entry.model)
clientModel = modelname.Base(clientModel)
if entry.modelPrefix {
    return strings.HasPrefix(clientModel, model)
}
return clientModel == model
```

无后缀时 `Base` 是恒等函数，既有行为逐字不变；有后缀时规则 `xxxx[1M]` 对请求 `xxxx` 生效（R5）。

### 5.5 模型页

`internal/control/project_model_collection.go:257` 与 `:493` 的 `ExternalModelNames` 换成 `RoutableModelNames`，保持该文件第 255-256 行声明的「与 `/v1/models` 可见集合一致」不变量（R3）。

派生名会让 `aliasApplied`（`:277`、`:501`，判定为 `clientModel != model.ID`）为 `true`——这是准确的，派生名确实来自别名。模型页的「模型数」统计（`projectModelSummary`）会随派生名增加一条，与 `/v1/models` 的条目数保持一致。

### 5.6 明确不动的部分

- `ExternalModelNames`（`internal/state/snapshot.go:75`）
- `GroupModelsResponse.ClientModels`（`internal/control/group_models.go:83`）
- 分组模型候选 `internal/control/group_options.go:124` 与前端缓存 `web/src/app/resources/groups.ts:1000-1006`：这是「可选择哪些模型」的候选集，不是「哪些名称可路由」，纳入派生名只会给用户一份可被下次保存丢弃的假别名（R7）。
- `validateGroupCollectionModels`（`internal/control/group_collection.go:439`）：读路径防御性校验，理由见 5.2。
- `internal/control/home.go:284` 的 `modelMatchesNameFilter`：它是「模型任一名称命中白名单即通过」的并集判断，模型名称集合含 `claude-deepseek-flash[1M]`，白名单里也是它，现有语义已成立，无需改动。同文件的 `:328-362` 与 `internal/control/project_model_collection.go:413-416` 走的都是这个函数，一并无需改动。

## 6. 数据流

```
配置: aliases = ["claude-deepseek-flash[1M]"]
        │
        ├─ ExternalModelNames ──────────► client_models / group_options / 前端选择器
        │                                  ["deepseek-flash", "claude-deepseek-flash[1M]"]   (形状不变)
        │
        └─ RoutableModelNames ──────────► 路由索引 ExecutionCandidates / ExecutionRouteCatalog
                                           {deepseek-flash, claude-deepseek-flash[1M], claude-deepseek-flash}
                                                     │
                        ┌────────────────────────────┴────────────────────────────┐
                        ▼                                                         ▼
              /v1/models 列表（遍历索引 key）                        请求 claude-deepseek-flash
              deepseek-flash                                        → byModel 命中
              claude-deepseek-flash[1M]                             → 白名单 modelname.Allows 放行
              claude-deepseek-flash                                 → 参数覆盖 Base 归一后匹配
                                                                    → 上游 deepseek-flash
```

## 7. 兼容性与不变量

- **前端零改动**：`client_models` 形状不变，`groups.ts` 的两个断言（`:445` 别名不重复且不等于 ID、`:449-453` 形状）继续成立。
- **存量数据零迁移**：派生名在索引编译期计算，已保存的带后缀别名升级后立即获得基名路由（R9）。
- **存量数据可能因派生名冲突而编译失败**：升级前若已存在「别名 `xxxx[1M]`」与「另一条目的 id `xxxx`」并存的配置，升级后派生名会与之撞名，`validateCompileInput` 拒绝发布新快照，运行中的旧配置继续服务。这是**已知且可恢复**的状态（读路径不受影响，用户可在管理后台删除冲突别名），但其存在说明：本方案的语义变化不只影响带后缀的那一条配置，也可能波及同一部署内的其他条目。实现时编译期错误信息必须能让用户区分「显式重名」与「派生重名」。
- **无后缀模型完全无感**：`Base` 对无后缀名是恒等函数，`RoutableModelNames` 与 `ExternalModelNames` 返回相同集合，所有消费点行为与改动前逐字一致。
- **配置文件不被改写**：`aliases` 落库内容不变（R8）。

## 7.1 检查阶段发现的既有跨层缺陷（本任务不修，需单独处理）

上游模型详情接口存在一处**与本任务无关但被本任务扩大到新形态**的前端硬断言冲突，质检阶段实测确认，记录在此以免丢失。

**事实链**：

1. `internal/control/price_reconcile.go:102-114` 的 `referenceCount` 对每个 `GroupModel` **配置条目** `++` 一次，不按名称展开。
2. `internal/control/project_model_collection.go:490-505` 的 `associations` 对每个 `(分组, 对外名称)` 产出一条，本次改动后名称集合为 `RoutableModelNames`。
3. `web/src/app/resources/models.ts:395` 硬断言 `price.reference_count !== associations.length` 即 `invalidResponse`。

**后果矩阵**：

| 配置形态 | 条目数 | 名称数 | 断言 |
| --- | --- | --- | --- |
| 单条目、无别名 | 1 | 1 | 通过 |
| 多条同 ID 记录表达多个别名（单别名时代存量写法） | 2 | 2 | 通过 |
| 单条目 + `aliases` 数组多别名 | 1 | ≥2 | **已失败（既有缺陷）** |
| 单条目、上游 ID 自身以 `[1M]` 结尾、无别名 | 1 | 改动后 2 | **由通过转为失败（本任务引入的形态）** |

第二行说明这个断言原本是按「一条记录一个名称」的假设写的；`GroupModel.Aliases` 数组（单条目多别名）引入后，`reference_count` 的口径没有同步更新，**任何使用多别名的配置其上游详情页都已经打不开**。本任务的改动让「上游 ID 自带 `[1M]`」这一原本 1:1 的形态也变成 1:2。

**为何不在本任务修**：修它要么改 `reference_count` 的口径（牵动价格对账与价格列表页显示的语义），要么改前端断言（违反本任务零前端改动的约束且是独立的契约变更）。两者都属于「价格引用计数」这个语义域，应当由独立任务评审并配测试，不适合夹带在路由兼容任务里。

**本任务的影响面**：仅「上游模型详情页」这一处 UI；不影响 `/v1/models`、路由、白名单、参数覆盖与分组模型页。PRD 的验收标准不覆盖该页面。

## 8. 被否方案与权衡

**方案 A：在 `ExternalModelNames` 内展开派生名。** 一处改动覆盖全部消费点，最省代码。否决原因：直接触发前端 `InvalidResponseError`，模型页整体不可用；且会让用户在前端看到无法编辑删除的别名。

**方案 B：保存时把基名物化进 `aliases`。** 前端展示天然一致、白名单可选、无新增概念。否决原因：改写用户配置，用户重读会发现自己没写过的别名；存量数据仍需额外迁移或兜底；派生名可被用户误删，产生隐性配置依赖。

**方案 C：仅在 `evaluateTargets` 未命中时回退试 `key+suffix`。** 改动面最小（约 5 行）。否决原因：`/v1/models` 仍只返回带后缀名，与用户选定的「两个名称都返回」不符；且巡检路径需同步加回退，容易漏改。

**方案 D：为后缀建通用机制（`[2M]`、`[128K]` 等）。** 按 YAGNI 否决，`ContextSuffixes` 保持为字面量表，未来确有需求时扩展即可。

## 9. 回滚

改动集中在 6 个文件、均为纯逻辑替换，无 schema 变更、无 migration、无配置改写。回滚即 `git revert` 提交，数据面无需任何修复动作。

## 10. 验证策略

- **单元测试**：`modelname` 包的 `Base`/`Allows` 边界（无后缀、两种大小写、纯后缀名、空名、超长名）。
- **快照测试**：`internal/state` 断言 `RoutableModelNames` 的派生集合与 `ExternalModelNames` 的不变形。
- **集成测试**：`internal/gateway` 用带后缀别名建立配置，断言以基名请求成功路由、`/v1/models` 同时含两名、响应模型名回写为请求名。
- **冲突测试**：`internal/control` 断言基名被他条目占用时返回 `MODEL_NAME_CONFLICT`。
- **回归**：既有 `group_models_test.go:361-368`（`len(targets) != 4` 类断言）、`models_test.go`、`snapshot_test.go` 必须全部通过；无后缀场景的断言值不应发生变化。
