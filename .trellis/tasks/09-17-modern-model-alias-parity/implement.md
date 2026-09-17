# Implement：modern 前端同步多别名 / 通配符 / Claude 适配开关

> 上下文阅读顺序：`implement.jsonl` 条目 → `prd.md` → `design.md` → 本文件。
> 全程不修改 `internal/`（后端零改动）。不执行 git commit。

## 阶段 1：shared 纯逻辑层 + classic re-export

- [x] 新建 `web/src/shared/models/model-aliases.ts`：从 `web/src/frontends/classic/features/models/model-draft.ts` 平移 design §2 列出的常量、类型与函数，实现与注释逐字保留；参数类型改为最小结构 `{ id: string; aliases?: string[] }`，不得 import classic 的 api 类型。
- [x] 改造 `model-draft.ts`：删除已平移的实现，按 design §2 的 re-export 形式导出；文件内其余导出（`ModelDraftValue`、`createModelDraft`、`mergeCandidateMetadata` 等）保持原位。
- [x] 验证：`pnpm run type-check` 通过（classic 调用点应零改动）。

**评审门 A**：确认 classic 在 re-export 后编译通过、导出面不变（`grep -rn "from './model-draft'\|from '@/features/models/model-draft'" web/src/frontends/classic` 列出的调用点全部无需改动）。

## 阶段 2：modern 的 AppTagInput 公共组件

- [x] 新建 `web/src/frontends/modern/components/ui/AppTagInput.vue`，交互与 Props 按 design §3（对齐 classic `TagInput.vue` 的行为注释）。
- [x] 在 `components/ui/index.ts` 注册导出。
- [x] 视觉变量取自 `styles/tokens.css` 现有定义；断点经 `app/breakpoints.ts`。

## 阶段 3：modern API 层形状改造

- [x] `api/group-create.ts`：`ModelDraft` 改为 `{ id: string; aliases: string[] }`；删除 `readModelAliases` 及其注释；确认 `GroupCreateRequest.models` 为 `{ id, aliases }[]`。
- [x] `api/group-detail.ts`：`saveGroupModels` 参数与请求体按 design §4；删除对 `readModelAliases` 的 import。

## 阶段 4：规则层重写

- [x] 重写 `features/groups/group-create-rules.ts`：新增 `ModelColumn` / `ModelDraftError` 类型与 `modelErrors`（列归属规则见 design §5）、`setClaudeAdapter`、`setAliases`；规则函数从 `@shared/models/model-aliases` import。
- [x] 保留 `credentialCount` / `validBaseURL` 原样。

## 阶段 5：GroupModelPicker 改造

- [x] 按 design §6：四列布局（ID / AppTagInput / AppSwitch / 删除）、表头与栅格同步、`updateRow(key, patch)`、搜索过滤用 `visibleAliases`、按 column 显示错误并聚焦。
- [x] `nextModel` 产出行 `aliases: []`。

## 阶段 6：三个入口适配

- [x] `GroupModelsPanel.vue`：draft 映射与 `signature()` 按 design §7。
- [x] `GroupModelSyncDialog.vue`：`next` 保留 aliases、`conflicts` 用 `findModelNameConflicts`、removals 展示名调整。
- [x] `GroupCreatePanel.vue`：`request()` 直接发 `{ id, aliases }`，移除 `readModelAliases` import。

## 阶段 7：i18n 三语

- [x] `modern/i18n/locales/group-create.ts` 按 design §8 补 zh-CN / en-US / ja-JP 文案；删除「逗号分隔」占位文案。
  - 实际偏差：`group-detail.ts` 无需改动——编辑面板与创建面板共用 GroupModelPicker，新增文案全部落在 `groupCreate.*` 命名空间，`groupDetail.*` 既有键已覆盖面板标题等。

## 阶段 8：验证

```bash
cd web && export PATH="/c/Program Files/nodejs:$PATH"
pnpm run type-check
pnpm run lint
pnpm run build
```

- [x] 三项全部通过（Windows 下 pnpm 需前置 node PATH；prettier/gofmt 的 CRLF 误报为既有噪声，不因它改动无关文件）。
- [x] 手动核对 AC1–AC7 的数据流正确性（可借 dev server 或代码走查）。

## 回滚点

- 阶段 1 后：classic re-export 若引发编译问题，回退 `model-draft.ts` 与新 shared 文件即可。
- 全量：单分支单 PR，`git revert` 提交即整体回退。
