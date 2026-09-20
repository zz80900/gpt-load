# 分组排序：未启用的排到最后

## Goal

modern 分组列表里，未启用的分组在任何排序模式下都排到最后，让常用的（在服务的）分组始终出现在前面。

用户价值：默认视图（`recent`）与按名称视图（`name`）下，被停用的分组不再插在正常分组中间，减少翻找与误操作。

## Background

### 现状（锚点）

modern 分组列表的排序比较器在 `web/src/frontends/modern/features/groups/GroupsView.vue:286`：

```ts
.sort((a, b) => {
  if (f.sort === 'priority' && rank(a) !== rank(b)) return rank(a) - rank(b)
  if (f.sort !== 'name' && a.lastActiveHour !== b.lastActiveHour)
    return (b.lastActiveHour ?? -1) - (a.lastActiveHour ?? -1)
  return a.name.localeCompare(b.name, locale.value) || a.id - b.id
})
```

其中 `rank` 定义在同文件 `:258`：

```ts
const rank = (group: GroupRow) => (isPaused(group) ? 2 : needsAttention(group) ? 0 : 1)
```

**关键问题**：`rank` 只在 `f.sort === 'priority'` 时参与比较。排序档位有三档
（`web/src/frontends/modern/api/groups.ts:6` → `['recent', 'priority', 'name']`），
默认值是 `recent`（`web/src/frontends/modern/features/groups/group-route.ts:22`）。
因此**默认视图下未启用的分组是混在中间的**。

### 「未启用」的口径（已定案）

沿用既有 `paused` 概念，定义在 `web/src/frontends/modern/api/groups.ts:112`：

```ts
export function isPaused(group: GroupRow): boolean {
  return group.availability === 'paused'
}
```

后端 `modernGroupAvailability`（`internal/control/modern_groups.go:84`）把
**组被禁用**与**权重为 0** 都映射为 `paused`（判定点
`internal/control/group_collection.go:509`）。UI 的「暂停」筛选页用的就是这一口径，
故排序沿用同一判定，避免出现「筛在暂停页里、排序却没置后」的自相矛盾。

### 约束

- **前端无测试设施**：`web/src` 下无任何测试文件，`package.json` 无 `test` 脚本，
  也未引入 vitest / jest / @vue/test-utils。因此本任务的自动化验证只能到
  `type-check` / `lint` / `build`，**行为正确性依赖代码审查**，无可执行的回归测试。
- 本次只改 modern。classic 侧按后端 `id ASC` 顺序渲染，不在范围内。

## Requirements

- R1 在 modern 分组列表的**全部**排序模式（`recent` / `priority` / `name`）下，
  `isPaused` 为真的分组一律排在 `isPaused` 为假的分组之后。
- R2 分组内部沿用该模式原本的次级排序，不退化：
  - `recent`：`lastActiveHour` 降序 → 名称 → id
  - `priority`：非暂停组中「需关注」优先 → `lastActiveHour` 降序 → 名称 → id；
    暂停组内部同样按 `lastActiveHour` 降序 → 名称 → id
  - `name`：名称（按 locale） → id
- R3 不改变后端返回顺序、不改动 `paused` 筛选（`f.view === 'paused'`）的过滤行为、
  不改动分页与其他筛选条件。
- R4 `priority` 模式既有语义不得回归：非暂停组中 `needsAttention` 的仍排在最前。
- R5 排序结果稳定可预期，不依赖 `Array.prototype.sort` 的稳定性以外的假设。

## Acceptance Criteria

- [ ] 三种排序模式下，给定同时含暂停组与非暂停组的集合，**全部暂停组位于全部非暂停组之后**
- [ ] `priority` 模式：非暂停组中 `needsAttention`（可用性非 `ready` 且非 `paused`）的仍排最前，无回归
- [ ] `recent` 模式：非暂停组之间仍按 `lastActiveHour` 降序（缺失值视为 -1）
- [ ] `name` 模式：非暂停组之间仍按 locale 名称排序，同名按 id 兜底
- [ ] 暂停组内部不是原序/随机序，仍按该模式的次级规则排序
- [ ] 仅 `paused`（未启用或权重 0）被置后；`no_credentials` / `no_models` / `unavailable`
      等其它可用性不受影响（仍按原规则参与排序）
- [ ] `pnpm run type-check`、`pnpm run lint`、`pnpm exec vite build` 全部通过
- [ ] 未触碰后端与 classic 目录

## Out of Scope

- classic 前端（其后端顺序渲染保持不变）
- 后端 `/api/groups` 与 `/api/modern/groups` 的返回顺序
- 新增排序档位、修改排序档位名称或默认值
- `paused` 筛选页的过滤逻辑
- 为前端引入测试设施（另议）

## Technical Notes

最小改动是在既有比较器最前面加一层暂停分区，例如把 `isPaused` 作为最高优先级比较项，
`priority` 分支保持原样（其 `rank` 已蕴含同样的分区，故等价）。实现细节由 implement 阶段确定。

## Open Questions

无阻塞项。
