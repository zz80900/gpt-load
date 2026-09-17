# Design：modern 前端同步多别名 / 通配符 / Claude 适配开关

## 1. 总体结构

```
web/src/shared/models/model-aliases.ts     ← 新建：纯逻辑单源（两侧共用）
web/src/frontends/classic/features/models/model-draft.ts
                                           ← 改造：删除平移走的实现，原地 re-export
web/src/frontends/modern/components/ui/AppTagInput.vue
                                           ← 新建：tag 式输入公共组件
web/src/frontends/modern/api/group-create.ts / group-detail.ts
                                           ← 改造：草稿形状 alias:string → aliases:string[]
web/src/frontends/modern/features/groups/group-create-rules.ts
                                           ← 重写：冲突检测 + 列归属 + 开关 setter
web/src/frontends/modern/features/groups/GroupModelPicker.vue
                                           ← 改造：tag 输入 + Claude 开关列 + 分列错误
web/src/frontends/modern/features/groups/GroupModelsPanel.vue
web/src/frontends/modern/features/groups/GroupModelSyncDialog.vue
web/src/frontends/modern/features/groups/GroupCreatePanel.vue
                                           ← 适配
web/src/frontends/modern/i18n/locales/*.ts ← 三语文案
```

后端零改动。`@shared` 是两个前端之间唯一合法的共享通道（`@` 指向 classic，`@modern` 指向 modern，互不引用）。

## 2. shared 纯逻辑层（新文件 `web/src/shared/models/model-aliases.ts`）

从 `classic/features/models/model-draft.ts` **平移**（非重写）以下内容，逐字保留实现与注释：

- `claudeAdapterAlias` 常量、私有 `claudeAdapterAliases`（`[1M]` 与 `[1m]` 两形态）
- `isClaudeAdapterAlias` / `hasClaudeAdapter` / `visibleAliases` / `withClaudeAdapter`
- `normalizeAliases(values, id)`（与服务端 `normalizeModelAliases` 同规则）
- `ModelNameConflict` 接口、`findModelNameConflicts` / `indexesWithConflicts` / `indexesWithEmptyIDs` / `modelDraftValidity`

依赖约束：该文件只允许 import 类型（或零依赖）。classic 版 `findModelNameConflicts` 依赖 `GroupModelUpdateDto`（`{id, aliases}` 子集），shared 版参数类型就地声明为最小结构 `{ id: string; aliases?: string[] }`，不 import classic 的 api 类型。

classic 侧 `model-draft.ts` 删除上述实现，改为：

```ts
export {
  claudeAdapterAlias, isClaudeAdapterAlias, hasClaudeAdapter, visibleAliases,
  withClaudeAdapter, normalizeAliases, findModelNameConflicts,
  indexesWithConflicts, indexesWithEmptyIDs, modelDraftValidity,
} from '@shared/models/model-aliases'
export type { ModelNameConflict } from '@shared/models/model-aliases'
```

调用点（GroupModelsTab、NewGroupImport、model-diff、GroupTestFields 等）零改动——它们继续从 `@/features/models/model-draft` 导入。

## 3. modern 公共组件 `components/ui/AppTagInput.vue`

交互逐项对齐 classic 的 `TagInput.vue`（同源交互，两端口径一致）：

- Props：`modelValue: string[]`、`label`、`placeholder?`、`disabled?`、`invalid?`、`describedBy?`、`removeLabel: (tag: string) => string`、`separators?: readonly string[]`（默认 `[',', '，', '、']`）
- emit：`update:modelValue`、`blur`
- 行为：Enter 提交；输入含分隔符即时切分提交且分隔符不入框；输入框空时 Backspace 删末项（不回填文本）；粘贴含分隔符时 `preventDefault` 并切分（否则放行默认粘贴）；失焦先提交残留文本再 emit blur；追加时 trim、去空、对自身去重
- `defineExpose({ focus })`
- 视觉：modern token（`--modern-border`、`--modern-radius-control`、`--modern-control-sm`、`--modern-danger` 等，具体以 `styles/tokens.css` 实际定义名为准）；焦点态用 `:focus-within`；组件内允许原生 `input`/`button`（check-modern-styles 只约束 `features/` 目录）
- 断点：小屏增高触控目标时经 `app/breakpoints.ts` 的常量生成 media query，不写裸像素断点（若脚本对断点来源有校验）
- 注册进 `components/ui/index.ts`

## 4. modern 草稿形状改造

`api/group-create.ts`：

```ts
export interface ModelDraft {
  id: string
  aliases: string[]
}
```

- 删除 `readModelAliases`（逗号拆分是上次合并的过渡方案，本任务移除）
- `GroupCreateRequest.models: { id: string; aliases: string[] }[]`（保持）

`api/group-detail.ts`：`saveGroupModels` 参数改为 `readonly { id: string; aliases: string[] }[]`，请求体直接发送 `{ id: model.id.trim(), aliases: model.aliases }`；`getGroupModels` 的 `GroupModel`（含 `aliases`/`clientModels`）保持不变。

## 5. `group-create-rules.ts` 重写（modern 的规则层）

```ts
export interface GroupDraftModel extends ModelDraft {
  key: number
  origin: 'manual' | 'discovery' | 'configured'
}
```

- `modelErrors(models)`：改用 `findModelNameConflicts(models)` + `indexesWithEmptyIDs`，返回按行 key 的错误与列归属：

```ts
export type ModelColumn = 'id' | 'alias' | 'claude'
export interface ModelDraftError { column: ModelColumn; name: string }  // name 为冲突名，id 错误时为空
export function modelErrors(models: readonly GroupDraftModel[]): Map<number, ModelDraftError>
```

列归属规则与 classic `ModelAliasEditor` 一致：空 ID → `id`；冲突名等于该行 ID → `id`；冲突名是 Claude 适配常量 → `claude`；其余 → `alias`。一行只报首个命中。

- `setClaudeAdapter(models, key, enabled)`：互斥写回。开启时该行 `withClaudeAdapter(aliases, true)`、其余行 `withClaudeAdapter(aliases, false)`；关闭只影响自身。放规则层而不是组件内，便于两处（编辑/创建）复用同一语义。
- `setAliases(models, key, aliases)`：`normalizeAliases(withClaudeAdapter(aliases, hasClaudeAdapter(当前行)), id)`——先把开关状态合并回可见列表再规范化，避免编辑普通别名时丢掉隐藏的 `claude-*[1m]`。

## 6. `GroupModelPicker.vue` 改造

- 行布局从「ID + 别名文本框 + 删除」三列扩为「ID + 别名 TagInput + Claude 开关 + 删除」四列；表头 `modern-create-model-labels` 同步加开关列；`grid-template-columns` 相应调整（参照 classic：别名列最宽、开关列定宽）。
- 别名列：`AppTagInput`，model-value 为 `visibleAliases(model.aliases)`，update 时走 `setAliases` 规则函数。
- 开关列：`AppSwitch`，model-value 为 `hasClaudeAdapter(model.aliases)`，update 走 `setClaudeAdapter`。
- 搜索过滤：`[model.id, visibleAliases(model.aliases).join(' ')].join(' ')`。
- 错误显示：按 `modelErrors` 的 column 分别挂到 ID 输入 / TagInput / 开关；`focusFirstInvalid` 按 column 聚焦对应控件（ID 输入、TagInput 的 expose focus、开关元素）。
- `update(key, field, value)` 改为 `updateRow(key, patch: Partial<Pick<ModelDraft,'id'|'aliases'>>)`。
- `nextModel` 产出行时 `aliases: []`。

## 7. 三个入口适配

- **GroupModelsPanel**：draft 映射 `aliases: model.aliases`（服务端已含 `claude-*[1m]`，开关由其推导）；`signature()` 序列化 `{id, aliases}`；保存传 draft 原样。
- **GroupModelSyncDialog**：`next` 合并时保留每行 aliases（增补行 `aliases: []`）；`conflicts` 改用 `findModelNameConflicts(next)` 输出的 `client_model` 名称列表；removals 展示名 `visibleAliases(model.aliases)[0] ?? model.id`。
- **GroupCreatePanel**：`request()` 里 `models: models.value.map(m => ({ id: m.id.trim(), aliases: m.aliases }))`；移除 `readModelAliases` 引用。

## 8. i18n（modern 三语）

`i18n/locales/group-create.ts`（创建 + 通用模型文案）与 `group-detail.ts`（编辑面板）按现有结构追加，语义对齐 classic zh-CN：

- `aliasPlaceholder`：输入别名后按回车添加（替换现有逗号分隔占位文案）
- `removeAlias: '移除别名 {alias}'`
- `claudeAdapter: 'Claude 适配'`、`claudeAdapterFor: '为 {id} 启用 Claude 适配'`、`claudeAdapterConflict: '本分组内已有模型启用 Claude 适配，同一分组只能启用一个'`
- 冲突文案沿用现有 `modelConflict`，改为带名称：`对外名称“{name}”重复`

en-US / ja-JP 同步翻译。classic 文案不动。

## 9. 兼容性与回滚

- 数据兼容：草稿只是前端内存形状，不落盘；服务端契约不变。
- 回滚：纯前端变更，`git revert` 单提交即可整体回退。
- classic 回归面：`model-draft.ts` 的 re-export 改造影响 classic 全部模型编辑入口，阶段 1 完成后立即跑 type-check 锁定。

## 10. 权衡记录

- **为什么不把 classic 的 `ModelAliasEditor` 整体提升到 shared**：它深度耦合 classic 的 UI 库（LedgerRecordList/AppSwitch/CompactFieldError），提升意味着把整套视觉体系搬进 shared，违背 #668 双前端各自的视觉体系。只提纯逻辑，UI 各自实现。
- **为什么开关互斥做成自动关闭而非报错**：后端同名冲突规则依然成立，但强制用户先手工关掉另一个再开这一个是纯摩擦；与 classic `setClaudeAdapter` 行为保持一致。
