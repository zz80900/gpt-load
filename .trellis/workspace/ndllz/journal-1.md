# Journal - ndllz (Part 1)

> AI development session journal
> Started: 2026-09-11

---



## Session 1: 分组模型支持多个别名

**Date**: 2026-09-14
**Task**: 分组模型支持多个别名
**Branch**: `main`

### Summary

把模型别名从「单值替换」改为「多值追加」：一个上游模型可配多个别名（上限 10），原模型 ID 始终可路由。后端改 state.ModelConfig 的 Alias 为 Aliases、新增导出的 ExternalModelNames 作为名称顺序不变量唯一来源、路由索引按「名称→上游」注册表注册并合并存量同 ID 多条目、唯一性校验改为名称所有权判定、存储与请求双向兼容旧单别名字段（存储侧不能用 alias_enabled 判定，否则存量别名全丢）。前端新增 TagInput 标签输入组件替换原单输入框+开关，projector 加同规则不变量，import-recovery 升版到 9。验证中确认 gateway/catalog/container/authkey 的 7 个测试失败在干净 HEAD 基线上同样存在，属预存 Windows 环境问题。

### Git Commits

| Hash | Message |
|------|---------|
| `e0d5ff9` | (see git log) |
| `c20b7eb` | (see git log) |
| `b3da5fe` | (see git log) |

### Status

[OK] **Completed**


## Session 2: 请求日志记录 Anthropic Beta 能力标记

**Date**: 2026-09-16
**Task**: 请求日志记录 Anthropic Beta 能力标记
**Branch**: `main`

### Summary

请求日志新增客户端声明的 Anthropic Beta 能力。dialect 从 Anthropic-Beta 请求头与请求体顶层 betas 数组取并集（trim、去重、排序、不归一大小写，任一来源解析失败只丢该来源、绝不报错），经 telemetry.RequestEvent 与 requestlog 投影（逗号分隔串、1024 字节 UTF-8 安全截断）落入 migration 0015 新增的 request_logs.anthropic_betas 列，control 响应暴露 anthropic_betas，前端日志详情展示完整 beta 串，列表与详情的客户端模型行对 context-1m-2025-08-07 显示 1M 徽标。两个关键决策：该字段是客户端自身声明、与 ClientModel 同级，不加入访问密钥作用域的内部字段脱敏；前端 itemFields 是严格白名单，后端加字段而前端未同步会让整个日志接口被判非法，故前后端放在同一提交、必须同版本部署。验证中确认 catalog/container/authkey/webui 的失败全部发生在本次未被触碰的文件上，根因是工作区 CRLF 行尾（release.yml 1702 行、Dockerfile 81 行、docker-compose.yml 30 行、go.mod 132 行、go.sum 342 行全部含 CR，go mod tidy -diff 报的整份 go.sum 差异同源），与本次改动无关；gateway 的两个失败为预存 Windows 平台问题；database_integration_test 的 0015 迁移链断言因缺 GPT_LOAD_DATABASE_TEST_DSN 无法本地验证。

### Git Commits

| Hash | Message |
|------|---------|
| `82bca076` | (see git log) |
| `d2b51c5f` | (see git log) |

### Status

[OK] **Completed**


## Session 3: 模型别名 1M 上下文后缀兼容

**Date**: 2026-09-16
**Task**: 模型别名 1M 上下文后缀兼容
**Branch**: `main`

### Summary

Claude Code 从 /v1/models 读到 xxxx[1M] 判定为 1M 上下文模型，发起请求时却剥离后缀发送 xxxx，而路由索引里此前只有配置中显式写过的名字，基名落到 ReasonNoRouteTarget 最终响应 503。方案是让带后缀名与基名互为等价的可路由名称：新增零内部依赖的 internal/modelname 包承担后缀归一（state 依赖 parameteroverride，反向不可，后缀工具放进 state 会立刻成环），state 内新增 RoutableModelNames（ExternalModelNames 逐项展开后缀基名、派生名紧跟来源项、跳过空基名并去重），而 ExternalModelNames 的形状被前端 groups.ts 硬断言锁死为 [id, ...aliases]，因此派生名只进路由集合不进该函数。关键决策是改索引注册点而非改查找回退：appendExecutionTargets 是唯一注册点，同时喂给 ExecutionCandidates 与 ExecutionRouteCatalog，所以 /v1/models 遍历索引 key 时天然返回两个名称、列表代码无需改动，且巡检路径无需重复回退；冲突检测三个位置刻意不同口径——写路径 normalizeGroupModels 与编译期 validateCompileInput 改为 RoutableModelNames，读路径 validateGroupCollectionModels 保持 ExternalModelNames，因为升级前已存在的别名撞名若在读路径也报错会把分组列表与选项接口打成 500、用户失去修复入口，代价是存在一个窄窗口（保存通过但快照编译失败），该状态可恢复因为 Publish 编译失败时不发布新快照、旧配置继续服务；白名单匹配（scheduler 巡检与 gateway 列表两处）与参数覆盖 matches 改按后缀归一比较，两侧各自保留白名单为空即不限制的外层判断，无后缀时 Base 是恒等函数故既有行为逐字不变。验证上 13 条验收标准中 12 条有自动化覆盖并通过，第 12 条（前端模型页渲染）拆分为两部分：分组模型页依赖 client_models 形状、本任务保持该形状不变故成立，上游模型详情抽屉则属下述既有缺陷。质检阶段用一次性探针在真实服务夹具下实测，定位到一处与本任务无关但被扩大到新形态的跨层缺陷：上游模型详情页的 price.reference_count 按配置条目计数（price_reconcile.go），而前端 models.ts 断言它等于按名称展开的 associations.length，实测边界为无别名 1:1 通过、普通别名 1:2 即失败、单个后缀别名 1:3 失败、上游 ID 自身以 [1M] 结尾且无别名从 1:1 变为 1:2——即该断言自 alias 字段存在的那天起就不成立，与后缀无关，本任务只改变失败时的名称数而非是否失败；同一探针验证 reference_group_count 等于 group_count 与 client_model_count 等于去重后的 client_model 数在四种形态下全部成立，冲突被限定在单一字段口径上。该缺陷本任务不修（修它要么改 reference_count 口径牵动价格对账，要么改前端断言属独立契约变更，均属价格引用计数语义域），已留档于 backend/model-name-contract.md 的 Known cross-layer defect 段与设计工件第 7.1 节待独立任务评审。验证环境方面，全量 go test 首次崩在 gcToolchain 链接阶段的 os/exec CreateProcess（Exception 0xc0000005），改 -p 2 降并行度后跑通；5 个失败包逐包归因，catalog/authkey/webui 三个不依赖本次改动的包分别源于 Windows 文件回滚语义、Windows 路径格式与 CI 文本断言，container 与 gateway 的失败点与改动零交集，日志显示 config_snapshot_publish revision=1 编译成功；gofmt -l 报出 930 个文件与 go mod tidy -diff 报出整份 go.sum 差异均为 CRLF 检出噪声，未触碰的文件同样被列，判定方法是对被列文件去 CR 后与 gofmt 输出逐字节比对全部一致。

### Git Commits

| Hash | Message |
|------|---------|
| `fc7faf5c` | (see git log) |
| `03c828b0` | (see git log) |

### Status

[OK] **Completed**


## Session 4: 统一价格引用计数口径并修正跨层契约

**Date**: 2026-09-16
**Task**: 统一价格引用计数口径并修正跨层契约
**Branch**: `main`

### Summary

修复上游模型详情抽屉在带别名配置下打不开的缺陷：reference_count 由配置条目数改为 (分组, 客户端可见名称) 关联数，与 associations.length 共用 priceAssociationKey 去重键，一致性由结构保证而非巧合，前端断言一条未删。三语 sharedImpact 与 zh-CN errors.referenced 文案跟随，model_price_test.go 与 model_prices_http_test.go 两处期望值 3 改为 5。八形态实测（含旧格式单别名字段、同分组多条同 ID 条目、跨分组）逐字相等。spec 的 Known cross-layer defect 改写为陈述性计数契约并修正 how many client-visible names 这一不准确措辞，guide 增 Mistake 5 与一条跨层检查项。

### Git Commits

| Hash | Message |
|------|---------|
| `7f41ae51` | (see git log) |
| `15fc1afe` | (see git log) |

### Status

[OK] **Completed**
