# 上游模型详情页价格引用计数口径修复 — 技术设计

## 1. 设计目标与硬约束

目标：让 `reference_count` 与 `associations.length` 表达同一件事，使上游模型详情抽屉恢复可用，且这个一致性由**后端一处实现**保证，而不是靠前端删校验绕开。

驱动方案的事实：

| 事实 | 位置 | 后果 |
| --- | --- | --- |
| 前端断言 `reference_count === associations.length` 位于严格投影校验内，失败即抛 `InvalidResponseError`，整个抽屉不可用 | `models.ts:397`、`ModelUpstreamDrawer.vue:171` | 两个数必须真的相等 |
| `reference_count` 参与删除保护与 `can_delete` | `model_price.go:314`、`:611` | 改口径须保持「有引用 ⟺ 计数 > 0」 |
| `reference_count` **不参与**价格物化，`reconcileReferencedPrices` 只用 `identity` | `price_reconcile.go:67-68` | 改口径不影响价格对账的数据本身，风险面收窄到展示与保护判定 |
| 三个数在当前实现下各不相同 | 见第 2 节 | 「改成名称数」不等于「改成 `associations.length`」，必须先裁定口径 |

## 2. 缺陷本质

同一个 `reference_count` 被两类消费点使用，而它只满足其中一类：

| 消费点 | 需要的语义 | 当前取值 | 改后取值 |
| --- | --- | --- | --- |
| `models.ts:397` 断言 | 与 `associations.length` 相等 | 配置条目数 | **相等** |
| `ModelUpstreamDrawer.vue:298` 关联说明 | 关联列表行数 | 配置条目数（错） | 关联行数 |
| `ModelUpstreamDrawer.vue:210-213` 共享影响 | 引用规模 | 配置条目数 | 关联行数 |
| `modelPrices.errors.referenced` | 引用规模 | 配置条目数 | 关联行数 |
| 后端删除保护与 `can_delete` | 有无引用 | 配置条目数 | 关联行数（`> 0` 等价） |

三个候选口径在同一份测试夹具上的取值（夹具见 `model_price_test.go:16-27`：分组 first 两条记录各带一个别名，分组 second 一条记录带一个别名）：

| 口径 | 值 | 说明 |
| --- | --- | --- |
| 配置条目数（现状） | 3 | `buildPriceReferenceSnapshot` 对每个条目 `++` |
| 客户端可见名称数（去重） | 3 | 即 `client_model_count`：`{shared-upstream, client-a, client-b}` |
| **(分组, 客户端可见名称) 对数** | **5** | 即 `associations.length`：g1 三条 + g2 两条 |

**采用第三个口径**。`reference_count` 与 `associations` 从此由同一条去重规则产生，前端断言恒成立。

## 3. 关键决策：为何取「关联对数」而非「名称去重数」

spec `.trellis/spec/backend/model-name-contract.md:165-169` 的措辞是「how many client-visible names reference this upstream model」，字面读指向「名称去重数」；但同一段又描述断言要求 `reference_count == associations.length`，而后者是 (分组, 名称) 对数。**两者在存在多个分组时并不相等**（上表 3 vs 5），措辞与断言描述互相矛盾，必须裁定。

取「关联对数」的理由：

1. **只有它能让断言成立**。取「名称去重数」后 `reference_count(3) ≠ associations.length(5)`，仍需改前端断言，等于把同一件事拆到两个字段上再互相比对，脆弱性没有消除。
2. **取「名称去重数」会让文案自相矛盾**。`sharedImpact` 同时传入 `references`（该数）与 `clients`（`client_model_count`），两者若同为一个名称去重数，句子里两个占位符恒显示相同值。
3. **「关联对数」有自然表述**：它就是把共享影响按「哪个客户端名称在哪个分组里」展开后的条数，与该价格在详情页列出的关联行一一对应。

判定依据写在第 9 节的验证里：改后 `reference_count` 必须与 `associations.length` 在全部形态（含存量脏数据）下逐字相等。

## 4. 改动点

### 4.1 `internal/control/price_reconcile.go` — 计数改为按关联去重

`referencedPrice` 的 `referenceCount int` 换成去重键集合，计数改为集合大小：

```go
type referencedPrice struct {
	identity        pricing.Identity
	associationKeys map[string]struct{} // (groupID, clientModel) 去重键
	groupIDs        map[uint]struct{}
}

// referenceCount 与上游模型详情页的 associations 同粒度：每条关联是
// 「某个分组下的某个客户端可见名称」。
func (reference referencedPrice) referenceCount() int {
	return len(reference.associationKeys)
}
```

循环体（原 `:111` 的 `reference.referenceCount++`）改为：

```go
for _, clientModel := range state.RoutableModelNames(state.ModelConfig{ID: model.ID, Aliases: model.Aliases}) {
	reference.associationKeys[priceAssociationKey(group.ID, clientModel)] = struct{}{}
}
reference.groupIDs[group.ID] = struct{}{}
```

新增共享键构造（**唯一出处**，详情页也用它，见 4.3）：

```go
// priceAssociationKey 是「分组 + 客户端可见名称」关联的去重键。价格引用计数与
// 上游模型详情页的关联列表共用它，两处口径由此在结构上一致；分头拼 key 会让
// 计数与列表在存量数据上再次漂移。
func priceAssociationKey(groupID uint, clientModel string) string {
	return fmt.Sprintf("%d\x00%s", groupID, clientModel)
}
```

需新增 import `gpt-load/internal/state`（同包 `project_model_collection.go` 已依赖，无新依赖方向）。

`reconcileReferencedPrices` 与 `newReconciledModelPrice` 只用 `identity`，**不受影响**。

### 4.2 `internal/control/model_price.go` — 跟随方法化

`referenceCount` 由字段变为方法，五处读取改为调用：

| 行 | 原 | 改 |
| --- | --- | --- |
| `:314` | `reference.referenceCount > 0` | `reference.referenceCount() > 0` |
| `:318` | `ReferenceCount: reference.referenceCount` | `reference.referenceCount()` |
| `:605` | `Referenced: reference.referenceCount > 0` | `reference.referenceCount() > 0` |
| `:606` | `ReferenceCount: reference.referenceCount` | `reference.referenceCount()` |
| `:611` | `reference.referenceCount == 0` | `reference.referenceCount() == 0` |

### 4.3 `internal/control/project_model_collection.go` — 详情页复用共享键

`:497` 的 `key := fmt.Sprintf("%d\x00%s", group.row.ID, clientModel)` 改为 `key := priceAssociationKey(group.row.ID, clientModel)`。行为不变，但两处口径从此只有一个定义。

### 4.4 后端测试 — 跟随期望值

`model_price_test.go:44` 的 `ReferenceCount != 3` 改为 `!= 5`。推导：分组 first 两条记录（别名 client-a、client-b）各产生 `{shared-upstream, client-a}` 与 `{shared-upstream, client-b}` 两条关联，分组 second 一条记录产生 `{shared-upstream, client-a}` 一条，合计 5；`ReferenceGroupCount` 仍为 2，不变。

该建议加一行注释写明 5 的构成，避免读者误以为它与条目数同源。

### 4.5 三语文案 — 跟随新含义

`models.drawer.sharedImpact` 的 `{references}` 不再是配置条目数，三个语言同步改：

| 语言 | 现值 | 改为 |
| --- | --- | --- |
| zh-CN | 同一计价身份被 {references} 条模型配置引用，涉及 {clients} 个客户端模型和 {groups} 个分组；保存后全部生效。 | 同一计价身份有 {references} 条客户端模型关联，覆盖 {clients} 个客户端模型和 {groups} 个分组；保存后全部生效。 |
| en-US | This pricing identity has {references} model references across {clients} client models and {groups} Groups; saving applies to all of them. | This pricing identity has {references} client-model associations across {clients} client models and {groups} Groups; saving applies to all of them. |
| ja-JP | 同じ価格識別子は {references} 件のモデル設定から参照され、クライアントモデル {clients} 件、グループ {groups} 件に影響します。保存内容はすべてに反映されます。 | 同じ価格識別子には {references} 件のクライアントモデル関連があり、クライアントモデル {clients} 件、グループ {groups} 件に影響します。保存内容はすべてに反映されます。 |

`modelPrices.errors.referenced`：

| 语言 | 现值 | 处理 |
| --- | --- | --- |
| zh-CN | 该价格仍被 {entries} 处配置引用，涉及 {groups} 个分组 | 改为「该价格仍有 {entries} 处引用，涉及 {groups} 个分组」——「处配置」不再准确 |
| en-US | This price has {entries} references across {groups} Groups | 不改，措辞本就中性 |
| ja-JP | この価格は {groups} グループから {entries} 件参照されています | 不改 |

### 4.6 前端

**零改动**。`models.ts:397` 的断言保留（改后恒成立，作为后端口径漂移的回归护栏），`ModelUpstreamDrawer.vue:210/213/298` 三处取数全部自动正确。

## 5. 兼容性与不变量

- **无别名配置**：名称集合只有 `[id]`，一个条目贡献一条关联，`reference_count` 与改动前逐字相同（单条目仍是 1），价格列表、删除保护、抽屉显示均无差异。
- **有别名配置**：`reference_count` 增大（1→2、1→3 等），价格列表页与重置对话框显示的引用数随之变大。这是**预期的用户可见变化**：该数从「配置条目数」变为「关联条数」，与新文案一致。
- **删除保护与 `referenced` 布尔值不变**：名称集合非空（`RoutableModelNames` 至少含 `model.ID`），故「关联数 > 0 ⟺ 条目数 > 0」恒成立。
- **价格物化不受影响**：`newReconciledModelPrice` 只用 `identity`。
- **前端严格校验全部保留**：`models.ts:395-398` 四条断言一条不减，其中 `reference_count === associations.length` 由后端结构性保证成立。
- **`reference_group_count` 不变**：仍按 `group.ID` 去重。

## 6. 被否方案

**方案 A：改前端——删除跨语义断言，抽屉改取 `associations.length`。** 改动最小（两行），但把「两个字段相等」从契约降级为巧合：后端无任何机制阻止未来再次漂移，且前端须自行从响应中挑正确的数。用户在评审中改为采纳 spec 口径，本方案落选。

**方案 B：改后端为「名称去重数」（即复用 `client_model_count` 的规则）。** 见第 3 节：断言仍不成立，需再改前端；且 `sharedImpact` 的 `{references}` 与 `{clients}` 恒等。

**方案 C：只改前端断言为方向性校验 `associations.length >= reference_count`。** 存档任务已否决（把后端跨模块策略编码进前端），本次同样否决。

**方案 D：后端新增 `association_count` 字段，`reference_count` 保持条目数。** 冗余数据；且详情响应有严格字段白名单，加字段需前后端同步发布。

## 7. 回滚

改动为 5 个 Go 文件（含测试；第 5 个 `model_prices_http_test.go` 是实施中才发现的第二处旧口径期望值，见 `implement.md` L3）+ 4 个 i18n 文件，无 schema 变更、无接口字段增删、无 migration。回滚即 `git revert`，无需数据修复——`reference_count` 是每次请求实时计算的派生值，不落库。

## 8. 验证策略

- **一致性探针（核心）**：一次性 Go 探针（`internal/control`，用 `createPriceTestGroup` 直接写库）在多形态下断言 `price.ReferenceCount == len(detail.Associations)`，形态须覆盖：无别名、普通别名、后缀别名、同分组多条目同 ID、上游 ID 自带后缀、**同分组重复条目（多条同 ID 记录共用一个上游——写路径有意支持的形态，不是脏数据，见 `group_write.go` 的 `ownersByName` 注释与 `group_models_test.go` 的永久用例）**、跨分组同 ID。前六种由 `GetUpstreamModelDetail` 的详情响应交叉核对，最后一种用快照直接核对。探针用后即删。
- **后端测试**：`go test -p 2 -count=1 ./internal/control/...`，重点 `TestChannelModelPriceIsSharedAcrossGroupsAndAliases`（期望值 3→5）与 `price_reconcile_test.go` 全部用例。
- **构建与静态检查**：`go build ./...`、`go vet ./...`。
- **前端门禁**（前端零改动，仅确认未被牵连）：`pnpm run lint`、`pnpm run type-check`、`pnpm run build`。
- **手工验收**：带别名与不带别名两种配置下打开详情抽屉，核对「共享影响」与「关联关系」两处数字，并与价格列表页的引用数比对。
