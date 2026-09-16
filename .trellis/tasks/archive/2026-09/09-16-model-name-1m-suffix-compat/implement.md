# 实施计划：模型别名 1M 上下文后缀兼容

## 前置约束

- 顺序为**自下而上**（L1 → L5）。L1 是新包，L2 起每一层都依赖前一层的产物。
- **本任务零前端改动**。`ExternalModelNames` 的返回值形状与 `client_models` 的 JSON 形状都不变，`web/` 下任何文件都不应出现在 `git diff` 里。一旦发现前端需要改，说明方案走偏了，应停下来回到 `design.md` 第 8 节重新选路。
- **不新增依赖**。`internal/modelname` 必须零内部依赖，否则会与 `state → parameteroverride` 的既有方向冲突（`design.md` 第 1 节）。
- 保持仓库既有注释风格：Go 注释为中文，导出符号的注释说明「为什么」而非「是什么」。

## 实施清单

### L1 — 新建 `internal/modelname` 包

1. 新增 `internal/modelname/modelname.go`：

   ```go
   // Package modelname owns canonicalization of client-facing model names.
   package modelname

   import "strings"

   // ContextSuffixes 是客户端用于声明扩展上下文窗口的模型名后缀。
   // Claude Code 同时存在 [1M] 与 [1m] 两种写法，因此两种形态都列出。
   var ContextSuffixes = [...]string{"[1M]", "[1m]"}

   // Base 返回剥离上下文后缀后的模型名；不含后缀时原样返回。
   //
   // 长度必须严格大于后缀，因此 [1M] 本身不会被剥成空串——基名为空时不应
   // 产生派生名，否则会在路由索引里留下一个空键。
   func Base(name string) string

   // Allows 报告 allowed 集合是否接受 name：精确命中，或按上下文后缀归一后命中。
   //
   // 两个方向都要覆盖：请求名带后缀时比对其基名（客户端剥离后缀），请求名为
   // 基名时枚举候选（白名单里存的是带后缀名）。空集合的语义由调用方保留，
   // 本函数不对空集合做「放行」特判。
   func Allows(allowed map[string]struct{}, name string) bool
   ```

   实现要点：`Base` 遍历 `ContextSuffixes` 用 `strings.HasSuffix` 精确匹配（两种大小写形态已并列，等价于大小写不敏感）；`Allows` 先试精确命中，`Base` 有变化时比基名，无变化时再枚举 `ContextSuffixes` 拼接的候选。全部是 map 查询，不遍历白名单。

2. 新增 `internal/modelname/modelname_test.go`，覆盖：
   - `Base` 无后缀名恒等；`xxxx[1M]` / `xxxx[1m]` 都得 `xxxx`；`[1M]` 自身不被剥离；`[1M][1M]` 得 `[1M]`；空串得空串；后缀出现在中间（`xx[1M]yy`）不剥离。
   - `Allows` 空集合一律 false；白名单存带后缀名时 `xxxx` 与 `xxxx[1M]` 都通过；白名单存基名时 `xxxx[1M]` 通过；不相关名称不通过。

### L2 — `state` 包：名称集合与索引

3. `internal/state/snapshot.go`，紧邻 `ExternalModelNames`（`:75`）新增：

   ```go
   // RoutableModelNames 返回模型在路由索引中认领的全部名称：ExternalModelNames
   // 的每一项，其后紧跟该项去掉上下文后缀的基名。
   //
   // 与 ExternalModelNames 的分工：后者是对客户端可见的名称集合，其形状被前端
   // client_models 断言锁死，不能含派生名；本函数额外展开后缀基名，供路由索引、
   // 写路径与编译期冲突检测使用。
   //
   // 派生名紧跟来源项之后（[id, id基名, 别名1, 别名1基名, ...]），使模型页成对
   // 展示；无后缀的项不产生派生名，重复项只保留首次出现。
   func RoutableModelNames(model ModelConfig) []string
   ```

   实现：以 `ExternalModelNames(model)` 为基准顺序，用一个 `seen` map 去重（派生名可能与后面的显式名同名，如 `id=xxxx`、`alias=xxxx[1M]`），`Base(name) == name` 时跳过。

4. `internal/state/snapshot.go:379`（`appendExecutionTargets` 内）：`ExternalModelNames(model)` 换成 `RoutableModelNames(model)`。**这是让 503 消失的核心改动**，同时喂给 `ExecutionCandidates` 与 `ExecutionRouteCatalog`。既有的 `claimed` 去重逻辑对派生名同样生效，无需改动。

5. `internal/state/snapshot.go:527`（`validateCompileInput` 内）：同样换成 `RoutableModelNames(model)`，并让错误信息能区分两种重名：

   - 名称在该模型的 `ExternalModelNames` 里 → 沿用现有的 `duplicate external model %q` 文案（显式重名）。
   - 否则说明这是派生名，需说明它由哪个模型的哪个名称经后缀剥离得到，以及当前被谁占用。用户配置里找不到这个名字，不解释就无法定位（`design.md` 第 7 节）。

6. `internal/state/snapshot_test.go` 补充用例：
   - 别名 `public[1M]` 时索引同时含 `public[1M]` 与 `public`，且都指向同一上游 ID。
   - 派生名与他模型显式名撞名时 `Compile` 返回错误，错误信息包含派生来源。
   - 无后缀模型：`RoutableModelNames` 与 `ExternalModelNames` 返回同一集合（逐项相等）。
   - **回归**：既有断言全部保持原值（尤其 `group_models_test.go:361-368` 的 `len(targets) != 4`——那 4 个名字都无后缀，数量不应变化）。

### L3 — `control` 包：写路径与模型页

7. `internal/control/group_write.go:238`（`normalizeGroupModels`）：`append([]string{id}, aliases...)` 换成 `state.RoutableModelNames(state.ModelConfig{ID: id, Aliases: aliases})`。传规范化后的 `aliases`（`normalizeModelAliases` 的产物），不要传原始 `value.Aliases`。检查该文件的 import 是否已含 `gpt-load/internal/state`，缺则补。

   **不要改动 `ModelNameConflict` 与 `ModelNameConflictData` 的 JSON 形状**——冲突项返回派生基名已足够定位（用户能看到冲突的两个条目下标），加字段是前端契约变更，超出本任务范围。

8. `internal/control/project_model_collection.go:257` 与 `:493`：两处 `state.ExternalModelNames(state.ModelConfig{...})` 换成 `state.RoutableModelNames(...)`，维持该文件第 255-256 行声明的「与 `/v1/models` 可见集合一致」不变量。

9. `internal/control/group_models_test.go` / `project_model_collection_test.go` 补充用例：
   - 含后缀别名的配置：保存成功后从库里重读，`aliases` 仍是 `["claude-deepseek-flash[1M]"]`、`client_models` 仍是 `["deepseek-flash", "claude-deepseek-flash[1M]"]`（R8 + R7）。
   - 另一条目已占用派生基名时，保存返回 `MODEL_NAME_CONFLICT`，冲突项含该基名（R6）。
   - 别名恰为 `[1M]` 时保存成功，且索引里不出现空键。

### L4 — 白名单匹配（两处）

10. `internal/scheduler/inspect.go:113-120`：

    ```go
    if len(query.accessKey.Filters.Models) > 0 {
        if query.externalModel == nil {
            return []targetDecision{}, ReasonModelRequiredByFilter, nil
        }
        if !modelname.Allows(query.accessKey.Filters.Models, *query.externalModel) {
            return []targetDecision{}, ReasonModelFiltered, nil
        }
    }
    ```

    「白名单为空即不限制」的外层判断必须保留——`Allows` 不对空集合特判。

11. `internal/gateway/models.go:99-103`：同一替换，`accessKey.Filters.Models` 与 `modelID` 传给 `modelname.Allows`。

12. `internal/scheduler/inspect_test.go` 与 `internal/gateway/models_test.go` 补充：白名单只含 `xxxx[1M]` 时，`xxxx` 放行且出现在 `/v1/models`；白名单含无关名称时 `xxxx` 仍被拒（`ReasonModelFiltered`）。

### L5 — 参数覆盖匹配

13. `internal/parameteroverride/rules.go:326-337`（`rule.matches`）：在 `entry.model == ""` 的提前返回之后，把 `entry.model` 与 `clientModel` 各自过一遍 `modelname.Base` 再比较；`modelPrefix` 分支的 `HasPrefix` 语义不变。

    ```go
    model := modelname.Base(entry.model)
    clientModel = modelname.Base(clientModel)
    if entry.modelPrefix {
        return strings.HasPrefix(clientModel, model)
    }
    return clientModel == model
    ```

    该文件目前只 import `execution` 与 `protocol`，需补 `gpt-load/internal/modelname`。`modelname` 零内部依赖，不会与 `state → parameteroverride` 形成环。

    注意语义变化：前缀模式下规则写 `xxxx[1M]` 会退化为前缀 `xxxx`，比改动前更宽松。这是后缀等价关系的自然结果，可接受，无需特殊处理。

14. `internal/parameteroverride/rules_test.go` 补充：规则 `xxxx[1M]` 精确模式对请求 `xxxx` 生效；规则 `xxxx` 对请求 `xxxx[1M]` 生效；无后缀场景的既有用例断言不变。

## 验证命令

L1 完成后：

```bash
go build ./... && go test -count=1 ./internal/modelname/...
```

L2 完成后（这是最关键的一层，索引行为在此定型）：

```bash
go test -count=1 ./internal/state/...
```

L3–L5 完成后：

```bash
go test -count=1 ./internal/control/... ./internal/scheduler/... ./internal/gateway/... ./internal/parameteroverride/...
```

最终全量门禁（含 gofmt / go mod tidy -diff / go vet / 前端 lint+format+build / go build / go test / git diff --check）：

```bash
make check
```

本任务不改前端，`make check` 的前端步骤预期全程无 diff。

## 风险文件与回滚点

| 风险点 | 说明 | 回滚 |
| --- | --- | --- |
| `internal/state/snapshot.go` 的 `RoutableModelNames` 与两处换用 | 同时影响数据面路由与 `/v1/models` 列表，是本任务唯一改变运行时行为的层 | 单文件 + 新包删除，其余各层改动随之失去作用，可一并回退 |
| `validateCompileInput` 的错误分支 | 派生名冲突会让新快照编译失败。`Publish` 编译失败时不发布（`internal/state/manager.go:84-97`），旧配置继续服务，不会打挂服务；但用户会看到「保存成功、配置不生效」 | 撤销 L2 步骤 5 的换用，恢复只检测显式名 |
| 存量数据撞名（升级前已存在「别名 `xxxx[1M]`」与「他条目 id `xxxx`」） | 升级后该配置无法编译。读路径不受影响（`validateGroupCollectionModels` 保持原集合），用户能在管理后台看到并删除冲突别名完成修复 | 无需回滚：这是配置冲突而非代码缺陷，用户侧可自解 |
| `internal/control/group_write.go` 的 `ModelNameConflict` | 派生名被他条目占用时，保存被拒且冲突项是用户没写过的名字 | 前端契约未变，回退该行即恢复原行为 |
| `parameteroverride` 的 `matches` | 前缀模式下规则后缀会被剥离，匹配范围比改动前宽 | 单函数回退，无状态残留 |

改动集中在 6 个文件（1 新增 + 5 修改），无 schema 变更、无 migration、无配置改写。回滚即 `git revert`。

## 启动前跟进检查

在运行 `task.py start` 之前确认：

1. PRD 的验收标准与 `design.md` 的改动点表一致，无残留 Open Question。最后一条（存量撞名时可恢复）需要真实构造一次脏数据才能验，确认测试环境允许。
2. 三份规划工件就位（复杂任务必需）。
3. `implement.jsonl` 与 `check.jsonl` 中的条目为真实内容，非种子 `_example` 行。
4. 手工验收所需的 `deepseek-flash` 上游与可写别名的分组已就绪，能真实发一次 Anthropic 协议请求核对 503 消失与列表双名称。

## 实施后待补

- 编译期派生重名的错误信息若最终走了结构化路径（而非仅文案），需同步评估前端提示是否需要区分，当前按纯文案处理。
- `ContextSuffixes` 保持为字面量表。若将来出现 `[2M]`、`[128K]` 一类需求，先确认客户端是否同样剥离后缀，再决定扩展表还是做通用机制。
- 若派生名撞名的存量场景在实践中真的出现，可考虑在分组列表接口暴露一个只读的「派生名」提示，让用户在保存前就看到冲突，而不是等保存被拒。

