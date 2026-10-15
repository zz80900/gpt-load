# Check: 分组模型自动同步开关与 claude- 前缀别名

> 对照 `prd.md` 的 AC1–AC11 逐条核对（2026-10-08）。阶段 1–5 全部完成，「静态可验」项用代码与测试作证，
> 需要真机/浏览器或真实周期才能验的项在最后一节如实列出。

## AC 核对

| AC | 结论 | 证据 |
|---|---|---|
| AC1 开关只出现在支持 discovery 的分组 | 静态通过 | `GroupModelsPanel.vue` 的 `v-if="channel?.discovery"` 与「同步模型」按钮同条件；后端 `syncGroupModelsOnce` 以 `descriptor.Capabilities.ModelDiscovery` 为闸门，`TestGroupModelAutoSyncSkipsUnsupportedDiscovery` 断言 Cohere（无 ListModels 路由、无订阅发现驱动）不发请求 |
| AC2 默认关闭、落服务端、重载/换浏览器一致 | 静态通过 | 列默认 `false`（迁移 `0029_zz_group_model_auto_sync`）；`TestGroupModelAutoSyncHTTPEndpoint` 断言 GET 默认 `enabled=false`、PUT 往返；前端只从 `GET /model-auto-sync` 读真源，无本地存储 |
| AC3 开关不进草稿、不受模型校验影响 | 静态通过 | 开关走独立 `useQuery` + 即时 PUT；`autoSyncEnabled` 未进入 `draft`/`baseline`/`signature()`/`dirty`，`save()` 路径一行未改（diff 可查） |
| AC4 一个周期后新增上游模型追加在末尾、已下线模型仍在 | 通过 | `mergeAutoSyncModels` 只 append、按 ID 去重、不删除；`TestGroupModelAutoSyncCoordinator` 用伪 ticker 驱动并断言列表为「原行 + 追加行」 |
| AC5 自动同步新增模型带 `claude-<id>` 别名 | 通过 | `claudePrefixedName` + merge；测试断言 `{ID:"fresh", Aliases:["claude-fresh"]}` |
| AC6 手动同步新增模型同样带别名，既有行别名不变 | 静态通过 | `GroupModelSyncDialog.next` 经 `withClaudePrefixedAliases` 生成；`kept` 行原对象原样保留（新数组，不就地改写）。仓库不新增前端测试，故无自动化断言 |
| AC7 ID 本身以 `claude-` 开头时不生成别名 | 通过 | 前后端同规则（大小写不敏感前缀跳过）；`group_model_auto_sync_test.go` 与 `model-aliases` 的实现一致 |
| AC8 冲突时按项降级、同步整体成功 | 通过 | 别名冲突 → 该行退回空别名；ID 被他人认领 → 该行跳过；结果列表必过 `normalizeGroupModels`。测试覆盖「降级」与「跳过」两类 |
| AC9 关掉开关后不再改动该分组 | 通过 | 每轮现查 `auto_sync_models = true`；`TestGroupModelAutoSyncCoordinator` 断言关闭的分组模型不变 |
| AC10 失败隔离、模型不变、有日志 | 通过 | 单组错误只 `Warn` 日志并继续下一组（`TestGroupModelAutoSyncIsolatesGroupFailure`）；无变化不写库、Revision 不变（协调器测试断言 `Revision` 未推进） |
| AC11 抽屉变宽且只影响该面板 | 静态通过 | 新增 `--modern-dialog-sheet-wide-width: 820px` 与 `sheet-wide` 档，只有 `GroupModelsPanel` 使用；`HealthDetailPanel` / `ModelCatalogSettingsDialog` / 其它面板仍走 `sheet`(660px) 或 `default`(540px) |

## 回归面

- `go build -p 1 ./...`、`go vet -p 1 ./...`：通过。
- `go test -p 1 -count=1 ./internal/control/... ./internal/storage/... ./internal/state/... ./internal/scheduler/... ./internal/app/...`：全绿；`internal/gateway`、`internal/container` 仅剩既有环境噪声（CRLF 夹具、真实 TCP 计时、Windows 环境变量大小写）。
- 契约面：`TestSharedAPIResponseFieldsMatchClassicContracts`（classic 白名单）与 `/groups` GET 路由清单测试保持绿，未新增共享 DTO 字段。
- 迁移面：`TestMigrationUpgradesPublishedLedger`（按 ID 拼出的 zz.16 账本可升级）与 `TestMigrationRewritesZz17ForkLedgerID` 绿；`internal/storage/migrations` 三个清单测试绿。
- 前端：`type-check` / `lint`（含 `check:styles`）/ LF 镜像 `prettier --check` / `build` 全绿。

## 已知未验证项（如实记录）

- **浏览器端实际点击验收**：开关的显隐、乐观翻转与失败回滚的观感、加宽后三列是否真的不拥挤（AC11 的视觉部分）、≤460px 容器宽度时工具栏换行行为——均未在真机浏览器里看过。
- **真实周期**：周期固定 1 小时，测试用伪 ticker/timer 驱动，未等过真实周期。
- **前端无自动化测试**：按 `web/README.md` 的仓库约定（不增加前端测试），AC6 的别名生成只有静态证据。
- **前后端口径差异（已记录）**：`claude-<id>` 与同批新增的其它模型 ID 同名时，前端「两行都进、都不带别名」，后端 `mergeAutoSyncModels` 在「新别名先被认领」的顺序下会跳过该行；两种结果都是合法状态，不违反 PRD R3（R3 只要求别名级降级）。
