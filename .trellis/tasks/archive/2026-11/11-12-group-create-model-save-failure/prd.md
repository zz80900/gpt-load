# 修复新建分组选择模型后保存失败

## Goal

现代前端在「新建分组」页只要存在任意一行模型草稿（手工添加或从发现结果勾选），点击创建就会在
真正发出请求之前失败，界面提示「创建失败，请重试。」。修复后选择模型可以正常保存。

## Background

现象与定位（已在本机用 Node 复现）：

1. `web/src/frontends/modern/features/groups/GroupCreatePanel.vue` 的 `request()` 在组装
   `models` 时写的是 `aliases: model.aliases`，该数组来自 Vue `ref` 的深层响应式代理。
2. 同文件的 `submit()` 调用 `operation.begin({ kind: 'create', request: request() })`。
3. `web/src/frontends/modern/features/groups/group-create-operation.ts` 的 `begin()`
   执行 `structuredClone(payload)` 冻结重试载荷。
4. `structuredClone` 无法克隆 Proxy：只要 `models` 非空，`aliases` 就是响应式 Proxy 数组，
   浏览器/Node 抛出 `DataCloneError: [object Array] could not be cloned`。
5. `GroupCreatePanel.vue:458-462` 捕获该异常，写入通用文案 `groupCreate.failed`
   （「创建失败，请重试。」），请求从未发出。

`models` 为空时 `.map()` 产出普通空数组，克隆成功，所以「不选模型可以保存、选了模型就失败」。

复现命令（web 目录，Node 24 + 已安装的 vue）：

```bash
node --input-type=module -e "
import { ref } from 'vue'
const models = ref([{ id: 'gpt-4o', aliases: [], key: 1, origin: 'discovery' }])
const payload = { models: models.value.map((m) => ({ id: m.id.trim(), aliases: m.aliases })) }
structuredClone(payload) // DataCloneError
"
```

经典前端 `createGroup` 直接 `fetch` + JSON 序列化，不经过 `structuredClone`，因此不受影响。

## Requirements

- R1：`begin()` 冻结的载荷必须与调用方传入的 JSON 语义一致，且能接受来自 Vue 响应式的
  嵌套数组/对象；不再因 Proxy 抛 `DataCloneError`。
- R2：修复放在共享的 `begin()` 边界（唯一克隆点），不在每个调用方各自拷贝字段。
- R3：重试语义不变：首次 `begin()` 后用户编辑表单不得改变已冻结的重试载荷。
- R4：`append` / `connect` 两种提交（目标冲突后追加/连接凭据）行为不变。
- R5：不改动后端契约、不改动载荷字段名与结构。

## Acceptance Criteria

- [x] 用 Node 复现脚本验证：含一行或多行模型（含响应式 `aliases`）的载荷经 `begin()` 的克隆方式处理后成功，且深拷贝（修改原数组不影响克隆结果）。
- [x] 空模型列表仍可正常创建（空数组在 JSON 往返下语义不变）。
- [x] `pnpm run type-check` 通过；`eslint` 通过；prettier 仅因本地 CRLF 检出告警，LF 内容与格式化输出逐字一致。
- [ ] 手工验证路径（待部署后）：新建分组 → 选择至少一个模型 → 创建成功；目标冲突后「仍要创建」/「追加凭据」路径仍正常。

## Out of Scope

- 前端引入 vitest 测试框架。
- 后端 `CreateGroupIdempotent` 的任何改动。
- 模型发现、模型选择器交互的其他问题。
