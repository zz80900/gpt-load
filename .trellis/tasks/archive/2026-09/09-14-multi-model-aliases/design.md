# 技术设计：分组模型多别名

## 1. 数据模型

### Go 运行时状态 `internal/state/snapshot.go`

```go
type ModelConfig struct {
    ID      string
    Aliases []string
}
```

新增并**导出**（供 `control` 包复用，保证顺序不变量只定义一次）：

```go
// ExternalModelNames 返回模型对外的全部名称：ID 在首位，其后是别名。
// 丢弃空白项、与 ID 相同的项，以及重复项，保持配置顺序。
func ExternalModelNames(model ModelConfig) []string
```

**删除**单数版 `externalModelName`（不保留）。这是重构的安全阀：编译器会强制暴露全部调用点，杜绝漏改导致别名静默失效。

### TypeScript

```ts
// web/src/app/resources/groups.ts
export interface GroupModelUpdateDto { id: string; aliases: string[] }

// web/src/api/control/types.ts
export interface GroupModelItemDto {
  id: string
  aliases: string[]
  client_models: string[]   // [id, ...aliases]，服务端权威顺序
  pricing_status: ModelPricingStatus
}
```

## 2. 存储兼容

`Group.Models` 是 `models.JSON`，别名藏在 JSON 内部——**无需数据库 schema 迁移**。

### 读 `internal/state/loader/loader.go:80`

```go
type modelDTO struct {
    ID      string   `json:"id"`
    Aliases []string `json:"aliases"`
    Alias   string   `json:"alias"` // 旧版单别名字段
}

// 旧写入路径在 alias_enabled=false 时会把 alias 清空，
// 因此历史上 alias != "" 恒等价于「已启用」，可直接前向迁移。
func (m modelDTO) effectiveAliases() []string
```

`aliases` 非空用 `aliases`，否则回退单值 `alias`。

### 写 `internal/control/group_write.go`

落库只写 `aliases`，**不带 `omitempty`**——`{"id":"x","aliases":[]}` 明确表达「无别名」，与旧格式区分。首次保存即完成该行迁移。

## 3. 路由注册与唯一性校验

### 索引结构天然支持多名称

`appendExecutionTarget`（`snapshot.go:397`）的索引是
`map[protocol]map[operation]map[外部模型名][]RouteTarget`——key 就是外部名。一个模型注册多个名称只需多次调用，**索引结构不改**。

### 注册（`snapshot.go:379-388`）

先在 group 循环内预计算一次注册表（名称与协议/operation 无关）：

```go
type modelRegistration struct { upstreamID, name string }

registrations := make([]modelRegistration, 0, len(group.Models))
claimed := make(map[string]struct{}, len(group.Models))
for _, model := range group.Models {
    upstreamID := strings.TrimSpace(model.ID)
    for _, name := range ExternalModelNames(model) {
        if _, duplicate := claimed[name]; duplicate { continue } // 同 ID 重复条目在此合并
        claimed[name] = struct{}{}
        registrations = append(registrations, modelRegistration{upstreamID, name})
    }
}
```

再对每个 (clientProtocol, operation) 遍历注册表调用 `appendExecutionTarget`。
`UpstreamModelID` 恒取 `model.ID`——别名只影响匹配，不改变上游调用。

**去重是必需的**：否则同一 (name → upstream) 会在索引里出现两次，放大路由权重、失败重试会打到同一个 target。

### 校验（`snapshot.go:478-488`、`control/group_collection.go:439`）

关键：用**所有权判定**而非 ID 唯一性判定。

```go
claimed := make(map[string]string) // name -> 认领它的上游 ID
for _, model := range group.Models {
    upstreamID := strings.TrimSpace(model.ID)
    if upstreamID == "" { /* error */ }
    for _, name := range ExternalModelNames(model) {
        if owner, exists := claimed[name]; exists && owner != upstreamID {
            return fmt.Errorf("group %d has duplicate external model %q", group.ID, name)
        }
        claimed[name] = upstreamID // 同一 ID 重复认领合法（存量兼容，见 PRD C1）
    }
}
```

### 为什么「A 的 ID == B 的别名」必须报错

1. 该名字会解析到两个不同上游，路由结果由 `sortExecutionRouteIndex` 的排序决定而非配置决定——静默选第一条
2. 计费/归因会错：`PriceIdentityForChannelModel(channelID, model.ID)` 按上游 ID 计费，同名二义下日志与价格归属落到排序靠前者
3. 这是既有契约的延续：今天 `{ID:"a"}` + `{ID:"b",Alias:"a"}` 已报错，多别名只是把别名集合从 1 扩到 N
4. fail-closed 是项目既有降级策略（`Compile` 失败 → 拒绝发布 → 上一个好快照继续服务）

## 4. API 契约

### 请求 `internal/control/group_write.go`

- `GroupModel{ID, Alias, AliasEnabled}` → `{ID string; Aliases []string; AliasEnabled bool \`json:"-"\`}`
- `UnmarshalJSON` 兼容旧形态：`aliases` 为空且 `alias_enabled=true` 时回退 `[]string{alias}`。老 bundle 与发布冒烟脚本因此继续可用
- `alias_enabled` 的判定收拢进 `GroupModel.UnmarshalJSON` 后，`optionalGroupModels` / `groupModelRequestWire` 可整段简化（删除 `alias_enabled` 必填校验）
- `normalizeGroupModels` 重写：id trim 非空；aliases 逐项 trim → 去空 → 去等于 ID 的项 → 首次出现去重 → 保持顺序；上限 10；名称长度沿用 255

### 响应 `internal/control/group_models.go`

```go
type GroupModelResponse struct {
    ID            string        `json:"id"`
    Aliases       []string      `json:"aliases"`
    ClientModels  []string      `json:"client_models"` // [id, ...aliases]
    PricingStatus PricingStatus `json:"pricing_status"`
}
```

`Aliases` 必须序列化为 `[]` 而非 `null`（否则前端 `projectArray` 抛错）。
**不保留任何旧字段**（见 PRD C3）。

`ModelNameConflict.ClientModel` 语义从「该条目的对外名」变为「被多个条目认领的对外名」。

## 5. 消费点适配

| 位置 | 改动 |
|---|---|
| `control/group_options.go:98-128` | 模型候选集展开 ID + 全部别名并按名去重 |
| `control/home.go:282-368` | 删除 `homeExternalModelName`；过滤改「任一名称命中即允许」；计数改**按模型计**（否则虚增） |
| `control/project_model_collection.go:255/276/411/492` | 单值 → 名称列表；访问范围过滤改「任一名称命中即保留」 |
| `control/group_create.go:226`、`group_update.go:46`、`group_collection.go:446` | 类型同步 |
| `.github/scripts/release-docker-smoke.sh:310` | 请求体改 `{id,aliases:[]}`，否则发布冒烟 400 |

**语义扩张需记录**：ID 恒可达后，限定别名的访问密钥能触达的名字变多（以前别名会遮蔽 ID）。这与 R2 一致，需在变更说明中写明。

## 6. 前端

### 标签输入组件 `web/src/components/ui/TagInput.vue`（新建）

放 `components/ui/`（通用控件，与 `AppTextInput` 同级），不放 `features/models/`。

交互规格：

| 场景 | 行为 |
|---|---|
| `Enter` | preventDefault + commit；空输入不动 |
| `,` / `、` 等分隔符 | commit 且不把分隔符写入输入框 |
| `Backspace` 且输入为空 | 删除末标签，焦点留输入框（**不**把标签文本还原进输入框） |
| 粘贴含分隔符 | split → trim → 去空 → 与已有及本次内部去重 → 一次性 commit |
| `blur` | 先 commit 未提交文本再 emit blur，且**合并为一次 `update:modelValue`**（否则父组件脏检查错位） |
| 去重 | 大小写敏感精确比较（与服务端一致） |

**不复用 `AppTextInput`**：它的单 input + `<label>` 布局放不下换行标签，且会破坏 `:deep(.icon-button)` 尺寸规则。样式沿用其令牌（`--color-border-control`、`--radius-control`、`--control-xs/sm`、`--text-meta`）与 860px 断点。
删除按钮用真实 `<button type="button">` + `aria-label`。

### 编辑器 `web/src/features/models/ModelAliasEditor.vue`

- 移除启用开关与 `setAliasEnabled`
- 别名列换 `TagInput`；`updateRow` patch 改 `Pick<ModelDraftValue,'id'|'aliases'>`
- 搜索过滤 `item.alias` → `item.aliases.join(' ')`
- `focusFirstInvalid` 的 `targetsModelID` 改用「冲突名是否等于该行 ID」
- `--ledger-record-list-record-min-height` 58px → 64px（多标签换行）

### 连带点

`model-diff.ts:26-35,71`、`GroupSettingsBaseForm.vue:51,153`、`GroupModelSyncDialog.vue:121`、`features/import/model-draft.ts:53`、`NewGroupImport.vue:786`、`import-recovery.ts`（version 8→9 + `isModel` 白名单 + 迁移分支）。

### i18n

`zh-CN/group.ts`：改 `aliasPlaceholder`、表头 `alias`；增 `removeAliasFor`；删 `aliasEnabledFor` / `aliasRequired` / `emptyAliasSummary`。
**en-US / ja-JP 不动**（PRD C4）。

## 7. 风险清单

1. **存量「同 ID 多条目」必须继续可编译**——用名称所有权判定，不能假设 ID 唯一
2. **任何让 `Compile` 失败的存量数据都是启动级故障**（`loader.go:589-663` 整段返回 error）
3. **响应契约无兼容窗口**——前端 projector 严格白名单，服务端与前端必须同批
4. **发布门禁脚本** `.github/scripts/release-docker-smoke.sh:310` 必须同步，否则发布流水线冒烟失败
5. **访问密钥模型过滤的语义扩张**需在变更说明显式记录
