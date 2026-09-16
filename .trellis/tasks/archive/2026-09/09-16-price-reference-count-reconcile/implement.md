# 实施计划：上游模型详情页价格引用计数口径修复

## 前置约束

- **前端零改动**。`web/src` 下不应出现任何 diff（i18n 文案除外——它属于文案层，见 L4）。若实施中出现改前端的冲动，说明方案走偏，回到 `design.md` 第 3 节。
- **不删除任何前端断言**。`models.ts:395-398` 四条全部保留（R6）。
- **一致性由结构保证，而非注释**。计数与关联列表必须共用 `priceAssociationKey`，不得两处各拼一次字符串。
- **不改价格物化路径**。`reconcileReferencedPrices`、`newReconciledModelPrice`、`referencedPrice.identity` 不参与本次改动；若 diff 中出现它们，视为范围外改动。
- 后端注释与文案均为中文；新增注释说明「为什么」（为何两者必须同键），不复述代码。

## 实施清单

### L1 — 后端口径统一（`internal/control/price_reconcile.go`）

1. 新增 import `gpt-load/internal/state`（同包 `project_model_collection.go` 已依赖，不产生新依赖方向）。

2. 新增共享键函数（放在 `referencedPrice` 定义附近）：

   ```go
   // priceAssociationKey 是「分组 + 客户端可见名称」关联的去重键。价格引用计数与
   // 上游模型详情页的关联列表共用它，两处口径由此在结构上一致；分头拼 key 会让
   // 计数与列表在存量数据上再次漂移。
   func priceAssociationKey(groupID uint, clientModel string) string {
       return fmt.Sprintf("%d\x00%s", groupID, clientModel)
   }
   ```

3. 结构体把计数字段换成去重键集合，计数改为方法（`:15-19`）：

   ```go
   type referencedPrice struct {
       identity        pricing.Identity
       associationKeys map[string]struct{}
       groupIDs        map[uint]struct{}
   }

   // referenceCount 与上游模型详情页的 associations 同粒度：每条关联是「某个分组下的
   // 某个客户端可见名称」。计数取集合大小，两者不可能给出不同的数。
   func (reference referencedPrice) referenceCount() int {
       return len(reference.associationKeys)
   }
   ```

   方法化是刻意的：字段可以被单独赋值而与集合脱节，方法不能。

4. 构造点（`:109`）补上集合初始化，否则写入 nil map 会 panic：

   ```go
   reference = referencedPrice{
       identity:        identity,
       associationKeys: make(map[string]struct{}),
       groupIDs:        make(map[uint]struct{}),
   }
   ```

5. 循环体（原 `:111` 的 `reference.referenceCount++`）改为按可路由名称展开并去重：

   ```go
   // 与模型页、/v1/models 同口径：后缀别名派生的基名也是一个可用模型名。
   for _, clientModel := range state.RoutableModelNames(state.ModelConfig{ID: model.ID, Aliases: model.Aliases}) {
       reference.associationKeys[priceAssociationKey(group.ID, clientModel)] = struct{}{}
   }
   reference.groupIDs[group.ID] = struct{}{}
   ```

### L2 — 消费点跟随

6. `internal/control/model_price.go` 五处由字段读改为方法调用（改完编译通过即证明无遗漏读取点）：

   | 行 | 原 | 改 |
   | --- | --- | --- |
   | `:314` | `reference.referenceCount > 0` | `reference.referenceCount() > 0` |
   | `:318` | `ReferenceCount: reference.referenceCount,` | `ReferenceCount: reference.referenceCount(),` |
   | `:605` | `Referenced: reference.referenceCount > 0,` | `reference.referenceCount() > 0,` |
   | `:606` | `ReferenceCount: reference.referenceCount,` | `ReferenceCount: reference.referenceCount(),` |
   | `:611` | `reference.referenceCount == 0` | `reference.referenceCount() == 0` |

7. `internal/control/project_model_collection.go:497` 改为复用共享键（行为不变）：

   ```go
   key := priceAssociationKey(group.row.ID, clientModel)
   ```

   该行原为 `fmt.Sprintf("%d\x00%s", group.row.ID, clientModel)`。改后详情页与计数只有一个键定义源。

8. 编译：`go build ./...`。此处若报 `reference.referenceCount` 相关错误，按其定位补漏——这正是方法化替代字段的用意。

### L3 — 后端测试期望值

9. `internal/control/model_price_test.go:44` 的 `ReferenceCount != 3` 改为 `!= 5`，并加一行说明：

   ```go
   // 引用数按 (分组, 客户端可见名称) 关联计：first 分组两条记录（别名 client-a、
   // client-b）各一条，second 分组一条记录一条，合计 5；分组数仍为 2。
   if len(listed.Items) != 1 || listed.Items[0].ReferenceCount != 5 || listed.Items[0].ReferenceGroupCount != 2 {
   ```

   不要改动断言的其它部分，也不要改 `:70`、`:94` 的空快照字面量（它们不含元素，与字段变更无涉）。

10. 跑 `go test -p 2 -count=1 ./internal/control/...`，确认除期望值外无其它用例受影响。

### L4 — 文案跟随新含义

11. `web/src/i18n/locales/{zh-CN,en-US,ja-JP}/models.ts` 的 `drawer.sharedImpact` 三个语言同步改为表达关联数：

    | 语言 | 新值 |
    | --- | --- |
    | zh-CN | `'同一计价身份有 {references} 条客户端模型关联，覆盖 {clients} 个客户端模型和 {groups} 个分组；保存后全部生效。'` |
    | en-US | `'This pricing identity has {references} client-model associations across {clients} client models and {groups} Groups; saving applies to all of them.'` |
    | ja-JP | `'同じ価格識別子には {references} 件のクライアントモデル関連があり、クライアントモデル {clients} 件、グループ {groups} 件に影響します。保存内容はすべてに反映されます。'` |

12. `web/src/i18n/locales/zh-CN/model-prices.ts:69` 的 `errors.referenced` 把「处配置」改为「处引用」：

    ```
    '该价格仍有 {entries} 处引用，涉及 {groups} 个分组'
    ```

    en-US 与 ja-JP 的同名条目措辞本就中性，**不改**。

13. `relationshipsHelp`（三语）**不改**：新口径下 `price.reference_count` 就是关联行数，文案本来就正确。

### L5 — 一致性探针（用后即删）

14. 新增临时探针 `internal/control/zz_probe_reference_count_test.go`，用 `createPriceTestGroup` 直接写库，对每个形态：建组 → `mustEnsureInitialPrices` → 调 `GetUpstreamModelDetail` → **断言** `detail.Price.ReferenceCount == len(detail.Associations)`（不是打印，是断言），并顺带断言 `ReferenceGroupCount == detail.GroupCount`：

    | 形态 | 配置 |
    | --- | --- |
    | 无别名单条目 | `[{"id":"x"}]` |
    | 普通别名 | `[{"id":"x","aliases":["client-a"]}]` |
    | 后缀别名 | `[{"id":"x","aliases":["client-a[1M]"]}]` |
    | 旧格式单别名字段 | `[{"id":"x","alias":"client-a"}]` |
    | 上游 ID 自带后缀 | `[{"id":"x[1M]"}]` |
    | 同分组多条目同 ID 各带别名 | `[{"id":"x","aliases":["client-a"]},{"id":"x","aliases":["client-b"]}]` |
    | **同分组重复条目** | `[{"id":"x"},{"id":"x"}]` |
    | 跨分组同 ID | 两个分组各配一条 `x` |

    最后一组（重复条目）是写路径有意支持的形态——`normalizeGroupModels` 按「同一个上游 ID 只贡献一个 index」处理多条同 ID 记录，不报冲突（见 `group_write.go` 的注释与 `group_models_test.go` 的永久用例），不是脏数据。它在旧实现下会出现 `associations.length(1) < reference_count(2)`，是本次「共用同一键」的**关键回归点**——新实现下必须为 `1 == 1`。

15. 跑探针，记录每个形态的两个数。任何一行断言失败即为方案不成立的证据，停下来回到 `design.md` 第 3 节。

16. 删除探针文件，确认 `git status --porcelain` 无 `zz_*` 残留。

## 验证命令

```bash
go build ./... && go vet ./... && go test -p 2 -count=1 ./internal/control/...
```

```bash
cd web && pnpm run lint && pnpm run format && pnpm run type-check && pnpm run build
```

前端零改动确认：

```bash
git diff --stat -- web/src
```

除 L4 的四个 i18n 文件外不应有任何 `web/src` 路径出现。

> `gofmt -l` 与 `go mod tidy -diff` 在本仓库会因 CRLF 检出报出全仓库级噪声（未触碰的文件同样被列），不是代码问题。判定方法是比对被列文件去掉 CR 后与 gofmt 输出是否逐字节一致。`go test` 全量并发时可能崩在链接阶段（`Exception 0xc0000005`），用 `-p 2` 重跑。

## 风险文件与回滚点

| 风险点 | 说明 | 回滚 |
| --- | --- | --- |
| `referencedPrice` 结构变更 | 字段换成集合 + 方法。所有读取点由编译期强制跟随，遗漏会直接编译失败 | 恢复结构体与方法调用 |
| `associationKeys` 未初始化 | `:109` 的构造点若漏掉 `make`，首次写入 panic；L5 探针在任一形态即可暴露 | 补 `make` |
| 测试期望值 3→5 | 若推导有误（例如夹具的 `alias` 未按预期并入），测试会失败——这正是它该起的作用 | 按实测值修正并复核推导 |
| 价格列表与重置对话框引用数变大 | **预期的用户可见变化**：带别名配置的引用数从条目数变为关联数 | 语义变化，非缺陷；如需回退则整体 revert |
| 删除保护行为 | `> 0` 判定在新口径下等价（名称集合非空），行为不变 | 无需回滚；L5 与既有测试覆盖 |
| 三语文案 | 只改 `sharedImpact` 三处与 zh `errors.referenced` 一处；改错语言会显示错乱 | 单行回退 |

改动为 5 个 Go 文件（含测试）+ 4 个 i18n 文件，无 schema 变更、无接口字段增删、无 migration。`reference_count` 是每次请求实时计算的派生值，不落库，回滚无需数据修复。第 5 个 Go 文件 `internal/control/model_prices_http_test.go` 不在上表清单内，是实施中实测失败才暴露的第二处旧口径期望值（删除冲突用例的 `reference_count` 3→5），属期望值跟随而非范围外改动。

## 启动前跟进检查

在运行 `task.py start` 之前确认：

1. `referenceCount` 方法化后，全部读取点已由编译确认跟随完毕（L2 第 8 步）。
2. L5 探针的八个形态全部断言通过，尤其是「同分组重复条目」这一存量脏数据形态。
3. L4 文案改动已在三种语言中逐一核对占位符名称（`references` / `clients` / `groups` / `entries`）未变——占位符写错会在运行时显示为原文。
4. `git diff --stat` 中 `web/src` 只出现 4 个 i18n 文件。

## 待更新的 spec（Phase 3.3）

`.trellis/spec/backend/model-name-contract.md` 末尾的 `### Known cross-layer defect`（:146-169）需要改写，且**不能只把「未修复」改成「已修复」**：

1. 标注已修复，写明采用的方案：后端让计数与关联列表共用同一去重键 `priceAssociationKey`，`reference_count` 改为 (分组, 客户端可见名称) 关联数，**前端断言保留且由后端结构性保证成立**。
2. **修正原文那句不准确的措辞**。`reference_count` 不是「client-visible names 的数量」（那是 `client_model_count`），而是「(分组, 名称) 关联数」（即 `associations.length`）。原文措辞与它自己描述的断言互相矛盾，是本次需要裁定的根源；若不改准确，后来者仍会被引向第三种口径。
3. 保留已实测的形态表作为回归基线，补上 L5 新增形态（旧格式单别名字段、同分组重复条目、跨分组）的结果。
4. 说明 `reference_count` 为派生值、不落库，故口径切换无需数据迁移。

## 实施后待补

- 前端若将来引入测试框架，应将「详情响应投影」的字段校验纳入单元测试——本次缺陷正是该类校验所依赖的跨字段关系一度失真而无人守护。
- 价格列表页与重置对话框展示的引用数变大了，若产品认为该处的语义应保持「配置条目数」，需另开字段（`design.md` 方案 D），而不是把 `reference_count` 改回去。
- `model_price_test.go:44` 的期望值 5 现在锁定了「关联对数」语义；未来若有人改后端口径会在此处失败——该断言即为此语义的守护点，勿在重构中删除。
