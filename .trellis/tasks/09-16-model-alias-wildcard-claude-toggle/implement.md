# 执行计划：模型别名通配符匹配与 Claude 适配开关

## 执行顺序总览

后端先行、前端后接。后端每一段都可独立编译与测试；前端依赖后端已能保存并路由
通配符别名。第 8、9 步（文档与 spec）必须在提交前完成。

```
1 modelname 基础        → 单元测试
2 state 构建期索引      → snapshot 测试         ← 评审门 A（数据结构定型）
3 scheduler 查找兜底    → scheduler/gateway 测试 ← 评审门 B（路由行为定型）
4 后端展示层过滤        → control 测试          ← 评审门 C（跨层契约核对）
5 前端常量与谓词        → type-check
6 前端列与开关          → type-check + 浏览器实测 ← 评审门 D（UI 定型）
7 前端展示层过滤        → type-check
8 README 文档
9 spec 修订
10 全量验证与提交
```

## 1. `internal/modelname` 基础

- [ ] `internal/modelname/modelname.go` 新增 `IsPattern`、`Match`、`PatternSpecificity`，
      带包内注释说明「`*` 匹配任意长度（含空）的字节序列」「不含 `*` 时退化为字符串
      相等」，并按项目习惯使用中文注释。
- [ ] `Match` 用双指针回溯实现，字节级比较，不做 Unicode 归一、不做大小写折叠。
- [ ] `internal/modelname/modelname_test.go` 新增用例：
  - [ ] `Match`：纯字面量模式等价于相等；前缀 `claude-*`、后缀 `*-sonnet`、中缀
        `claude-*-5`、多 `*`、`*` 单独成模式、空串名称、模式命中空串前缀
  - [ ] `Match` 不命中：`claude-*` 不匹配 `gpt-4o`；`claude-sonnet-*` 不匹配 `claude-opus-5`
  - [ ] `IsPattern`：含与不含 `*`
  - [ ] `PatternSpecificity`：`claude-*` = (7, 7)；`claude-sonnet-*` = (14, 14)；
        `claude-*-5` = (7, 8)；`*` = (0, 0)
  - [ ] **既有断言不得调整**：`Base` 对 `*` 是恒等这条要有显式用例（`Base("claude-*[1m]")
        == "claude-*"`），`TestBaseStripsContextSuffix` 现有 10 个子用例全部保持原期望
  - [ ] `Allows` 现有用例全部保持原期望（C5：不扩展模式语义，三条非扩张断言必须继续通过）

验证：

```bash
go test -p 2 ./internal/modelname/...
```

**回滚点**：本步骤是纯新增，直接 revert 即可。

## 2. `internal/state` 构建期索引

- [ ] `internal/state/snapshot.go` 新增 `ModelPattern` 与 `ModelPatternIndex` 类型。
- [ ] `ConfigSnapshot` 新增 `ExecutionPatterns` 与 `ExecutionRoutePatterns` 两个字段，
      初始化在 `Compile` 里与既有两个索引并列（`snapshot.go:307-308` 附近），
      注册分别跟进 `snapshot.go:324`（巡检目录）与 `snapshot.go:369`（数据面候选）。
- [ ] `appendExecutionTargets`（`snapshot.go:432`）增加模式索引参数；在既有 `claimed`
      去重（456-466）之后按 `modelname.IsPattern` 分流：非模式走
      `appendExecutionTarget`（原样），模式走新的 `appendModelPattern`。
      **去重必须在分流之前**。`NoModelRouteKey` 分支（481-487）不参与分流。
- [ ] `appendModelPattern` 写入 builder map，`Targets` 必须填全 `UpstreamModelID`
      与 `Mode`（与 498-506 行同源）。
- [ ] 从 `sortExecutionRouteIndex`（540-557）中提取 `lessRouteTarget(a, b RouteTarget) bool`，
      两处复用。
- [ ] 新增 `finalizeModelPatternIndex`：按 `design.md` §4.4 的三级键排序模式
      （`prefixLen` 降序 → `literalCount` 降序 → 模式字节序升序），并对每个模式的
      `Targets` 用 `lessRouteTarget` 排序。在 `Compile` 末尾、两个
      `sortExecutionRouteIndex` 调用（382-383）旁调用。
- [ ] 新增 `ConcreteModelNames` / `ConcreteRoutableModelNames` 与其私有 `withoutPatterns`。
- [ ] 更新 `RoutableModelNames` 的文档注释：它现在返回的集合包含**不可枚举**的模式
      字面量，「返回了它」不再等价于「精确查找能命中它」——注释 92-103 行当前承诺的是
      可枚举性，必须改写。

- [ ] `internal/state/snapshot_test.go` 扩展现有用例（**不得修改既有期望值**）：
  - [ ] `TestRoutableModelNamesExpandsContextSuffixBase`（60）新增含 `*` 的行，钉住
        「模式字面量原样出现、并派生出剥后缀后的模式，不展开成具体名」
  - [ ] `TestRoutableModelNamesMatchesExternalNamesWithoutSuffix`（117）新增含 `*` 的行，
        钉住两个集合对模式别名是否一致
  - [ ] `TestCompileIndexesContextSuffixAliasBase`（135）新增用例，钉住
        「精确索引不含模式键、模式索引含且仅含模式键」
  - [ ] 新模式索引的用例：跨分组同名模式聚合为一个条目、`Targets` 按
        `lessRouteTarget` 排序、模式按特异性降序、`ExecutionPatterns` 与
        `ExecutionRoutePatterns` 分别对应启用组与全部组
  - [ ] 确定性用例：同一配置重复 `Compile` 两次，两个模式索引 `reflect.DeepEqual` 相等
        （对应 `Manager.Matches` 的 `configuration_changed` 风险）
  - [ ] 冲突用例：`TestCompileRejectsInvalidCoreConfiguration`（407）新增同模式跨 ID
        用例与「重叠模式不冲突」用例；**既有两个后缀错误字符串断言不得重排格式**
  - [ ] 无通配符配置下 `ExecutionPatterns` 为空、`ConcreteModelNames` 与
        `ExternalModelNames` 逐项相等的回归用例

验证：

```bash
go test -p 2 ./internal/state/... ./internal/control/...
```

**评审门 A**：确认模式索引结构、两个快照字段的分配点、以及 `ConcreteModelNames`
的命名与语义。此处定型后第 3、4 步才有稳定基础。

**回滚点**：本步骤完成后精确索引行为应逐字不变（AC22），若 `internal/state` 与
`internal/control` 既有测试出现非预期失败，先回退本步再排查。

## 3. `internal/scheduler` 查找兜底

- [ ] `internal/scheduler/inspect.go` 的 `evaluateTargets`（98）签名增加
      `patterns state.ModelPatternIndex` 参数。
- [ ] 查找段（136-143）改为 `design.md` §4.2 的形式：精确命中短路；仅当
      `query.externalModel != nil` 且精确未命中时才走 `matchModelPattern`；两者都空
      仍返回 `ReasonNoRouteTarget`。
- [ ] 新增 `matchModelPattern(patterns []state.ModelPattern, name string) []state.RouteTarget`，
      线性扫描返回首个命中的 `Targets`。
- [ ] 更新三个调用点：
  - [ ] `internal/scheduler/scheduler.go:102`（`CandidateGroupIDsForQuery`）→ 传
        `snapshot.ExecutionPatterns`
  - [ ] `internal/scheduler/scheduler.go:289`（`filterTargetsWithReason`）→ 传
        `snapshot.ExecutionPatterns`
  - [ ] `internal/scheduler/inspect.go:367`（`Inspect`）→ 传 `snapshot.ExecutionRoutePatterns`

- [ ] `internal/scheduler/inspect_test.go` 扩展：
  - [ ] 模式命中：`claude-*` 命中 `claude-opus-5`，返回的目标与精确命中同构
        （`UpstreamModelID` / `Mode` / `ResolvedTarget` 均正确）
  - [ ] 精确优先：同时存在精确键与模式时走精确键，结果不与模式合并
  - [ ] 特异性裁决：`claude-*` 与 `claude-sonnet-*` 并存时 `claude-sonnet-5` 命中后者；
        交换配置顺序后结果不变
  - [ ] `ReasonNoRouteTarget`：既无精确键也无模式命中时结果与今天逐字相同，且
        **不**在 `ReasonModelFiltered` 与 `ReasonNoRouteTarget` 之间翻转
        （`TestInspectAppliesTopLevelReasonPriority` 的优先级矩阵不得变动）
  - [ ] `NoModelRouteKey` 路径不参与模式匹配（资源操作用例）
  - [ ] `TestInspectEligiblePoolMatchesIteratorInitialWeightedPool`（270）等三项
        Inspect / Iterator 一致性用例在模式配置下继续通过——这是「兜底必须落在共享
        查找点、不能落进 Inspect」的强制臂
  - [ ] `Allows` 相关回归：既有白名单用例期望值全部不变

- [ ] `internal/gateway` 测试扩展：
  - [ ] `models_test.go`：`TestVisibleModelIDs`（16）与
        `TestVisibleModelIDsNormalizesContextSuffixFilter`（70）新增含模式的配置，
        断言**列表里不出现模式名**（AC11）；既有 want 列表不得变动
  - [ ] `handler_test.go:1831`（`TestHandlerModelListExposesContextSuffixAliasBase`）
        新增模式用例，逐字节断言响应体不含 `*`
  - [ ] `handler_test.go:4653`（不泄漏上游模型名）新增用例：请求名从未在配置里出现、
        经模式命中后，响应仍原样回显**客户端请求名**，不泄漏上游 ID
  - [ ] `dialects_integration_test.go:854` 旁新增端到端用例：Anthropic 协议下
        `claude-opus-5` 经 `claude-*[1m]` 路由到配置的上游 ID 并返回 200

验证：

```bash
go test -p 2 ./internal/scheduler/... ./internal/gateway/...
```

**评审门 B**：确认精确优先的短路位置、`NoModelRouteKey` 守卫、以及三个调用点的
一致性。此处定型后路由行为即冻结。

## 4. 后端展示层过滤

按 `design.md` §4.3 的表逐点替换，**必须同一个提交内完成**：

- [ ] `internal/control/group_options.go:124` → `state.ConcreteModelNames`
- [ ] `internal/control/project_model_collection.go:259` → `state.ConcreteRoutableModelNames`
- [ ] `internal/control/project_model_collection.go:496` → `state.ConcreteRoutableModelNames`
- [ ] `internal/control/price_reconcile.go:131` → `state.ConcreteRoutableModelNames`
- [ ] `internal/control/home.go:280` 与 `:402` → `state.ConcreteModelNames`
- [ ] 改写 `internal/control/project_model_collection.go:255-259` 的注释：它现在说的
      「与 /v1/models 一致」在 `/v1/models` 走精确索引后不再成立
- [ ] **确认不改**：`internal/gateway/models.go`（模式不在精确索引里）、
      `internal/control/group_models.go:83`（`client_models` 契约）、
      `internal/control/group_collection.go:449`（读路径必须宽松）

- [ ] 测试扩展（**既有期望值不得调整**）：
  - [ ] `internal/control/group_models_test.go:419`（`TestUpdateGroupModelsRoutesContextSuffixAliasBase`）
        新增模式用例：保存后 `aliases` 与 `client_models` 与保存前逐项一致
        （AC6，模式字面量出现在 `client_models` 中）、精确索引不含模式键
  - [ ] `group_models_test.go:94`（`TestNormalizeGroupModelsAppliesAliasSwitchAndReportsStableConflicts`）
        新增：同模式跨 ID 报 `MODEL_NAME_CONFLICT` 且 `ClientModel` 等于模式串（AC7）；
        同 ID 多记录共用模式保存成功（AC8）；重叠模式保存成功（AC9）
  - [ ] `group_models_test.go:749`（`TestUpdateGroupModelsFailuresDoNotPublish`）
        新增：模式冲突时同样不发布快照、不改动已存模型（AC10）
  - [ ] `group_options` 用例：候选集不含模式名（AC12）
  - [ ] 模型页用例：名称列表、上游关联明细、`price.reference_count` /
        `associations.length` / `client_model_count` 三者相互一致且都不含模式名（AC13）
  - [ ] 首页用例：名称集合不含模式名（AC14）
  - [ ] 读路径回归：存量含 `*` 别名的分组仍能列出、不返回 500

验证：

```bash
go test -p 2 ./internal/control/... ./internal/gateway/... ./internal/scheduler/...
```

**评审门 C**：逐条核对 `price.reference_count == associations.length` 与
`client_model_count`，这三处必须同口径。历史上这一对漂移过一次，spec 有专门章节。

**回滚点**：本步骤若导致模型页计数断言失败，回退本步（第 1-3 步的纯路由能力不依赖它）。

## 5. 前端常量与谓词

- [ ] `web/src/features/models/model-draft.ts` 新增 `claudeAdapterAlias`、
      `claudeAdapterAliases`、`isClaudeAdapterAlias`、`hasClaudeAdapter`、
      `isWildcardAlias`、`visibleAliases`、`withClaudeAdapter`。
- [ ] `ModelAliasEditorLabels` **不改**（该列文案走第 6 步的可选 prop）。
- [ ] `normalizeAliases` 与 `findModelNameConflicts` **不改**（见 `design.md` §5.5/§5.6）。
- [ ] `withClaudeAdapter` / `visibleAliases` 必须返回新数组，且**不做页面条件分支**——
      导入页同样走这两个函数，以保证草稿里存在的常量别名被原样保留（AC23）。

验证：

```bash
cd web && pnpm run type-check
```

本步不涉及组件，类型检查应直接通过。

## 6. 前端列与开关

- [ ] `web/src/features/models/ModelAliasEditor.vue`：
  - [ ] 新增可选 prop `claudeAdapter?: { title: string; forModel: (id: string) => string;
        conflict: (id: string) => string }`；列渲染条件为 `claudeAdapter !== undefined`
  - [ ] `grid-class` 改为计算属性：有 `claudeAdapter` 时返回
        `'model-alias-editor__grid model-alias-editor__grid--claude'`，否则返回
        `'model-alias-editor__grid'`
  - [ ] 新增 `.model-alias-editor__grid--claude` 规则插入第 2 个轨道（`116px`，对齐既有
        先例 `GroupsView.vue:641` 的 `140px`、`AccessKeyCollection.vue:425` 的 `136px`）；
        **既有 4 轨道规则保持逐字不变**，导入页走它
  - [ ] 表头（238 与 239 之间）插入第 3 个 `columnheader`，带 `v-if="claudeAdapter"`
  - [ ] 别名单元格（止 306）与 `third-column` 单元格（起 308）之间插入开关单元格，
        同样受 `v-if="claudeAdapter"` 控制；使用 `AppSwitch`，状态来自
        `hasClaudeAdapter(item.aliases)`，事件走 `setClaudeAdapter(index, $event)`
        （内部调 `updateRow` + `withClaudeAdapter`）
  - [ ] TagInput 的 `:model-value` 改为 `visibleAliases(item.aliases)`
  - [ ] 搜索匹配文本（74 行）改用 `visibleAliases(item.aliases).join(' ')`
  - [ ] `setAliases`（92-96）在 `normalizeAliases` 之前用 `withClaudeAdapter` 合并回
        开关状态，且构造新数组（不得就地 push）
  - [ ] 新增 `claudeAdapterError(index)`，并把开关单元格的错误纳入
        `visibleInvalidIndexes`；冲突名是 Claude 适配常量时错误挂到开关列而非别名列
  - [ ] CSS：新单元格在桌面为 flex-wrap 容器；加入 `min-width: 0` 列表（397-400）；
        在 860px 块内 `grid-column: 1 / -1` 并加入既有的 `display:grid;
        align-content:start; gap:5px` 选择器列表（492-498）；移动端标题用
        `.model-alias-editor__mobile-label` + `flex-basis: 100%`
- [ ] `web/src/features/groups/models/GroupModelsTab.vue`：模板里多传
      `:claude-adapter="{ title: t('group.modelEditor.claudeAdapter'), forModel: ..., conflict: ... }"`；
      `:columns="3"`（533）改 `4`
- [ ] `web/src/features/import/NewGroupImport.vue`：**不传** `claude-adapter`，其余不动
- [ ] `web/src/i18n/locales/zh-CN/group.ts` 的 `group.modelEditor` 内新增
      `claudeAdapter` / `claudeAdapterFor` / `claudeAdapterConflict`
- [ ] 同步补 `en-US` / `ja-JP` 对应键（不参与打包，但与仓库三语维护惯例一致）
- [ ] **不要**在 `zh-CN/import.ts` 里加这组键——导入页不渲染该列

验证：

```bash
cd web && pnpm run type-check && pnpm run lint
```

浏览器实测（无前端测试运行器，只能手工；启动方式见 `/run`）：

- [ ] 桌面宽度：表头依次为 模型ID / 别名 / Claude 适配 / 价格状态 / 操作，
      列内容与表头一一对应（AC21）
- [ ] 860px 断点：卡片依次为 ID / 别名 / 开关 / 价格状态，无横向溢出（AC21）
- [ ] 别名输入框中看不到 `claude-*[1m]`（AC15）
- [ ] 手输 `gpt-*` 正常展示、可删除、保存后仍在（AC16）
- [ ] 打开开关保存 → 重新拉取分组 → 开关仍为打开（AC17）
- [ ] 同模型另有可见别名 `foo`，关闭开关保存 → 后端 `aliases` 为 `["foo"]`（AC18）
- [ ] 无其他别名时关闭开关保存 → `aliases` 为 `[]`（AC19）
- [ ] 同一分组第二个模型打开开关 → 开关列给出可定位的冲突提示（AC20）
- [ ] 导入页的模型表格**没有**该列，且列布局与改动前逐字一致，无空轨道（AC22）

**评审门 D**：UI 定型。这里必须停下让大哥实际点一遍再继续。

**回滚点**：本步骤纯前端，回退后后端能力仍完整（用户可手输 `claude-*[1m]` 达到同样
效果）。

## 7. 前端展示层过滤

按 `design.md` §5.8 的表逐点处理，实现时逐个确认数据源：

- [ ] `web/src/app/resources/groups.ts` 的 `cacheGroupModels`（~1000）
- [ ] `web/src/components/config/ParameterOverrideRulesEditor.vue` 的 `modelSuggestions`（~146）
- [ ] `web/src/features/groups/settings/GroupSettingsBaseForm.vue` 的 `validationModelOptions`（48-53）
- [ ] `web/src/features/groups/models/GroupModelSyncDialog.vue` 的 `removalPreview`（118-125），
      连 `v-if` 的判空一起改到过滤后的列表上
- [ ] `web/src/features/access-keys/access-key-options.ts` 的 `buildAccessKeyModelOptions`（26-35）
- [ ] `web/src/features/monitor/InspectorTab.vue` 的 `configuredModels`（104-108）
- [ ] **不要改** `web/src/app/resources/groups.ts` 的 `projectGroupModelItem` 断言（441-458）：
      `client_models` 仍逐项等于 `[id, ...aliases]`，改它会导致所有开了开关的分组
      模型页整页报错

验证：

```bash
cd web && pnpm run type-check && pnpm run lint
```

浏览器实测：巡检表单、访问密钥模型选择器、参数覆盖规则编辑器、校验模型下拉、
同步删除确认弹窗里都不出现含 `*` 的名称。

## 8. README 文档

- [ ] `README.md`、`README_CN.md`、`README_JP.md` 的模型/分组配置相关小节补充：
  - [ ] 别名支持 `*` 通配符（`*` 匹配任意长度，含空）
  - [ ] 精确匹配优先于通配符；多个通配符重叠时更具体的（字面前缀更长）优先
  - [ ] 通配符别名不会出现在 `/v1/models` 与各类模型选择器中
  - [ ] 「Claude 适配」开关等价于为该模型写入别名 `claude-*[1m]`，用于把任意
        Claude 模型名路由到该上游
  - [ ] 已知边界：单独一个 `*` 会匹配该分组内所有无精确键的请求名；升级前就已存在
        的 `*` 别名会从「无效」变为「生效」

## 9. spec 修订

按 `design.md` §8 的六条逐项修订 `.trellis/spec/backend/model-name-contract.md`。
第 5 条（重写 Wrong 示例）与第 2 条（修订「唯一注册点」不变量）是硬性的——现行 spec
明确反对本特性的唯一可行设计。

## 10. 全量验证与提交

```bash
go build ./...
go vet ./...
go test -p 2 ./internal/... 2>&1 | tee /tmp/after.txt
```

- [ ] 与本任务开始前记录的基线逐包比对，确认没有新增 FAIL
      （Windows 环境：需 `-p 2`；全量套件存在与本任务无关的既有 FAIL）
- [ ] AC1-AC24 逐条勾选，标注哪些是测试覆盖、哪些是手工验证
- [ ] `add -A` 前确认没有误加临时文件
- [ ] 提交信息遵循仓库既有风格（`feat(...)` / `docs(spec)` 等前缀）

## 评审门汇总

| 门 | 位置 | 内容 |
| --- | --- | --- |
| A | 第 2 步后 | 数据结构定型：模式索引形状、快照字段、`ConcreteModelNames` 命名 |
| B | 第 3 步后 | 路由行为定型：精确优先短路、`NoModelRouteKey` 守卫、三调用点一致 |
| C | 第 4 步后 | 跨层契约核对：`client_models` 与模型页计数三处同口径 |
| D | 第 6 步后 | UI 定型：列顺序、断点布局、开关行为，需实际点击验收 |
