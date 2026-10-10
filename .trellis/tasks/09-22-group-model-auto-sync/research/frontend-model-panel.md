# Research: 模型与别名面板（modern）前端调研

- **Query**: 分组详情「模型与别名」抽屉的新分组级自动同步开关、同步新增模型的 `claude-<模型ID>` 别名、抽屉加宽、i18n/测试/URL 状态约定
- **Scope**: mixed（modern 前端为主，附后端接口与契约约束）
- **Date**: 2026-09-22
- **任务**: `.trellis/tasks/09-22-group-model-auto-sync`（PRD 见 `prd.md`）

---

## 0. 结论速览

| 问题 | 结论（证据见下文分节） |
|---|---|
| 开关保存通道 | 两条现成通道（models PUT / settings PUT）都要动后端；且**任何塞进现有响应结构的新字段都会强制改 classic 契约**（§1.4、§1.5） |
| 工具栏加开关 | 改 `GroupModelPicker.vue` 的 `#actions` 插槽消费方（`GroupModelsPanel.vue:173-184`）即可，组件本身不用改；`AppSwitch` **没有可见文案**，必须自己排一个 `<span>`（§2） |
| `claude-<id>` 别名落点 | `GroupModelSyncDialog.vue:57-69` 的 `next`，`aliases: []` 在第 64 行；`normalizeAliases`/`visibleAliases` 都不会吃掉它（§3） |
| 加宽抽屉 | `--modern-dialog-sheet-width` 被 3 个面板共用；只加宽该面板需要三层 props/union 打通或新增 token（§4） |
| i18n | 三语同文件、靠 `enUS: typeof zhCN` 类型约束强制（type-check），无校验测试（§5） |
| 前端测试 | 没有 vitest/组件测试，`web/README.md:55` 明确「不增加前端测试」；用 lint/format/type-check/build + `make check`（§6） |
| 开关是否进 URL | 不需要。URL 状态只放展示态（`url-state.ts:10`），持久化配置不属于此列（§7） |

---

## 1. `GroupModelsPanel.vue` 数据流与保存通道

文件：`web/src/frontends/modern/features/groups/GroupModelsPanel.vue`（195 行）

### 1.1 draft / baseline / dirty

| 位置 | 内容 |
|---|---|
| `:24-25` | props `{ group: GroupRow; channel?: GroupChannel }`；emit `close` / `saved` |
| `:29-32` | `useQuery` 读 `groupModelsKey(group.id)` → `getGroupModels` |
| `:33-35` | `draft = ref<GroupDraftModel[]>([])`、`baseline = ref('')`、`initialized = ref(false)` |
| `:57` | `signature()` = `JSON.stringify(draft.map(({id, aliases}) => ({id, aliases})))` —— **只有 id 与 aliases 进脏检查**，`key`/`origin` 是纯本地字段 |
| `:58` | `dirty = initialized && signature() !== baseline` |
| `:59-73` | `watch(query.data, ...)`：`if (!models || dirty || saving) return` → 用 `models.map((model, key) => ({key, id, aliases, origin: 'configured'}))` 重建 draft，写 `baseline`，置 `initialized`；`{ immediate: true }` |
| `:74-82` | `prices` 从 `query.data` 派生 `pricingStatus`，传给 picker 做价格徽标 |
| `:36` `:108-109` `:122-124` | `saving` 期间的写回保护与 finally 复位 |

要点：**服务端返回的 `aliases` 就是唯一真源**（`:56` 注释：「Claude 适配开关状态完全由它推导，不另存字段」）。任何新增开关若被塞进 `signature()`，就会进入草稿与脏检查；PRD R1/AC3 明确要求开关不进草稿。

### 1.2 `save()` 调用链

`save()` 在 `:101-125`：

1. `:102-107` 守卫：`!dirty || saving` 直接返回；`attempted = true`；`modelErrors(draft).size` 非空时 `picker.focusFirstInvalid()` 并中止（**校验失败 → 整次保存失败**，这正是 PRD 不把开关绑在草稿上的原因）。
2. `:108-110` `saving = true`、清 error、`cancelDiscovery()`。
3. `:112` `cache.cancelQueries({ queryKey: groupModelsKey(group.id) })`。
4. `:113` `await saveGroupModels(client, group.id, draft.value, controller.signal)`。
5. `:115-119` `baseline = signature()`；`cache.setQueryData(groupModelsKey(...), result)`；invalidate `['modern','group-model-names', id]`；`emit('saved')`、`emit('close')`。
6. `:120-124` 任何异常 → `error = t('groups.edit.saveFailed')`（**没有识别 `MODEL_NAME_CONFLICT`**，现代端无该错误码分支；对比 classic 有：`web/src/frontends/classic/features/groups/models/GroupModelsTab.vue:474`）。

调用方：`GroupDetailView.vue:345-352`（`v-if="panel === 'models'"`，`@saved="modelsSaved"` → `GroupDetailView.vue:168-171` invalidate `groupQueryKey` / `groupCredentialsKey`）。

同步对话框路径：`syncModels(models)` 在 `:126-131` —— `if (saving || dirty) return; draft = models; syncing = false; await save()`。即「同步模型」不是独立接口，它只是**改写 draft 后复用同一条保存链**。

### 1.3 `saveGroupModels` 的请求体与响应结构

文件：`web/src/frontends/modern/api/group-detail.ts`

- `:163-178` `saveGroupModels(client, id, models, signal)` → `PUT /api/groups/${id}/models`，body：

```ts
json: { models: models.map((model) => ({ id: model.id.trim(), aliases: model.aliases })) }
```

  **注意：前端不做别名规范化**（只 trim id），`aliases` 原样送上；服务端再规范化（§3.4）。

- `:143-148` `GroupModel = { id, aliases, clientModels, pricingStatus }`（`GroupModel` 类型，注意与后端同名结构不同）。
- `:149-159` `readModels`：读 `record(value).items` → `{ id, aliases, client_models, pricing_status }`；响应结构硬断言，多余/缺失字段会抛 `InvalidResponseError`。
- `:160-162` `getGroupModels` / `:179-184` `discoverGroupModels`（`POST /groups/:id/models/discover` → `data.models`）。
- `:10-12` 缓存键：`groupSettingsKey`、`groupModelsKey`、`groupCredentialsKey`（前缀 `['modern', ...]`）。

后端同构事实：`internal/control/group_models.go:16-27`（请求 `{models}`、响应 `{items,total,pending?…}`、`GroupModelResponse{id,aliases,client_models,pricing_status}`），路由 `internal/control/http_routes.go:244-253`（GET/PUT `/groups/:group_id/models`）、`:436-444`（discover）。

### 1.4 分组级开关应该走哪条保存通道

#### 通道 A：跟随 `saveGroupModels`（models PUT）

- 请求体目前只有 `models` 一个键；后端用**严格 JSON 解码**：`internal/control/server.go:1030-1040` + `internal/control/strict_json.go:11-31`（`decoder.DisallowUnknownFields()`）。
  → 直接多送一个 `auto_sync: true` 会被 400 拒绝，**后端必须改 `GroupModelsUpdateRequest`**。
- 若再把状态放进响应（`GroupModelsResponse` / `GroupModelResponse`，`internal/control/group_models.go:20-31`），会命中 Go 契约测试：`internal/control/classic_api_contract_test.go:39-40` 要求这两个结构的每个导出 JSON 字段都出现在 classic 白名单数组 `web/src/frontends/classic/app/resources/groups.ts:77-78`。
- 语义冲突：PRD R1 要求开关**即时保存、不进草稿、不因模型校验失败回滚**；而这条通道的 `save()` 被 `dirty` 与 `modelErrors` 双重守卫（`:102-107`），把开关并进来就必须拆开这两条语义（要么开关单独发一次只带开关的 PUT，要么开关自己也变脏）。

#### 通道 B：走分组 settings（`PUT /api/groups/:id/settings`）

- 现成读写 API 在同文件：`:129-131` `getGroupSettings`、`:132-141` `saveGroupSettings`（`patch: AdvancedSettingsPatch`）；`AdvancedSettingsPatch` 定义在 `:68-74`：`params / validation_model / validation_protocol / overrides / proxy` —— **没有分组级布尔字段的落点**。
- 顶层新字段（如 `auto_sync_enabled`）需要改 `GroupSettingsUpdateRequest`（后端 `internal/control/group_settings.go:40-50`，同样是严格解码），并出现在 `GroupSettingsResponse`（`:24-38`）里 → 命中 `classic_api_contract_test.go:37`（`groupSettingsFields`，classic 数组在 `groups.ts:62-76`）。
- 另一条路是把开关做成 group 可覆盖的 runtime setting，放进 `overrides`：
  - 后端 group 覆盖有**白名单**：`internal/state/runtime_settings.go:253-308`（`default: unknown group setting`）；新增键要加进 `IsRuntimeSettingKey`（`internal/state/runtime_settings.go:102-124`）与 group 解析分支，`config.Settings` 是 `map[string]any`（`internal/platform/config/config.go:127`），所以不会改响应结构体。
  - **但 classic 会拒绝未知键**：classic 读分组 settings 时对 `overrides` / `effective` 做字段白名单断言，`web/src/frontends/classic/app/resources/groups.ts:349-376` + `:433-434`，落点是 `web/src/frontends/classic/app/resources/projector.ts:134-144`（不在白名单 → `invalidResponse()` 抛错，整个 classic 分组设置页读不出来）。白名单数组：`groups.ts:114-123`（`runtimeSettingFields` / `groupRuntimeSettingFields`）。→ **仍要动 classic 文件**。
  - 现代端还有一处**丢键风险**：`readRuntime`（`api/group-detail.ts:78-102`）只读 `runtimeNumbers`（`:34-39`）与 `runtimeSwitches`（`:40`）里的键，服务端多返回的键会被丢掉；而 `GroupAdvancedPanel.vue:207-226` 保存时以「服务端返回的 overrides 深拷贝」为基线重建 `overrides`，丢掉的键下次保存即被删除。同时 `readSettings`（`:103-128`）要求 `effective` 必须逐个含 `runtimeNumbers + runtimeSwitches + header_rules`（`:107-112`），缺一即 `InvalidResponseError`。
- 现成 UI 范式（若选通道 B）：
  - `GroupAdvancedPanel.vue`：`useQuery(groupSettingsKey)`（`:48-51`）→ 本地 ref 快照 + `snapshot()` 脏检查（`:72-89`）→ 构造最小 patch（`:227-243`）→ `saveGroupSettings`（`:251-255`）→ `cache.setQueryData(groupSettingsKey, result)` + `emit('saved'/'close')`；`useMessageSource`（`:265`）报错。
  - `GroupBasicsForm.vue`：**最接近「开关即时保存」的范式** —— `AppSwitch` 行在 `:221-224`，`save()` 在 `:122-162`（最小 patch `:132-138`，`updateGroupBasics` 于 `:147`，成功后 `accept(result)` + 合并进 `groupSettingsKey` 缓存 `:150-153`，成功提示 `:155-156`/`:166-168`）。注意它是 `name/weight/price_multiplier/enabled` 通道（`api/groups.ts:66-71` `GroupBasicsPatch`、`:184-197` `updateGroupBasics`），**不是** `AdvancedSettingsPatch`。
  - 配置类编辑器的目录是 `features/config/`（只有 `ParameterRulesEditor.vue` 与 `parameter-rule-draft.ts`），没有可直接复用的「分组开关」组件。

#### 通道 C：新增 modern 专属端点（证据支持，供取舍）

- 现代端已有先例：`api/group-detail.ts:301` 用 `/api/modern/groups/:id/credentials`；`web/README.md:39` 明确「新版页面可以独立设计展示 DTO、聚合查询及管理接口」。
- Go 契约测试只枚举**既有共享 DTO**（`classic_api_contract_test.go:17-141`），新端点既不在 classic 读取路径上，也不进该清单 → **可以做到完全不改 classic**（PRD Constraints：只改 modern）。

### 1.5 跨层硬约束（选通道时最先看这条）

`internal/control/classic_api_contract_test.go:17-141`：对 `GroupSettingsResponse`(`:37`)、`GroupEffectiveConfigResponse`(`:38`)、`GroupModelsResponse`(`:39`)、`GroupModelResponse`(`:40`)、`GroupSummaryResponse`(`:36`)、`GroupCollection*`(`:31-35`) 等结构，**每个导出 JSON 字段都必须出现在 classic 的对应白名单数组里，否则 Go 测试失败**。加上 classic 运行时对 `overrides`/`effective` 未知键直接判非法响应（§1.4 通道 B），结论是：

> 只要开关状态以「现有响应结构里的新字段」形式暴露，就必须同时改 classic（白名单数组或 `groups.ts:114-123`），与 PRD「classic 不动」冲突。要规避，只能选「新端点」或「复用已有 map 且同步改 classic 白名单」两类写法之一。

---

## 2. 工具栏与 `AppSwitch`

### 2.1 `actions` 插槽与工具条布局

文件：`web/src/frontends/modern/features/groups/GroupModelPicker.vue`

- 插槽消费点在 `GroupModelsPanel.vue:173-184`：`<template #actions>` 内是「同步模型」按钮（`:174-183`，`v-if="channel?.discovery"`、`:179` 的 disabled 条件为 `!initialized || dirty || saving || discovering`、`:181` `@click="syncing = true"`）。
- 插槽落点是 `GroupModelPicker.vue:260-274` 的 `.modern-create-model-tools`，顺序：`选择模型`（`:261-269`）→ `<slot name="actions" />`（`:270`）→ `手动添加`（`:271-273`）。**新开关放在插槽内部、`同步模型` 之后即满足 PRD「紧邻同步模型、位于手动添加左侧」**（插槽内顺序由面板决定）。
- 布局 CSS：`.modern-create-model-heading, .modern-create-model-tools`（`:429-434`，`display:flex; align-items:center; gap: var(--modern-space-2)`）、`.modern-create-model-tools`（`:455-460`，`flex:0 1 auto; margin-left:auto; justify-content:flex-end; flex-wrap:wrap`）、列表布局覆盖（`:461-473`，`grid-template-columns: minmax(0,1fr) max-content`，tools 改成 `nowrap`）、容器查询降级（`:474-481`，容器名 `modern-workspace-panel` 宽 ≤460px 时换行/单列）、移动断点（`:539-546`，760px 时改用 `--modern-touch-target`）。
- 容器名来源：`GroupWorkspacePanel.vue:72`（`container: modern-workspace-panel / inline-size`）挂在面板 body 上。

要点：工具栏宽度是「选择模型 + 同步模型 + (新开关) + 手动添加」四件套，`nowrap` 只在 >460px 容器宽度下成立；加一个带文字的开关会明显挤压（见 §4.4）。

### 2.2 `AppSwitch` 组件契约

文件：`web/src/frontends/modern/components/ui/AppSwitch.vue`

- props（`:9-18`）：`modelValue: boolean`（必填）、`label: string`（必填）、`disabled?`、`loading?`、`size?: 'xxs' | 'sm' | 'md'`（默认 `md`）；emits `update:modelValue`（`:19`）。支持 `v-model`。
- **`label` 不是可见文案**：`<AppTooltip :label="label">`（`:23`）把它当 tooltip，同时作为 `aria-label`（`:31`）。组件模板只渲染轨道（`:37-39`），**没有文本节点**。
- 因此「带可见文本标签的开关」必须由调用方自己排一个文本元素；同尺寸选项下开关高度由 `--modern-control-*` 控制（`:67-76`），工具栏里与 `size="sm"` 按钮同排时可选 `sm` / `xxs`（`:65-66` 注释：xxs 只降容器高度用于与 xxs 按钮对齐）。
- 导出位置：`web/src/frontends/modern/components/ui/index.ts:37`。

### 2.3 Claude 适配开关那列的用法（交互范式）

`GroupModelPicker.vue:347-368`（列容器 `.modern-create-model-claude`）：

```vue
<AppSwitch
  :model-value="hasClaudeAdapter(model.aliases)"
  :label="t('groupCreate.claudeAdapterFor', { id: model.id || t('groupCreate.modelID') })"
  size="sm"
  :disabled="disabled"
  @update:model-value="models = setClaudeAdapter(models, model.key, $event)"
/>
```

即：**状态由数据推导 + 单向 `:model-value` + 事件回写**（不是 `v-model` 到局部 ref）。列 CSS 在 `:499-509`（含 `min-width: max-content`），列头文案 `:296`。

### 2.4 带可见文案的开关既有范式

| 范式 | 位置 | 形态 |
|---|---|---|
| 行内开关 + 左文本 | `GroupBasicsForm.vue:221-224` | `<div class="modern-group-settings-enabled"><span>{{ t(...) }}</span><AppSwitch v-model=... :label=... /></div>`，CSS 在 `:303-310`（`justify-content: space-between`） |
| 带 label 的条目容器 | `features/settings/SettingItem.vue:1-45`（`label` / `controlId` / `hint` 等 props）+ 用法 `SettingsView.vue:615-627` | 用 `<label :for="controlId">` 与控件 `id` 关联，适合表单页；工具栏里过重 |
| 纯图标开关 | `GroupListRow.vue:272-278`、`AccessKeysView.vue:538-545` | 无可见文案，靠 `label` 做 tooltip/aria |

对本次「工具栏内、紧邻按钮、需要可见文案」：`GroupBasicsForm` 的 `span + AppSwitch` 是最贴近的既有写法，但需要一个新 class（例如沿用 `--modern-space-2` 间距）。

### 2.5 样式与模板硬约束（会拦住随手写法）

`web/scripts/check-modern-styles.mjs`（`pnpm run check:styles`，被 `lint` 调用，`web/package.json:14-15`）：

- `:16-48` 业务页面（`features/**`）不得直接写原生 `button/input/select/textarea/svg`，必须用 `components/ui` 组件；鼠标提示必须用 `AppTooltip`。
- `:114-136` 颜色/视觉属性必须取 `--modern-*` 变量；`margin|padding|gap|border|outline|inset|scroll-margin|scroll-padding` 出现裸 `px/rem/...` 即报错（`width/height` 不在该正则内）；时长必须用变量。
- `:138-146` 媒体查询断点只允许 `web/src/frontends/modern/app/breakpoints.ts:2-7` 的 `420 / 760 / 1150 / 1700`。
- ESLint：`web/eslint.config.mjs:19-21` 禁止 modern `.vue` 里静态内联样式；`:41-68` 禁止 `features/layouts` 直接 import reka-ui 的浮层/菜单/提示原语；`:22-40` 禁止 modern 代码写 `refetchInterval` 等轮询选项（**「后端定时同步」不要在前端做任何轮询**）。

---

## 3. 同步合并逻辑与 `claude-<id>` 别名

### 3.1 `GroupModelSyncDialog.vue` 的 `next` 与要动的行

文件：`web/src/frontends/modern/features/groups/GroupModelSyncDialog.vue`

| 位置 | 内容 |
|---|---|
| `:14-15` | props `{ groupId, models: readonly GroupDraftModel[] }`，emit `close` / `confirm(models)` |
| `:18` | `current = props.models.map((m) => ({ ...m }))`（浅拷贝行，aliases 数组仍是**同一引用**） |
| `:22-34` | `sync_mode` URL 状态（full/add/cleanup），`view` → `mode` 双向绑定 |
| `:43-45` | `live` = discovery 里 `sources.includes('live')` 的候选 |
| `:46-50` | `additions` = 非 cleanup 模式下 `live` 中「分组内没有同 id」的候选 |
| `:51-55` | `removals` = 非 add 模式下 `current` 中「live 里没有」的行（匹配用 `item.id === model.id.trim()`） |
| `:56` | 注释：合并结果保留既有别名（含隐藏的 `claude-*[1m]`），增补行无别名 |
| `:57-69` | `next` computed：过滤掉 removals，再追加 additions；`key` 从 `Math.max(-1, ...current.key) + 1` 起自增 |
| **`:64`** | **`aliases: []`** ← 自动别名要改的就是这一行（同一对象字面量 `:62-67` 内） |
| `:70-73` | `conflicts = findModelNameConflicts(next).map(c => c.client_model)`；`:101-103` 有冲突时 `disabled` → 禁用「确认同步」；`:104` `tone` 视 removals 取 danger |
| `:106` | `@confirm="emit('confirm', next)"` → 回到面板 `syncModels`（§1.2） |
| `:135` | removals 列表用 `visibleAliases(model.aliases)[0] ?? model.id` 作显示名 |
| `:140-142` | 冲突提示 `t('groupWorkflows.modelConflicts') + conflicts.join('、')`（分隔符是硬编码的「、」，不随语言变化） |

`GroupDraftModel` 形状：`features/groups/group-create-rules.ts:11-14`（`ModelDraft & { key: number; origin: 'manual' | 'discovery' | 'configured' }`）。

### 3.2 `shared/models/model-aliases.ts` 各函数语义与调用约束

文件：`web/src/shared/models/model-aliases.ts`（现代端经 `@shared/models/model-aliases` 引入，classic 原地 re-export；spec 规定「前端别名纯逻辑只有这一份实现」`.trellis/spec/backend/model-name-contract.md:235-245`）

| 函数 | 行 | 语义 / 对本次的约束 |
|---|---|---|
| `claudeAdapterAlias` | `:21` | 常量 `'claude-*[1m]'`；`:29` 另认大写 `claude-*[1M]` |
| `isClaudeAdapterAlias` | `:32-34` | 只认这两个常量字符串 |
| `hasClaudeAdapter` | `:37-39` | `aliases.some(isClaudeAdapterAlias)` —— **`claude-<id>` 这种字面别名不会让 Claude 适配开关变亮**（两套东西互不干扰，与 PRD 背景描述一致） |
| `visibleAliases` | `:47-49` | 只过滤掉 Claude 适配常量；普通通配符与 `claude-<id>` 都会**照常显示在别名输入框**（`:44-45` 注释说明刻意不隐藏通配符） |
| `withClaudeAdapter` | `:58-66` | 开启时补齐常量（已有大写形态则保留，不制造重复）、关闭时移除两种形态；**始终返回新数组**（`:52-57` 注释：就地改写会污染脏检查基线） |
| `normalizeAliases` | `:75-86` | 逐项 `trim`、丢弃空项与**等于模型 ID**的项、按首次出现去重（大小写敏感）。→ `claude-<id>` 只会在 `id === 'claude-' + id` 时被丢弃（不可能），**不会被过滤**；但生成别名前若 id 带空白，trim 后可能与别名不一致（服务端也 trim id：`saveGroupModels` `:174`） |
| `findModelNameConflicts` | `:94-130` | 名称 = `[id, ...aliases]` 全部规范化后的并集；**跨上游 ID 认领同名才算冲突**，同一上游 ID 的多行可重复认领（`:101-104` 注释）；返回 `{client_model, indexes}` |
| `indexesWithConflicts` / `indexesWithEmptyIDs` / `modelDraftValidity` | `:132-155` | 冲突/空 ID 的下标集合与汇总 |

调用方（会被别名自动生成间接影响）：

- `GroupModelPicker.vue:99`（`modelErrors`）与 `:114-121`（列归属）：`group-create-rules.ts:30-48` —— 冲突名 **等于行 ID** 归 ID 列，**是 Claude 适配常量**归开关列，**其余归别名列**；一行只报首个错误。
- `GroupModelPicker.vue:327` 别名框 `:model-value="visibleAliases(model.aliases)"`、`:336` 回写 `setAliases(models, key, $event)`（`group-create-rules.ts:78-97`：与当前状态 OR 推导开关 + `normalizeAliases` 收尾）→ **自动生成的 `claude-<id>` 会显示在别名框里，用户可手动删除**（属预期行为，需在验收时明确）。

### 3.3 自动别名 × 冲突检测（关键风险）

- 现在 `next` 里新增行 `aliases: []`，新行只认领自己的 `id`；改为 `aliases: ['claude-' + id]` 后，新行额外认领一个名称。
- 前端冲突口径 `findModelNameConflicts`（`:94-130`）与后端写路径 `normalizeGroupModels`（`internal/control/group_write.go:218-276`）**不完全一致**：后端用 `state.RoutableModelNames`（`internal/state/snapshot.go:115-135`），**带上下文后缀的名字会额外认领其基名**（`[1M]`/`[1m]`，`internal/modelname/modelname.go:14,20-30`）。前端不建模这个派生（`.trellis/spec/backend/model-name-contract.md:104-126` 明确三处检测**故意不要求一致**；读取路径用 External 集合、写路径用 Routable 集合）。
  → 因此自动生成别名可能引出两类新情况：
  1. 对话框预检通过、`saveGroupModels` 被后端以 `MODEL_NAME_CONFLICT` 拒绝（例如新别名 `claude-foo[1m]` 与另一行 id `claude-foo`；或 id 本身带 `[1M]` 后缀）。此时现代面板只显示通用 `t('groups.edit.saveFailed')`（`GroupModelsPanel.vue:121`）。
  2. 预检直接报冲突 → `:101-103` 禁用「确认同步」，用户原先能完成的一次同步被卡住（须先手工清理冲突名）。
- 后端对「同名跨 ID」是**拒绝保存**（`group_write.go:269-274` → `ErrModelNameConflict`）；组内合法写法（相同 ID 多行、同模式 `claude-*` 与 `claude-opus-5` 并存）不受影响（spec `model-name-contract.md:114-118`）。
- 已存在于分组内的模型不会被追溯加别名（PRD R3 不追溯），这与 `next` 只处理 additions 的结构天然一致（`removals`/既有行不参与）。

### 3.4 服务端对别名的硬约束（生成规则必须满足）

- `internal/control/group_write.go:193-216` `normalizeModelAliases`：trim、丢弃空项与等于 id 的项、大小写敏感去重、**单项 > 255 字节 → ErrValidation**、**每模型最多 10 个别名 → ErrValidation**；`group_write.go:28-29`（`maxAliasesPerModel = 10`、`maxModelNameBytes = 255`）。
- `group_write.go:218-276` `normalizeGroupModels`：模型 ID trim 后不得为空；冲突检测见上。
- spec 里已有先例配置：「alias `claude-deepseek-flash[1M]` on upstream `deepseek-flash`」是 Good case（`.trellis/spec/backend/model-name-contract.md:140-145`）；`claude-*` 通配与具体名并存也是允许的（`:118`）。说明 `claude-<id>` 前缀别名符合既有命名契约，无需新增后端别名规则。
- 前端目前**没有**任何 `claude-` 前缀拼接的既有实现（全仓 grep `claude-` 只命中常量 `claude-*[1m]` 与文案），要做的是新逻辑而非复用。

---

## 4. 抽屉宽度

### 4.1 现状：token 与分发链

- token 定义：`web/src/frontends/modern/styles/tokens.css:237-241`

```
--modern-dialog-width: 540px;
--modern-dialog-wide-width: 900px;
--modern-dialog-sheet-width: 660px;
--modern-dialog-confirm-width: 420px;
```

- 尺寸类：`web/src/frontends/modern/components/ui/AppDialogContent.vue:10`（`size?: 'default' | 'wide' | 'sheet' | 'confirm'`）、`:23`（`class="modern-dialog"` + `` `modern-dialog--${placement}` `` + `` `modern-dialog--${size}` ``）、`:62-73`（`--wide` → 900px、`--sheet` → 660px、`--confirm` → 420px）、`:81-87`（`--editor` 用 `--modern-dialog-width` 兜底，`inset: 0 0 0 auto`，即右侧全高抽屉）。
- 分发链（模型面板这条）：
  `GroupModelsPanel.vue:147` `wide` 布尔 → `GroupWorkspacePanel.vue:17` props `wide?` → `:30-36` 传 `:size="wide ? 'sheet' : 'default'"` → `GroupEditorSurface.vue:11`（`size?: 'default' | 'sheet'`）→ `:30-40` `AppDialogContent placement="editor" :size="size"`。

### 4.2 `sheet`（660px）的三个消费者

| 消费者 | 行 | 说明 |
|---|---|---|
| `GroupModelsPanel.vue:147` | `wide` | 「模型与别名」抽屉（本次目标） |
| `features/health/HealthDetailPanel.vue:81` | `size="sheet"` | 健康详情（`placement="editor"`，`:77-88`） |
| `features/models/ModelCatalogSettingsDialog.vue:92` | `size="sheet"` | 模型目录设置（`placement="editor"`，`:89-93`） |

补充：其它 `GroupWorkspacePanel` 使用方（`CredentialDetailPanel.vue:145`、`GroupAdvancedPanel.vue:269`、`GroupCredentialAddPanel.vue:95`）**都不传 `wide`** → 走 `default`（540px）；`GroupCreatePanel.vue:535-542` 直接用 `GroupEditorSurface`（不传 size → `default`）。`size="wide"`（900px）当前只被 `features/models/ModelSelectionDialog.vue:261` 使用。

### 4.3 只加宽该面板的可选做法与影响面

| 做法 | 改动点 | 影响面 |
|---|---|---|
| **A. 直接改共享 token** `tokens.css:239` 660px → 例如 820px | 1 行 | 三个 `sheet` 面板一起变宽（健康详情、模型目录设置也变），违反 PRD R4「只加宽该抽屉」 |
| **B. 新增尺寸档**（推荐路径，需打通三层） | ① `AppDialogContent.vue:10` union 加档（如 `'sheet-wide'`）+ 新类（`:65-67` 旁）用**新 token**（`tokens.css:239` 旁新增，如 `--modern-dialog-sheet-wide-width: 820px`）；② `GroupEditorSurface.vue:11` union 同步加档；③ `GroupWorkspacePanel.vue:17` 的 `wide?: boolean` 需换成可选枚举或再加一个 prop（如 `size?: 'default' \| 'sheet' \| 'sheet-wide'`），并在 `:32` 透传；④ `GroupModelsPanel.vue:147` 传新档 | 只影响显式使用新档的面板；类型是 union，编译期能查漏（`vue-tsc`）。缺点是要动 4 个共享文件的公共类型 |
| **C. 面板侧覆盖宽度**（不改共享 props/union） | 需要让自定义 class 落到 portal 里的 `.modern-dialog` 元素上。`AppDialogContent.vue:4` 是 `inheritAttrs: false` + `v-bind="$attrs"`（`:21`），class 会与固定 class 合并；但上游 `GroupEditorSurface.vue` 根节点是 `DialogRoot`（`:22`），`GroupWorkspacePanel.vue:30-36` 也是单根组件链，透传是否落到 `.modern-dialog` 需要实测；且 portal 内容不带 scoped 属性（`AppDialogContent.vue:33-34` 注释），面板里要写**非 scoped 全局样式块**并用足够特异的选择器 | 改动文件最少（1-2 个），但依赖 attrs 透传链路；回归风险集中在「是否真的命中 portal 元素」，且会绕过 token（`check:styles` 不拦 `width` 裸 px，但违反 token 惯例） |

其它可加宽相关事实：面板 body 自带容器查询（`GroupWorkspacePanel.vue:72` 定义 `modern-workspace-panel`），`GroupModelPicker.vue:474-481`（460px）与 `GroupAdvancedPanel.vue:481-485`（440px）按它降级；`GroupModelsPanel` 还带 `fill`（`:148`）→ `GroupWorkspacePanel.vue:84-90` `.is-filled`（`overflow: hidden`，列表内部滚动）。

### 4.4 加宽的连带效果（需在实现时确认）

- 容器宽度越过 460px 后，`GroupModelPicker.vue:474-481` 的单列降级不再触发；工具栏保持 `nowrap`（`:470-473`），此时「选择模型 + 同步模型 + 自动同步开关 + 手动添加」需要在 ≤ 新宽度内排开，否则溢出（工具条是 `flex-wrap: wrap` 被 `nowrap` 覆盖）。
- 三列表格列宽定义在 `GroupModelPicker.vue:482-488`（`minmax(0,1.1fr) minmax(0,1.4fr) max-content var(--modern-control-sm)`），加宽直接增益别名列与 ID 列。

---

## 5. i18n 规范

- 组织方式：每个领域一个文件、**同一文件里三个导出** —— `zhCN` / `enUS: typeof zhCN` / `jaJP: typeof zhCN`（例：`i18n/locales/group-workflows.ts:1,32,66`；`group-detail.ts:1,135,276`；`group-create.ts:1,71,148`）。汇总入口 `i18n/index.ts:5-15`（`messages: { 'zh-CN': zhCN, 'en-US': enUS, 'ja-JP': jaJP }`，`fallbackLocale: 'en-US'`）。
- **强制机制是类型而非测试**：`enUS: typeof zhCN` 让缺键/多键在 `pnpm run type-check`（`vue-tsc --noEmit`，`web/package.json:18`）阶段报错；仓库内没有任何 i18n 校验测试或 lint 规则（`web/eslint/` 只有 `frontend-boundaries.mjs`；`vue-i18n` 未做 `DefineLocaleMessage` 强类型，`find web/src -name "*.d.ts"` 仅 `env.d.ts`）。
- 结论：**三语必须同时维护**（PRD Constraints 亦如此要求），同一文件的三个对象一起改。
- 本次新文案的自然落点：`group-workflows.ts`（同步相关键 `syncModels:`14/:45/:78、`syncDescription`、`syncMode`、`syncModes` 等都在此），或 `group-detail.ts`（面板标题类 `modelsAndAliases` 在 `:41/:180/:316`）；开关的无障碍 label 建议与可见文案共用同一个 key（`AppSwitch` 的 `label` 同时是 tooltip 与 `aria-label`，`AppSwitch.vue:23,31`）。
- 既有事实（实现时不要误改）：冲突提示里的顿号是硬编码（`GroupModelSyncDialog.vue:141`）。

---

## 6. 前端测试与校验

- `web/package.json:11-19` scripts：`dev` / `build`（先 `type-check`）/ `lint`（`eslint . --max-warnings=0 && check:styles`）/ `check:styles`（`node scripts/check-modern-styles.mjs`）/ `format`（`prettier --check .`）/ `format:write` / `type-check`（`vue-tsc --noEmit -p tsconfig.app.json && tsc --noEmit -p tsconfig.node.json`）。
- **没有组件测试框架**：`find web/src -name "*.test.ts" -o -name "*.spec.ts"` 无结果；`package.json` 无 vitest/jest；`web/vite.config.ts`、`eslint.config.mjs` 无测试配置。
- `web/README.md:53-55`（「验证」节）明确：「前端使用现有 lint、format、type-check 和 build；最终运行仓库 `make check`。**不增加前端测试**、浏览器验收或本地 race。」
- 仓库根 `Makefile:32-42` 的 `check`：gofmt 检查 → `go mod tidy -diff` → `go vet ./...` → `pnpm --dir web run lint` → `format` → `build` → `go build` → `go test -count=1 . ./internal/...` → `git diff --check`。
- 与本次相关的额外检查：`check-modern-styles.mjs`（§2.5）、ESLint 的 modern 专属规则（`eslint.config.mjs:19-40,41-68`）、Go 契约测试 `internal/control/classic_api_contract_test.go`（§1.5，改后端响应结构时会红）。

---

## 7. `useURLState` 用法约定

- 实现：`web/src/frontends/modern/app/url-state.ts:10-47`；文件头注释 `:10`：「**只保存明确声明的展示状态；密钥、授权信息和编辑草稿不进入 URL**」。签名为 `useURLState<T extends object>(keys, parse, serialize)`，返回 `Ref<T>`，双向同步到 `route.query`（`:19-45`，`router.replace` 失败会回滚本地值）。
- 本面板已有键（`GroupModelsPanel.vue:37-41`）：`sync_models='1'` 表示同步对话框打开；`syncing` computed 包一层 getter/setter（`:42-47`）。
- 对话框键（`GroupModelSyncDialog.vue:22-28`）：`sync_mode` ∈ `full|add|cleanup`，`full` 不写 URL。
- 选择器键（`GroupModelPicker.vue:46-60`）：`models_q` / `models_page` / `models_size` / `pick_models`。
- 面板关闭时统一清理（`GroupDetailView.vue:98-116`：`delete panel / pick_models / sync_models / sync_mode`）。
- 约定归纳：进 URL 的都是「当前打开了哪个浮层/筛选/分页」这类可分享的展示态；**分组级持久化开关属于服务端配置，不应进 URL**（PRD R1 也要求它落服务端）。`positivePage`（`url-state.ts:48-51`）是数值解析的现成工具。

---

## 8. 对本次设计的直接结论

### 8.1 最小改动文件清单（按改动类型分组）

**A. 必然修改（modern，功能本体）**

| 文件 | 改动 |
|---|---|
| `web/src/frontends/modern/features/groups/GroupModelsPanel.vue` | `:173-184` 插槽内加「带可见文案的 AppSwitch」；新增开关状态的读取/写入与即时保存逻辑（不进 `signature()`/`dirty`，`save()` 路径不动）；`:147` 传新的抽屉尺寸档 |
| `web/src/frontends/modern/features/groups/GroupModelSyncDialog.vue` | **仅 `:64`** `aliases: []` → 生成 `claude-<id>`（含 `claude-` 前缀跳过与冲突跳过） |
| `web/src/frontends/modern/i18n/locales/group-workflows.ts`（或 `group-detail.ts`） | 三个语言对象各加同名 key（开关文案 + 无障碍 label） |
| 新增 API 层函数（按所选保存通道定文件） | 通道 A/B → `web/src/frontends/modern/api/group-detail.ts`；通道 C → 同文件或新文件加 `/api/modern/...` 调用 |

**B. 加宽抽屉（选择 §4.3-B 时需要）**

| 文件 | 改动 |
|---|---|
| `web/src/frontends/modern/styles/tokens.css:239` 旁 | 新增 `--modern-dialog-sheet-*-width` |
| `web/src/frontends/modern/components/ui/AppDialogContent.vue:10` + `:65-67` 旁 | `size` union 加档 + 新尺寸类 |
| `web/src/frontends/modern/features/groups/GroupEditorSurface.vue:11` | `size` union 同步加档 |
| `web/src/frontends/modern/features/groups/GroupWorkspacePanel.vue:17,32` | `wide` 改成/补充尺寸枚举并透传 |

（若接受「健康详情与模型目录设置一起变宽」，只改 `tokens.css:239` 一行即可，但违反 PRD R4/AC11。）

**C. 跨层（后端 + 契约，取决于保存通道 —— 需与后端方案对齐）**

| 位置 | 触发条件 |
|---|---|
| `internal/control/group_models.go:16-18` 或 `internal/control/group_settings.go:40-50` | 请求体新增字段（严格解码 `strict_json.go:22`） |
| `internal/control/classic_api_contract_test.go:37-40` + `web/src/frontends/classic/app/resources/groups.ts:62-78` | 只要**响应结构**新增字段（含 `overrides`/`effective` 内新键 → 还要改 `groups.ts:114-123`，否则 classic 抛 `invalidResponse`，`projector.ts:134-144`） |
| 新端点（如 `/api/modern/groups/:id/...`） | 唯一能同时满足「开关落服务端」与「classic 不动」的形态（`classic_api_contract_test.go:17-141` 不覆盖新路由；`README.md:39` 允许现代端自有接口） |

**D. 明确不需要改的文件**

- `web/src/shared/models/model-aliases.ts`（现有函数语义已兼容 `claude-<id>`，见 §3.2）；不过若要把「生成别名」做成纯函数并复用到两处（对话框与后端对齐口径），新增函数应放这里而不是组件里（spec `.trellis/spec/backend/model-name-contract.md:235-245` 要求别名纯逻辑单源）。
- `web/src/frontends/modern/api/groups.ts`（除非开关走 `GroupBasicsPatch` 通道，那一侧只支持 `name/enabled/weight_manual/price_multiplier`，`:66-71`）。
- classic 目录下任何业务文件（PRD Constraints；例外见 8.1-C）。

### 8.2 必须复用的既有函数

- 别名与开关推导：`@shared/models/model-aliases` 的 `normalizeAliases`（生成后自检）、`visibleAliases`（展示）、`findModelNameConflicts`（预检冲突）、`hasClaudeAdapter` / `withClaudeAdapter`（不要绕过它们直接操作 `aliases` 数组）。
- 草稿行工具：`group-create-rules.ts` 的 `setAliases` / `setClaudeAdapter` / `modelErrors` / `GroupDraftModel`。
- 请求与缓存：`api/group-detail.ts` 的 `getGroupModels` / `saveGroupModels` / `groupModelsKey` / `groupSettingsKey` / `getGroupSettings` / `saveGroupSettings`；`api/groups.ts` 的 `updateGroupBasics` + `GroupBasicsPatch`（如需走 basics 通道）。
- 交互与提示：`components/ui` 的 `AppSwitch` / `AppButton` / `AppNotice` / `AppTooltip`；`useMessageSource`（`@modern/app/messages`）；`useDraftGuard`/`AppDraftGuard` 的脏检查不必参与（开关不进草稿）。
- 缓存一致性：保存成功后 `cache.setQueryData` + `emit('saved')` 的既有写法（`GroupModelsPanel.vue:115-119`、`GroupBasicsForm.vue:146-156`）。

### 8.3 三个待定取舍

1. **开关保存通道**
   - A 跟随 models PUT：复用现有面板保存链与缓存键，但请求/响应都要动后端，且 `save()` 的 dirty/校验守卫与「即时保存」语义冲突，还会把开关状态混进模型响应的契约（触发 classic 契约改动）。
   - B 走 groups settings：有现成的「即时保存 + 最小 patch + 缓存合并」范式（`GroupBasicsForm.vue:122-162`），但顶层新字段与 `overrides` 新键都会撞上 classic 的白名单断言（`groups.ts:62-76`、`:114-123`），现代端还要同步扩 `runtimeNumbers/runtimeSwitches`（`api/group-detail.ts:34-40,78-112`）以免丢键。
   - C 新增现代端专属端点：唯一能同时满足 PRD「只改 modern」与「即时保存」的形态，代价是后端新增路由与 DTO、前端新增 api 函数与 query key。
2. **只加宽「模型与别名」面板的做法**
   - A 改共享 token（1 行，但三处面板同变，违反 AC11）；B 新增尺寸档打通 `AppDialogContent → GroupEditorSurface → GroupWorkspacePanel` 三层（改动 4 个共享文件、类型安全）；C 面板侧类名/全局样式覆盖（改动最少、依赖 attrs 透传是否落到 portal 元素，需实测且绕过 token）。
3. **三语要求**
   - 结论是硬要求（PRD Constraints + `enUS: typeof zhCN` 类型约束），但需决定新 key 放 `group-workflows.ts` 还是 `group-detail.ts`，以及是否复用同一 key 作为 `AppSwitch` 的 `label`（tooltip/aria）与可见文案。

---

## Caveats / 未确认项

- **attrs 透传是否命中 portal 元素未实测**（§4.3-C）：`GroupEditorSurface.vue:22-40` 的根是 `DialogRoot`，`AppDialogContent.vue:4,21` 是 `inheritAttrs:false + v-bind="$attrs"`，class 合并行为需要跑起来验证；未验证前不要把方案 C 当作确定可行。
- **`GroupModelSyncDialog` 的既有别名引用共享**：`:18` 只浅拷贝行，`aliases` 数组与面板 draft 同引用；新生成的别名单必须是新数组（`withClaudeAdapter:52-57` 的既有教训），否则会污染 `signature()` 基线。
- **`claude-<id>` 与 `[1M]` 后缀的交互**：若上游模型 ID 本身带 `[1M]`/`[1m]`，生成的别名会同时认领基名（后端 `modelname.Base`，`modelname.go:20-30`），可能触发前端预检测不到的写路径冲突；PRD R3 只写了「ID 以 `claude-` 开头则跳过」，未提后缀情形。
- **别名数量上限**：后端 `maxAliasesPerModel = 10`（`group_write.go:28`）。本次只给 `aliases: []` 的新增模型加 1 个别名，不会触限；但若将来对既有行补别名需重新评估。
- **自动同步失败/冲突的后端行为不在本次前端调研范围**（PRD R2 由后端实现；前端只有开关与别名两件事）。
- **未找到**：modern 侧任何 `MODEL_NAME_CONFLICT` 的专门处理（对比 classic `GroupModelsTab.vue:474`）；也未找到任何 `claude-<id>` 前缀别名的既有生成实现（全仓 grep 无命中）。
