# 上游模型详情页价格引用计数口径修复

## Goal

统一 `reference_count` 的口径，使其与上游模型详情页的关联列表表达同一件事，从而让「模型页 → 上游模型详情」抽屉在配置了模型别名时正常打开，并消除前端对后端字段关系的假设。

用户价值：

- **可用性**：当前任何带别名的模型配置，该抽屉都打不开（显示「无法加载上游模型详情」），用户看不到该上游模型的价格、关联分组与客户端名称对应关系。
- **一致性**：修复后 `reference_count` 与关联列表由后端同一条规则产生，二者恒等；前端断言不再是一个随时可能失效的假设，而是后端口径漂移时的回归护栏。

**预期的用户可见变化**：`reference_count` 从「配置条目数」变为「(分组, 客户端可见名称) 关联数」，因此价格列表页、重置对话框与详情抽屉中显示的引用数在带别名的配置上会变大（例：一个分组一条带后缀别名的记录，由 1 变为 3）。无别名的配置数字不变。

## Background

### 现象

分组配置上游 ID `deepseek-flash`、别名 `claude-deepseek-flash[1M]`。在模型页打开该上游模型的详情抽屉，前端校验失败并抛出 `InvalidResponseError`，抽屉只显示「无法加载上游模型详情」。页面其余部分正常。

### 现状链路

```
详情响应 price 计数     internal/control/price_reconcile.go:111              匹配该价格身份的配置条目数
详情响应 associations  internal/control/project_model_collection.go:496-501  条目展开为客户端可见名称后按 (分组, 名称) 去重
前端详情页断言         web/src/app/resources/models.ts:397                   要求两者相等，否则 invalidResponse()
前端共享影响文案       web/src/features/models/ModelUpstreamDrawer.vue:212   「被 {references} 条模型配置引用」
前端关联说明文案       web/src/features/models/ModelUpstreamDrawer.vue:298   「共 {count} 条引用，逐条显示…」
```

### 已确认事实

1. **`reference_count` 是配置条目数**。`buildPriceReferenceSnapshot` 遍历分组下的模型条目，对每个匹配 `(channel, model.ID)` 的条目计数加一（`price_reconcile.go:102-114`），不按别名展开。
2. **`associations.length` 是去重后的 (分组, 客户端可见名称) 对数**。遍历同一批条目，对每条取 `state.RoutableModelNames`（含后缀派生基名），按 `group.ID + "\x00" + 名称` 去重（`project_model_collection.go:487-511`）。
3. **两者覆盖同一批条目，仅聚合方式不同**。筛选条件同为 `channelID` 与 `model.ID` 相等。
4. **`reference_count` 不参与价格物化**。`reconcileReferencedPrices` 与 `newReconciledModelPrice` 只使用 `reference.identity`（`price_reconcile.go:67-68`、`:119-143`），因此改其口径不影响价格对账数据本身，只影响展示与保护判定。
5. **`reference_count` 参与删除保护**。`reference.referenceCount > 0` 决定删除是否被拦截（`model_price.go:314`）、`referenced` 布尔值与 `can_delete`（`:605-611`），错误数据里也回传该数（`:318`）。
6. **`GroupModel` 兼容旧格式的单别名字段**。`alias`（单数）在 `aliases` 为空时被合并为 `[]string{alias}`（`group_write.go:58-65`），故存量数据里两种写法都会产生客户端可见名称。
7. **spec 对口径的措辞与断言描述矛盾**。`.trellis/spec/backend/model-name-contract.md:165-169` 称两个数都应表达「how many client-visible names reference this upstream model」，但同一段描述断言要求二者相等，而 `associations.length` 是 (分组, 名称) 对数——在存在多个分组时两者并不相等。本次按「使断言成立」裁定为关联对数，理由见 `design.md` 第 3 节。
8. **三个候选口径在同一份测试夹具上取值不同**。`model_price_test.go:16-27` 的夹具（分组 first 两条记录、分组 second 一条记录，均已并入别名）对应：配置条目数 3、名称去重数 3、关联对数 5。
9. **三处文案以该数入句**。`models.drawer.sharedImpact`（三语）与 `modelPrices.errors.referenced`（zh-CN 写「处配置」）都用它描述引用规模，口径变了文案须同步。
10. **无别名配置下名称集合只有 `[id]`**，一个条目恰好贡献一条关联，因此该配置的 `reference_count` 与改动前逐字相同。
11. **前端无测试设施**。`web/` 下无测试框架与测试文件，`package.json` 无 test 脚本，质量门禁仅 `lint`、`format`、`type-check`。
12. **影响面为该抽屉与价格列表的引用数显示**。详情断言抛错后由 `ModelUpstreamDrawer` 的 `detailQuery.isError` 分支渲染错误态（`ModelUpstreamDrawer.vue:171`），模型页列表、路由、`/v1/models` 不受影响。

## Requirements

- **R1 抽屉可用性**：配置了模型别名（含 1M 后缀别名与普通别名）时，上游模型详情抽屉正常打开并渲染全部内容。
- **R2 口径统一**：`reference_count` 表达「引用该价格身份的 (分组, 客户端可见名称) 关联数」，与 `associations.length` 恒等；该一致性由后端同一处实现保证，而非前端约定。
- **R3 保护语义不退化**：删除保护、`referenced` 布尔值、`can_delete`、`reference_group_count` 的判定与取值规则保持不变。
- **R4 文案一致**：`models.drawer.sharedImpact`（三语）与 `modelPrices.errors.referenced`（zh-CN）的措辞与新的数字含义一致，不得出现「条模型配置」这类与关联数不符的表述。
- **R5 无别名配置无感**：无别名的配置下，价格列表、删除提示与抽屉显示的数字与改动前逐字一致。
- **R6 前端校验不放宽**：`models.ts` 详情投影中的全部断言保留，不得为了让响应通过而删除或放宽任何一条。

## Acceptance Criteria

- [ ] 分组配置上游 ID `deepseek-flash`、别名 `claude-deepseek-flash[1M]` 时，模型页的该上游模型详情抽屉正常打开，显示「无法加载上游模型详情」以外的真实内容。
- [ ] 该场景下「关联关系」说明文字显示 3，与下方列表实际行数一致；「共享影响」提示出现且其中的引用数与关联行数相同（均为 3）。
- [ ] 配置普通别名（无 1M 后缀）时抽屉同样正常打开，「关联关系」显示 2。
- [ ] 单条目无别名的配置下，抽屉与价格列表页显示的引用数与改动前一致（均为 1）。
- [ ] 价格列表页的引用数、删除保护的拦截行为、重置对话框的错误提示均与新口径一致；带别名配置的引用数大于改动前（与该价格的关联条数相同）。
- [ ] 三语 `sharedImpact` 与 zh-CN `errors.referenced` 文案不再出现「条模型配置 / 处配置」这类与关联数不符的表述。
- [ ] 后端 `reference_count` 与 `associations.length` 在全部测试形态（含同分组重复条目这一存量脏数据形态）下逐字相等。
- [ ] `go build ./...`、`go vet ./...`、`go test -p 2 -count=1 ./internal/control/...` 全部通过（含 `model_price_test.go` 更新后的期望值）。
- [ ] `pnpm run lint`、`pnpm run type-check`、`pnpm run build` 通过，且 `web/src` 下无改动。

## Out of Scope

- **改前端断言或抽屉取数**。前端零改动是本次方案的既定结果，也是「一致性由后端保证」这一目标的判据；删除断言或用 `associations.length` 替代前端取值均属方案 A，已在 `design.md` 第 6 节否决。
- **改后端为「名称去重数」口径**。见 `design.md` 第 3 节的否决理由。
- **新增计数字段**。不新增 `association_count` 之类字段，`reference_count` 直接承载该语义。
- **在前端引入测试框架**。超出本缺陷范围；本次以前端门禁加后端测试作为验证手段。
- **`reference_group_count`、`client_model_count` 或路由、白名单、参数覆盖等其它计数的调整**。实测它们在全部配置形态下均正确（见任务 `09-16-model-name-1m-suffix-compat` 设计工件第 7.1 节），本次只统一一个字段。
- **1M 后缀能力本身**。由任务 `09-16-model-name-1m-suffix-compat` 完成并已归档。
