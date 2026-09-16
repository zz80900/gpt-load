# 设计：模型别名通配符匹配与 Claude 适配开关

## 1. 边界与目标形态

本特性只在两处引入新概念，其余全部是既有机制的复用或展示层过滤：

| 层 | 变更性质 |
| --- | --- |
| `internal/modelname` | 新增纯函数：模式判定、模式匹配、特异性度量 |
| `internal/state` | 新增**独立**的通配符索引结构（不混入精确索引） |
| `internal/scheduler` | 唯一查找点新增「精确未命中 → 模式兜底」分支 |
| `internal/control`、`internal/gateway` | 展示层枚举面过滤模式名 |
| `web/` | 新增一列开关 + 常量别名的隐藏与回写 + 展示层过滤 |

不改：`ExternalModelNames`（`client_models` 契约）、`normalizeModelAliases` 的文法、
`modelname.Allows`、`parameteroverride` 的规则语义、存储形状、任何迁移。

## 2. 为什么模式必须走独立索引，而不是索引里的一个特殊键

既有 spec（`.trellis/spec/backend/model-name-contract.md` 第 3 节）规定「路由索引是
唯一注册点，`/v1/models` 遍历索引键，因此注册进去就免费出现在列表里」。这条规定
对**有限可枚举**的后缀派生成立——`xxxx[1M]` 的基名 `xxxx` 能在构建期算出来。

通配符不成立：`claude-*` 的匹配集合是无穷的，无法在构建期展开成索引键。于是有两
条路：

- **A：把模式字面量当索引键**（`index[proto][op]["claude-*"] = targets`）。
  代价是每个遍历索引键的消费点都必须学会跳过 `*`：`/v1/models`
  （`internal/gateway/models.go:95`）、以及将来任何新增的遍历点。漏掉一处就是把
  `claude-*` 当成真实模型广播给客户端。同时精确查找的热路径上多出一个永远不可能
  被命中的键。
- **B（选定）：模式进独立索引**，精确索引里只有具体名称。

选 B 的理由：`/v1/models` **一行都不用改**就能满足 R13——它遍历的是精确索引，
模式从来不在里面。规格里的「唯一注册点」不变量因此仍然成立，只是需要把定义从
「索引键集合」修订为「精确索引键集合 ∪ 模式索引模式集合」。这是本任务必须同时
修订 spec 的原因。

## 3. 数据结构

### 3.1 `internal/modelname` 新增

```go
// IsPattern 报告 name 是否为通配符模式。
func IsPattern(name string) bool

// Match 报告 name 是否被 pattern 匹配。'*' 匹配任意长度（含空）的字节序列；
// pattern 不含 '*' 时退化为字符串相等。
func Match(pattern, name string) bool

// PatternSpecificity 返回模式的裁决度量：首个 '*' 之前的字面前缀字节数，
// 以及全部字面（非 '*'）字节数。用于重叠模式的确定性排序。
func PatternSpecificity(pattern string) (prefixLen, literalCount int)
```

放在 `modelname` 而非 `state`：该包的既定职责就是「客户端名称归一」，且刻意不依赖
`gpt-load/internal` 下的任何包（`state` 依赖 `parameteroverride`，反向依赖会成环）。
`scheduler` / `state` / `gateway` 都已经 import 它，模式匹配与后缀归一必须共用同一
份定义，否则又会复制出四份会漂移的实现。

`Match` 用双指针回溯实现（遇 `*` 记档、失配回退），字节级比较，不做 Unicode
归一——模型名在本项目全程按字节精确比较（`group_models_test.go` 有「大小写敏感」
的既有契约）。

### 3.2 `internal/state/snapshot.go` 新增

```go
// ModelPattern 是一条通配符模式及其认领的全部目标。
type ModelPattern struct {
    Pattern string        // 原样模式串，用于报错与巡检展示
    Targets []RouteTarget
}

// ModelPatternIndex 按协议与操作索引通配符模式。每个切片在构建期按特异性降序
// 排定，因此查找是「首个命中即返回」，结果与配置顺序、map 遍历顺序无关。
type ModelPatternIndex map[protocol.Protocol]map[execution.Operation][]ModelPattern
```

`ConfigSnapshot` 新增两个字段，与既有两个索引一一对应：

```go
ExecutionCandidates    ExecutionCandidateIndex
ExecutionRouteCatalog  ExecutionCandidateIndex
ExecutionPatterns      ModelPatternIndex   // 数据面兜底
ExecutionRoutePatterns ModelPatternIndex   // 巡检兜底
```

**两个都要建**。`Compile` 对巡检目录的注册（`snapshot.go:324`）发生在
`group.Enabled` 判断**之前**，对数据面候选的注册（`snapshot.go:369`）在**之后**；
只建一个会让路由巡检给出与真实路由不同的答案。既有两个索引面临同样的分野，这里是
沿用而不是新引入。

两个字段都必须在 `Compile` 内构建完成、返回后不再改动：`Manager.publishCompiledLocked`
直接存指针，数据面只持 `publishMu.RLock` 并发读（`internal/state/manager.go:126-140`）。
且构建顺序必须确定——`Manager.Matches` 对整个快照做 `reflect.DeepEqual`
（`internal/state/manager.go:99-115`），构建期若依赖 map 遍历顺序，每次 reconcile 都会
报告「配置已变更」，表现为随机的 503 `configuration_changed`。因此最终切片在
构建期**排序**产出，不使用 map 迭代顺序。

### 3.3 名称集合：不新增集合，只新增过滤器

既有三个集合的分工保持不变：

| 集合 | 是否含模式 | 理由 |
| --- | --- | --- |
| `ExternalModelNames` | **含**（字面量） | `client_models` 契约 = `[id, ...aliases]`，前端 `groups.ts` 逐项硬断言 |
| `RoutableModelNames` | **含**（字面量 + 后缀基名） | 写路径与编译期冲突检测必须看见模式 |
| 精确路由索引键 | **不含** | 由 §3.4 的分流保证 |
| 模式路由索引 | **仅含模式** | 同上 |

新增两个只读过滤器，供展示层使用：

```go
// ConcreteModelNames 返回可枚举的具体模型名：ExternalModelNames 去掉通配符模式。
func ConcreteModelNames(model ModelConfig) []string

// ConcreteRoutableModelNames 返回可枚举的具体可路由名：RoutableModelNames 去掉通配符模式。
func ConcreteRoutableModelNames(model ModelConfig) []string
```

两者共用同一个私有 `withoutPatterns(names []string) []string`，保证「什么算模式」只有
一个定义（`modelname.IsPattern`）。

## 4. 数据流

### 4.1 构建期：唯一注册点分流

`appendExecutionTargets`（`snapshot.go:432`）是既有唯一注册点。签名增加一个模式索引
参数，函数体在既有 `claimed` 去重（第 456-466 行）**之后**、按名称形态分流：

```
registrations = dedup(RoutableModelNames(model) for model in group.Models)
├── IsPattern(name) == false → appendExecutionTarget(index, ...)          // 既有路径，逐字不变
└── IsPattern(name) == true  → appendModelPattern(patterns, ..., name, target)
```

- **去重必须在分流之前**。`claimed` 去重的存在理由（`snapshot.go:449-453` 的注释）是
  防止同一 `(名称 → 上游)` 注册两次导致该目标候选权重翻倍、失败重试反复落同一个
  target。模式走同一份去重，因此同一上游 ID 的多条记录共用同一模式只注册一次（R18）。
- **两条路径互斥**：`IsPattern` 是同一个谓词，一个名称不可能同时进两个索引。
- **`NoModelRouteKey` 分支**（第 481-487 行的资源操作）不经过 `registrations`，因此
  永远不会进模式索引（AC5）。
- `ModelPattern.Targets` 必须填全 `UpstreamModelID` 与 `Mode`（与精确路径同源，
  第 498-506 行）。`UpstreamModelID` 缺失会让响应回显静默失效、上游模型名泄漏给
  客户端，并同时破坏模型冷却与计价归因。

构建期用 builder map 聚合跨分组的同名模式：

```go
type modelPatternBuilder map[protocol.Protocol]map[execution.Operation]map[string]*ModelPattern
```

`Compile` 末尾新增 `finalizeModelPatternIndex(builder) ModelPatternIndex`：把每个
`(protocol, operation)` 下的模式按 §4.4 的特异性规则降序排序，并对每个模式的
`Targets` 施加与精确索引相同的目标排序。

`sortExecutionRouteIndex`（`snapshot.go:540`）的目标比较器要提取为共享的
`lessRouteTarget(a, b RouteTarget) bool`，供两处复用（DRY：目标优先级只能有一个定义，
否则「原生路由优先 → GroupID → 上游 ID」这条规则会分叉）。

### 4.2 请求期：唯一查找点兜底

`evaluateTargets`（`internal/scheduler/inspect.go:98`）是全项目**唯一**的模型名查找
入口，三个调用方共用：

- 数据面候选冻结：`CandidateGroupIDsForQuery`（`scheduler/scheduler.go:102`），在
  凭据捕获**之前**调用
- 数据面候选池：`filterTargetsWithReason`（`scheduler/scheduler.go:289`）
- 巡检：`Inspect`（`scheduler/inspect.go:367`），读 `ExecutionRouteCatalog`
- 间接第四个：`Iterator.CooldownUntil`（`scheduler/model_cooldown.go`）在迭代器耗尽时
  调 `Inspect`

因此兜底只加在这一处，四个调用路径同时生效。签名增加模式索引参数：

```go
func evaluateTargets(
    snapshot *state.ConfigSnapshot,
    index state.ExecutionCandidateIndex,
    patterns state.ModelPatternIndex,
    query normalizedQuery,
) ([]targetDecision, ReasonCode, error)
```

查找段（第 136-143 行）改为：

```go
modelKey := state.NoModelRouteKey
if query.externalModel != nil {
    modelKey = *query.externalModel
}
routes := byModel[modelKey]
// 精确优先：命中即结束，不与模式结果合并或重排（R3）。
// query.externalModel == nil 是无模型资源请求，其 modelKey 为保留空串，
// 必须留在精确索引里，绝不进入模式匹配（AC5）。
if len(routes) == 0 && query.externalModel != nil {
    routes = matchModelPattern(patterns[query.clientProtocol][query.operation], modelKey)
}
if len(routes) == 0 {
    return []targetDecision{}, ReasonNoRouteTarget, nil
}
```

`matchModelPattern` 线性扫描已排序的模式切片，返回首个命中的 `Targets`；未命中返回
`nil`。返回的 `[]RouteTarget` 与精确命中同构，下游决策构造（第 145-200 行）零改动。

**为什么不在 gateway 侧兜底**：请求路径与巡检路径共用这一个查找；在 gateway 加一份
会让路由巡检给出与真实路由不同的判断（既有 spec 已把这条列为 Wrong，本任务保留该
结论，只是把「不许有兜底」修订为「兜底只许有这一处」）。

**确定性约束**：`CandidateGroupIDsForQuery` 的结果被用于捕获凭据，`newWithClock`
随后用它建候选池。两次调用必须对同一 `(snapshot, query)` 返回相同结果，否则会出现
「凭据按 A 组捕获、派发按 B 组」——表现为成功的捕获之后跟着 503 或
`configuration_changed` 重试。模式匹配是纯函数且模式切片已排序，满足该约束。

### 4.3 展示层过滤清单

R14 要求模式名不出现在任何「客户端可用名称」枚举面。过滤点如下（后端全部改用
§3.3 的 `ConcreteModelNames` / `ConcreteRoutableModelNames`）：

| 位置 | 现状 | 处理 |
| --- | --- | --- |
| `internal/control/group_options.go:124` | `ExternalModelNames` | 换 `ConcreteModelNames` |
| `internal/control/project_model_collection.go:259` | `RoutableModelNames` | 换 `ConcreteRoutableModelNames` |
| `internal/control/project_model_collection.go:496` | `RoutableModelNames` | 换 `ConcreteRoutableModelNames` |
| `internal/control/price_reconcile.go:131` | `RoutableModelNames` | 换 `ConcreteRoutableModelNames` |
| `internal/control/home.go:280,402` | `ExternalModelNames` | 换 `ConcreteModelNames` |
| `internal/gateway/models.go:95` | 遍历精确索引键 | **不改**（模式不在精确索引里） |
| `internal/control/group_models.go:83` | `ExternalModelNames` → `client_models` | **不改**（契约） |
| `internal/control/group_collection.go:449` | `ExternalModelNames`，读路径宽松校验 | **不改**（见 §6） |

`project_model_collection.go` 的两处与 `price_reconcile.go` 的一处**必须在同一个提交里
一起改**。它们共同定义 `price.reference_count` / `associations.length` /
`client_model_count`，前端 `web/src/app/resources/models.ts` 用一个 `if` 交叉断言这几个
数字。历史上正是这一对漂移导致过一次线上缺陷（见 spec 的计数契约一节与回归基线
表），spec 也点明它们「只靠约定耦合、无结构约束」。

`project_model_collection.go:255-259` 的注释「与 /v1/models 一致」在本任务后不再成立
（`/v1/models` 走精确索引，模型页走过滤后的可路由名），必须改写为新的口径说明。

### 4.4 重叠模式的裁决

`finalizeModelPatternIndex` 对同一 `(protocol, operation)` 下的模式按下述键排序：

1. `prefixLen` 降序（首个 `*` 之前的字面前缀更长者优先）
2. `literalCount` 降序（字面字符更多者优先）
3. `Pattern` 字节序升序

查找取**首个命中**，因此只有一条模式贡献目标，不存在合并。

举例：`claude-*` 与 `claude-sonnet-*` 同时存在，请求 `claude-sonnet-5`：
`prefixLen` 分别为 7 与 14 → `claude-sonnet-*` 胜出。请求 `claude-opus-5`：前者命中，
后者不匹配（`claude-` 之后的 `opus-5` 不以 `sonnet-` 开头）。

裁决与配置顺序、分组 ID、map 遍历顺序全部无关（AC4）。排序在构建期完成，请求路径
不做任何排序或分配。

### 4.5 冲突检测：零改动

`normalizeGroupModels`（`internal/control/group_write.go:243`）与 `validateCompileInput`
（`internal/state/snapshot.go:609`）都是「按 `RoutableModelNames` 的精确字符串认领，
跨上游 ID 同名即冲突」。模式作为字面量进入这个集合后：

- 同一分组内两个不同上游 ID 配同一模式 → `MODEL_NAME_CONFLICT`（R16/R17，AC7）
- 同一上游 ID 的多条记录共用同一模式 → 认领者是同一个 ID，不冲突（R18，AC8）
- 两个不同但重叠的模式 → 字符串不等，不冲突（R20，AC9）
- 模式与精确名 → 字符串不等，不冲突（R19，AC2）

**错误信息也不需要改**。`modelNameConflictError`（`snapshot.go:129`）先查
`slices.Contains(external, name)`：模式名本身在 `external` 里，走第二分支；
`derivedNameSource` 会用 `modelname.Base` 找出派生来源。别名 `claude-*[1m]` 的
冲突因此被报成「由 `claude-*[1m]` 剥掉上下文后缀派生」——这句话是准确的，因为
`claude-*` 确实来自它。

**不要**把冲突检测扩展成「模式与精确名的前缀重叠检查」：那会拒绝 `claude-*` 与
`claude-opus-5` 共存，正好否掉本特性存在的理由。

写路径与编译期必须始终用同一个集合、同一条规则，否则会出现「保存成功、新配置不
生效」——`UpdateGroupModels` 有编译预检（`group_models.go:133-140`），但它只区分
「本次提交是修复动作」与「存量另有损坏」，不能替代两侧口径一致。

## 5. 前端设计

### 5.1 常量与谓词（唯一来源）

`web/src/features/models/model-draft.ts`：

```ts
/** Claude 适配开关写入的别名。后端把它当普通通配符别名处理，不需要 Go 侧常量。 */
export const claudeAdapterAlias = 'claude-*[1m]'
/** 视为「Claude 适配已开启」的别名集合：两种后缀大小写都认，写入时一律用规范形态。 */
const claudeAdapterAliases: readonly string[] = [claudeAdapterAlias, 'claude-*[1M]']
export function isClaudeAdapterAlias(alias: string): boolean
export function hasClaudeAdapter(aliases: readonly string[]): boolean
export function isWildcardAlias(alias: string): boolean      // alias.includes('*')
export function visibleAliases(aliases: readonly string[]): string[]
export function withClaudeAdapter(aliases: readonly string[], enabled: boolean): string[]
```

`hasClaudeAdapter` / `isClaudeAdapterAlias` / `visibleAliases` / `withClaudeAdapter` 全部
建立在同一份 `claudeAdapterAliases` 上，任何展示、隐藏、回写、冲突判定都只经由它们，
不得在组件里再写字符串字面量。

`ModelAliasEditorLabels` **不新增字段**：该列的文案由 §5.7 的可选 prop 携带。

**识别两种大小写、写入只用小写**的理由：`modelname.ContextSuffixes` 把 `[1M]` 与
`[1m]` 当作两个等价后缀，用户在别名框里手输大写形态是完全可能的；若只认小写，
大写形态会同时出现在「可见别名」与「路由行为」两个位置，开关却显示关闭，属于
明确的误导。

### 5.2 被隐藏的是常量，不是「所有通配符」

`visibleAliases` 只过滤 `claudeAdapterAliases` 这两项，不过滤一般通配符。理由是
DRY/YAGNI 之外的一条硬约束：若把所有含 `*` 的别名都隐藏，用户手输的 `gpt-*` 会
「输入即消失」，且它仍会被保存（`normalizeAliases` 不做形态判断），用户无法删除
一个看不见的值。R12 明确要求手输的其他通配符别名照常可见可编辑。

搜索框的匹配文本（`ModelAliasEditor.vue:74` 的 `item.aliases.join(' ')`）同样改用
`visibleAliases`：隐藏就要藏干净，否则输入 `claude` 会命中所有开着开关的行，与
「看不到它」自相矛盾。

### 5.3 列插入

`ModelAliasEditor.vue` 的列顺序完全由 DOM 顺序决定，没有任何位置元数据：表格根是
`display:grid`，表头行与记录行都是 `grid-template-columns: subgrid`，单元格落在哪一列
就是它在父级里的第几个子元素。因此**三处改动必须落在同一个下标**（且都受 §5.7 的
`claudeAdapter` prop 控制，未提供时三处一并缺席）：

1. 第 2 个轨道由 §5.7 的 `--claude` 修饰类插入。列最小宽度从 620px 升到约 736px，
   4 个间距 64px，合计约 800px，而桌面栅格只在 860px 以上生效，且
   `.ledger-record-list` 带 `overflow-x: auto`，挤压时横向滚动而非重叠。
2. 表头 `<span role="columnheader">` 插在第 238 行（别名）与第 239 行（`thirdColumn`）之间。
3. 记录内的 `.ledger-record-list__cell` 插在别名单元格（止于 306 行）与
   `.model-alias-editor__third-column`（起于 308 行）之间。

插了表头没插单元格（或下标不一致）会静默错位，CSS 与类型检查都不会报错——这是
本列唯一的实现陷阱。

**不要改 `LedgerRecordList.vue`**：它是全应用共用的通用栅格，页面通过
`grid-class` 传入的 CSS 变量是它文档化的唯一扩展点。改它会影响应用里所有 ledger 列表。

### 5.4 移动端（<=860px）

该断点下 `LedgerRecordList` 把记录切成卡片两列并隐藏表头，`ModelAliasEditor` 只给
ID 与别名单元格 `grid-column: 1 / -1`，其余靠自动排布。若不给新单元格显式定位，开关
会与价格状态挤在同一行的两列里。因此新单元格在 860px 块内显式
`grid-column: 1 / -1`，并加入既有 `display: grid; align-content: start; gap: 5px` 的选择器
列表，使卡片依次读作 ID / 别名 / 开关 / 价格状态（AC21）。移动端标题复用
`.model-alias-editor__mobile-label`，按 `AccessKeyCollection.vue:450-455` 的先例在 flex
容器里用 `flex-basis: 100%` 独占一行。

### 5.5 开关的状态与回写

`AppSwitch`（`web/src/components/ui/AppSwitch.vue`，reka-ui）是完全受控组件，本身无
内部状态，必须由派生值驱动：

```vue
<AppSwitch
  :model-value="hasClaudeAdapter(item.aliases)"
  :label="labels.claudeAdapterFor(item.id)"
  :disabled="disabled"
  @update:model-value="setClaudeAdapter(index, $event)"
/>
```

不做本地乐观副本：`discard()`（`GroupModelsTab.vue:496`）会把 `draft` 还原成 `saved`，
乐观副本会与还原后的数据脱节。

`setAliases`（`ModelAliasEditor.vue:92`）收到的是 TagInput 发出的**可见**列表，必须在
`normalizeAliases` 之前把开关状态重新合并回去，否则隐藏的常量会在用户编辑别名时被
静默删除（R11）：

```ts
function setAliases(index: number, aliases: string[]): void {
  const item = props.modelValue[index]
  if (item === undefined) return
  updateRow(index, {
    aliases: normalizeAliases(
      withClaudeAdapter(aliases, hasClaudeAdapter(item.aliases)),
      item.id,
    ),
  })
}
```

`withClaudeAdapter` / `updateRow` 都必须构造**新数组**。`GroupModelsTab.vue:243` 的
`saved` 与 `draft` **共享同一个 `aliases` 数组引用**（展开对象时没有展开数组），就地
`push` 会污染 `saved`，使 `dirty` 永远为真且 `discard()` 无法恢复。

`normalizeAliases`（`model-draft.ts:127`）本身**不改**：它只做 trim、去空、去 ID、
精确去重，通配符字面量能原样通过——这正是开关状态能穿过保存、脏检查与同步的原因。

### 5.6 开关互斥与冲突错误的落点

**开关在分组内互斥。** `setClaudeAdapter(index, true)` 不只改本行，还要清掉分组内其他
所有行的常量别名，因此它不能用 `updateRow`（那只改一行），必须自己 `map` 整个
`modelValue`：目标行按 `enabled` 写入，其余行在 `enabled` 为真且已含常量时按
`false` 写回，既不匹配的行只克隆 `sources`。关闭某个开关只影响它自己。

理由：后端的同名模式冲突规则要求同一分组内两个上游不能认领 `claude-*[1m]`，这条规则
本身是对的、也必须保留；但让用户「先手工关掉另一个再打开这一个」是把一条内部约束
泄漏成了操作步骤。互斥让那个状态在 UI 上根本不可能出现。

`withClaudeAdapter` 与整个 `map` 都必须构造**新数组**。`GroupModelsTab.vue:243` 的
`saved` 与 `draft` **共享同一个 `aliases` 数组引用**（展开对象时没有展开数组），就地
`push` 会污染 `saved`，使 `dirty` 永远为真且 `discard()` 无法恢复。

`normalizeAliases`（`model-draft.ts:127`）本身**不改**：它只做 trim、去空、去 ID、
精确去重，通配符字面量能原样通过——这正是开关状态能穿过保存、脏检查与同步的原因。

前端 `findModelNameConflicts`（`model-draft.ts:152`）**也不需要改**：它按精确字符串
认领名称，与后端 R17/R19/R21 的规则逐条一致。互斥把开关路径的冲突消解掉了，但这个
检测本身仍然正确。

需要的是**错误呈现位置**。`ModelAliasEditor.modelAliasError` 会把冲突归到别名列，而
该别名被隐藏了——用户会看到一行红底、别名框空空如也、错误文案指着一个看不见的字符
串。处理方式：冲突名是 `claudeAdapterAlias` 之一时，把错误改挂到开关单元格上，并使用
专门的文案（`claudeAdapterConflict()`）。这需要在 `ModelAliasEditor.vue` 里保留
`claudeAdapterError(index)`，并在 `visibleInvalidIndexes` 的计算里把开关单元格的错误
一并纳入。

**这条错误现在只覆盖开关管不到的通路**：用户在别名框里手输了两次 `claude-*[1m]`，或
库里存在改动前保存的重复数据。互斥不做拦截——手输是在具体字段里发生的，静默删掉另一
行的值会让用户莫名其妙。

### 5.7 列的启用：一个可选 prop，不用必填 label

该列**只在分组详情页**出现，批量导入页复用同一个组件但不渲染它。实现上用**单个可选
prop**同时承担「启用该列」与「提供其文案」两件事：

```ts
withDefaults(
  defineProps<{
    // ...既有 props
    claudeAdapter?: {
      title: string
      forModel: (id: string) => string
      conflict: (id: string) => string
    }
  }>(),
  { claudeAdapter: undefined },
)
```

列渲染条件就是 `claudeAdapter !== undefined`。这样做的理由：

- 把「启用」与「文案」拆成两个 prop（例如 `claudeToggle` + 三个必填 label 字段）会
  逼导入页提供一份永远用不到的必填文案，并在 `zh-CN/import.ts` 里留下三个死键。
- 把文案塞进既有的必填 `ModelAliasEditorLabels` 会让类型检查强制导入页也补齐，
  同样产生死键。
- 单一可选对象让「没有文案就不能渲染列」成为类型层面的事实，不存在半启用状态。

`GroupModelsTab.vue:159-183` 的 label 对象**不需要改**（不含 claude 字段），改为在
模板里多传一个 `:claude-adapter="{ title: t('group.modelEditor.claudeAdapter'),
forModel: (id) => t(...), conflict: (id) => t(...) }"`。

**栅格轨道数必须随 prop 变化**。`--ledger-record-list-grid` 定义在
`ModelAliasEditor.vue:384`，当前是 4 个轨道。新增 5 轨道的规则而不是改写既有规则：

```css
/* 既有 4 轨道规则保持逐字不变 —— 导入页走这条，布局与改动前完全一致 */
.model-alias-editor__grid { --ledger-record-list-grid: <原值>; }

/* 分组详情页多一列，插在第 2 个轨道 */
.model-alias-editor__grid--claude {
  --ledger-record-list-grid: minmax(180px, 24fr) minmax(280px, 52fr) 116px minmax(120px, 18fr) 40px;
}
```

`grid-class` 由计算属性产出：`claudeAdapter` 存在时返回
`'model-alias-editor__grid model-alias-editor__grid--claude'`，否则返回
`'model-alias-editor__grid'`。这样导入页的布局改动量为零，符合 R8 与 AC22。

导入页只需保持其他 props 不变即可；它的模型行不会产生该常量别名，但
`visibleAliases` / `withClaudeAdapter` 的过滤与回写逻辑**不做条件分支**——一旦草稿里
存在该别名（例如从恢复草稿载入），保存时必须原样保留（R12 / AC23）。

### 5.8 前端展示层过滤

这些点的数据源都是从 `client_models` 派生的名称枚举，模式名会随之泄漏：

| 文件 | 位置 | 作用 |
| --- | --- | --- |
| `web/src/app/resources/groups.ts` | `cacheGroupModels`（~1000） | 用 `client_models` 重写分组选项缓存，是所有分组选项消费者的上游 |
| `web/src/components/config/ParameterOverrideRulesEditor.vue` | `modelSuggestions`（~146） | 参数覆盖规则的模型自动补全 |
| `web/src/features/groups/settings/GroupSettingsBaseForm.vue` | `validationModelOptions`（48-53） | 校验模型下拉，选项标签是 `aliases.join('、')` |
| `web/src/features/groups/models/GroupModelSyncDialog.vue` | `removalPreview`（118-125） | 删除确认弹窗里的别名映射；过滤后 `v-if` 的判空要跟着改，否则 `→` 右侧为空 |
| `web/src/features/access-keys/access-key-options.ts` | `buildAccessKeyModelOptions`（26-35） | 访问密钥模型白名单候选 |
| `web/src/features/monitor/InspectorTab.vue` | `configuredModels`（104-108） | 巡检表单的模型下拉 |

实现时逐个确认其数据源是否已被 `cacheGroupModels` 覆盖，不要凭文件名推断。

## 6. 明确不做的判定（含理由）

| 项 | 决定 | 理由 |
| --- | --- | --- |
| 把模式从 `ExternalModelNames` 里剔除 | 不做 | `client_models` 契约是 `[id, ...aliases]`，`groups.ts` 逐项断言。剔除需要改前端的跨层契约，且会让「保存时原样写回」失去依据 |
| 把模式从 `validateGroupCollectionModels` 的认领集合里剔除 | 不做 | 读路径必须保持宽松：它返回错误会被 `groupCollectionDataError` 包成 `ErrInternalServer`，分组列表与分组选项接口整体 500，用户失去唯一的修复入口。既有 spec 已把这条不对称记录为有意设计 |
| 给 `modelname.Allows` 加模式语义（C5） | 不做 | 白名单描述「客户端能用哪些名称发请求」，而 R14 已把模式挡在选择器之外。新增写路径校验去拒绝 `*` 更糟：既有存量数据会在编译期失败并冻结配置发布 |
| 禁止裸 `*` 或字面量为空的模式 | 不做 | 是一个可恢复的配置选择而非数据损坏；加守卫需要写路径 + 编译期两处新校验并镜像到前端（前端 `normalizeAliases` 是无错误返回的纯归一函数，改它会波及所有调用点），收益不匹配成本。改为在 README 里写清后果（R6） |
| 禁止上游 ID 含 `*` | 不做 | 与别名走同一套判定；该组合的计价身份行为记为已知边界（C7），不新增校验以免既有存量数据在编译期失败 |
| 统一 `parameteroverride` 的 `*` 方言 | 不做 | 该方言作用于人写的规则、匹配客户端请求名；改它会改变既有全部规则的含义。只在文档里记录两套方言并存 |
| 在巡检响应里暴露「命中了哪个模式」 | 不做 | 前端可见 DTO 变更，且巡检页当前还要求模型名必须属于已配置集合（`InspectorTab.vue:283` 的 `validatedRequest`），单加字段也无法让通配符路由在管理端可查。两项合起来才是可用特性，作为后续任务 |
| 别名控制字符校验 | 不做 | 现状即如此，修正它超出范围；但方案不得新引入依赖「别名无控制字符」的逻辑（C6） |

## 7. 兼容性与回滚

**兼容性**

- 无通配符的配置：`IsPattern` 全为 false，`ConcreteModelNames` 是恒等函数，
  `matchModelPattern` 永不被调用（精确命中短路），两个新索引为空。行为逐项不变。
- 存量含 `*` 的别名：今天语法合法但行为是死的（不匹配任何请求、只在 `/v1/models`
  里以字面 `*` 出现）。升级后它变成真模式。这是**静默的语义变更**，必须在 README
  与 spec 里写明，让升级用户知道「以前无效的 `*` 别名现在会真的生效」。
- `client_models` 形状、`ExternalModelNames`、`Allows`、存储 JSON、迁移注册表全部
  不动；`Group.Models` 是 opaque JSON 列，`*` 不是 JSON 转义字符，往返字节不变。
- 控制面读路径的 `GroupModel.UnmarshalJSON` 用 `DisallowUnknownFields`：本设计没有
  新增持久化字段，因此不会触发 `json: unknown field` 导致分组列表 500。

**回滚**

改动全部是可逆的代码变更，无数据迁移，因此回滚 = 回退提交。需要注意的中间态：

- 若已保存过 `claude-*[1m]` 别名，回滚到旧版本后该别名退化为死字面量，并重新出现
  在 `/v1/models` 里（旧版本会把它当普通名称广播）。回滚后应从界面或配置里清掉这些
  别名。
- 快照编译失败时既有契约保持：不发布新快照、旧配置继续服务、`Publish` 不换指针。

## 8. 必须同步修订的 spec

`.trellis/spec/backend/model-name-contract.md` 是本特性的**对立面**，不修订就是给下一个人
埋雷：

1. 第 1 节的三集合表新增一行：通配符模式集合（归属、受众、形状）。
2. 第 3 节「The routing index is the single registration point」需要重写为：精确索引键
   集合 **∪** 模式索引模式集合才是「可路由」的完整定义；`/v1/models` 只枚举精确索引
   键，因此模式**不会**免费出现在列表里——这条恰好与原文相反。
3. 第 3 节补充：`ExternalModelNames` / `RoutableModelNames` 保留模式字面量（契约与
   冲突检测需要），展示层必须经 `ConcreteModelNames` / `ConcreteRoutableModelNames`
   过滤。
4. 第 4 节的冲突检测表补充模式冲突一行，并写明「模式不与精确名、也不与重叠模式
   构成冲突」。
5. 第 7 节的 Wrong 示例（禁止在查找路径加兜底）必须重写：该结论对后缀派生成立，
   对通配符不成立，兜底**必须**存在于 `evaluateTargets`，但只许存在于这一处。
6. 计数契约一节的回归基线表补一行含 `*` 的配置，并写明模式名不计入
   `reference_count` / `client_model_count`。
