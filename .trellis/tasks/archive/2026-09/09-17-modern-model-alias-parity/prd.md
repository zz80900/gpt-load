# PRD：modern 前端同步多别名 / 通配符 / Claude 适配开关

## 背景

- 2026-09-17 合并上游 #668 后项目成为双前端：`classic`（原前端整体迁移）与 `modern`（上游新增管理前端），`getPreferredFrontend()` 默认返回 modern。
- fork 的模型能力——一个模型多个对外别名、通配符模式别名（`*`）、Claude 适配开关（等价别名 `claude-*[1m]`，分组内互斥）——完整实现只存在于 classic。
- 合并时对 modern 只做了「能跑不丢数据」的最小适配：别名输入是逗号分隔文本框、无 Claude 开关、冲突判定是简化版（不区分同 ID 重复认领）。

## 目标

modern 前端的分组模型编辑能力与 classic 对齐：多别名 tag 式编辑、通配符别名、Claude 适配开关、与后端一致的冲突判定。别名/冲突纯逻辑收敛到 `@shared` 单一实现，两个前端共用。

## 需求

- **R1 多别名编辑**：modern 的模型行使用 tag 式别名输入（回车 / 逗号 / 顿号提交，输入框空时退格删除末项，粘贴按分隔符拆分，失焦提交未确认文本），替换现有逗号分隔文本框。
- **R2 通配符**：别名允许包含 `*`，前端不做形态校验，原样保存与回显。
- **R3 Claude 适配开关**：模型行提供「Claude 适配」开关；开关状态完全由别名数组中是否存在 `claude-*[1m]` 推导，不引入新持久化字段；开关在分组内互斥——打开一个自动关闭其余；别名输入中不显示该常量（由开关承载），用户手输 `claude-*[1m]`（含大写 `[1M]` 形态）等价于打开开关。
- **R4 冲突判定**：与后端 `validateGroupCollectionModels` 同口径——同一上游 ID 的多行可重复认领同一名称（存量单别名写法），跨 ID 认领同名才是冲突；冲突需报出名称与涉及行，并把错误归属到正确的列：冲突名等于该行 ID → ID 列；冲突名是 Claude 适配常量 → 开关列；否则 → 别名列。
- **R5 别名规范化**：trim、丢弃空项与等于模型 ID 的项、按首次出现去重（大小写敏感精确比较），与服务端 `normalizeModelAliases` 同规则；前端判合法的配置必须能被服务端接受。
- **R6 覆盖 modern 三个入口**：分组详情模型编辑（GroupModelsPanel）、创建分组（GroupCreatePanel）、模型同步对话框（GroupModelSyncDialog）。
- **R7 逻辑单源**：别名规范化、Claude 适配推导、冲突检测等纯函数提升到 `web/src/shared/models/`，classic 与 modern 均从该处引用；两侧不得保留第二份实现。
- **R8 文案**：modern 三语（zh-CN / en-US / ja-JP）补齐新增文案，语义对齐 classic zh-CN。

## 非目标

- 不改后端：`internal/control/group_models.go` 的 `{id, aliases, client_models, pricing_status}` 契约已支持全部能力。
- 不改 classic 的交互、布局与文案；classic 仅允许把纯函数挪到 shared 后原地 re-export。
- 不做 Claude 开关的批量启用、不新增独立的通配符语法说明页。

## 验收标准

- **AC1** modern 分组详情编辑模型：别名以 tag 呈现、可增删；保存后重新读取，aliases 与界面所见一致。
- **AC2** 含 `*` 的别名（如 `gpt-*`）可输入、保存、回显，tag 上原样显示。
- **AC3** Claude 开关互斥：开启行 A 后再开启行 B，A 自动关闭；保存后仅 B 的别名含 `claude-*[1m]`；重新进入页面开关状态正确回显。
- **AC4** 在别名输入框手输 `claude-*[1m]` 或 `claude-*[1M]`：不显示为 tag，开关点亮；关闭开关后该别名被移除。
- **AC5** 冲突判定：两行不同 ID 认领同名 → 前端阻止保存并提示名称与位置；同一 ID 的两行重复认领同名 → 不报错。
- **AC6** 创建分组流程（GroupCreatePanel）具备与编辑流程相同的 R1–R5 能力。
- **AC7** 同步对话框合并结果保留既有别名与开关状态；存在冲突时禁用确认按钮并列出冲突名称。
- **AC8** classic 行为零变化：`web` 的 type-check 与 lint 通过，模型编辑关键路径目测不变。
- **AC9** modern 的 type-check、lint（含 `check-modern-styles`）、build 全部通过。

## 依赖与风险

- `web/scripts/check-modern-styles.mjs` 强制约束：`frontends/modern/features/` 下的业务组件禁止直接使用原生 `button`/`input`/`select`/`textarea`/`svg` 标签，必须使用 `components/ui` 公共组件——新的 tag 输入组件必须落在 `components/ui/`，且视觉变量须使用 `styles/tokens.css` 定义的现代版 token，响应断点经 `app/breakpoints.ts`。
- classic 与 modern 互不 import（别名 `@` 只指向 classic），共享只能经 `@shared`。
- Windows 下 pnpm 需前置 node PATH；lint 的 CRLF 误报按既有噪声处理。
