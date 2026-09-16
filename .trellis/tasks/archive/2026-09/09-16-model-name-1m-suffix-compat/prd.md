# 模型别名 1M 上下文后缀兼容

## Goal

让配置了 1M 上下文后缀（`[1M]` / `[1m]`）的模型别名同时以去除后缀的基名可路由，使 Claude Code 一类客户端「用带后缀名识别 1M 上下文、用基名发起请求」的行为在 gpt-load 上端到端成立，用户不需要把同一个模型配两遍。

用户价值：

- **可用性**：当前配置 `claude-deepseek-flash[1M]` 别名后，客户端请求 `claude-deepseek-flash` 直接吃 503，模型完全不可用。
- **配置成本**：`xxxx[1M]` 与 `xxxx` 语义等价，无需重复维护两条别名。

## Background

### 现象

模型上游 ID 为 `deepseek-flash`，配置别名 `claude-deepseek-flash[1M]`。Claude Code 从 `/v1/models` 看到该名称、判定为 1M 上下文模型，但发起请求时剥离后缀发出 `claude-deepseek-flash`。gpt-load 侧路由查找失败，返回 503 `no_available_candidate`。

### 现状链路

```
/v1/models 列表      internal/gateway/models.go:94-110   遍历 ExecutionCandidates 的 key
索引 key 注册        internal/state/snapshot.go:377-386  由 state.ExternalModelNames 灌入
名称定义源           internal/state/snapshot.go:75       ExternalModelNames = [ID, ...aliases]
请求侧路由查找       internal/scheduler/inspect.go:133-140  byModel[modelKey] 精确匹配，无回退
```

`byModel` 是精确匹配的 map 查找，索引里只存在配置里显式写过的名字，因此基名无路可走，落到 `ReasonNoRouteTarget`，最终由 `internal/gateway/handler.go:1456` 写为 `reasonNoCandidate`（503）。

### 已确认事实

1. **`ExternalModelNames` 的形状被前端硬断言约束**。`web/src/app/resources/groups.ts:443-454` 要求 `client_models` 严格等于 `[id, ...aliases]`（长度必须为 `aliases.length + 1`，顺序逐项相等），否则抛 `InvalidResponseError` 使模型页整体不可用。该断言旁注明确写着「服务端与这里必须同规则」。因此**派生名不能进入 `ExternalModelNames`**，否则必须同步改前端并连带影响 `client_models` 驱动的全部选择器。
2. **路由查找只有一处入口**。`ExecutionCandidates` 的 map 查找集中在 `internal/scheduler/inspect.go` 的 `evaluateTargets`，`ExecutionRouteCatalog`（巡检用）走同一函数。请求路径与巡检路径共用一份逻辑。
3. **索引注册点唯一**。`internal/state/snapshot.go` 的 `appendExecutionTargets` 是 `ExecutionCandidates` 与 `ExecutionRouteCatalog` 共用的注册函数，改一处两处生效。
4. **冲突检测有两处，且都以 `ExternalModelNames` 为口径**。保存期 `internal/control/group_write.go:221-271`（`normalizeGroupModels`，产出 `MODEL_NAME_CONFLICT`）与编译期 `internal/state/snapshot.go:521-533`（`validateCompileInput`）。两处必须与索引注册保持同一名称集合，否则会出现「保存通过、编译失败」或反向的不一致。
5. **访问密钥模型白名单是独立集合，不随模型配置自动扩展**。白名单取值来自 `internal/control/group_options.go:120-131`（= `ExternalModelNames`）与前端缓存 `web/src/app/resources/groups.ts:1000-1006`，因此只可能包含配置名 `xxxx[1M]`。而调度侧 `internal/scheduler/inspect.go:113-120` 与列表侧 `internal/gateway/models.go:99-103` 都按**精确等值**判断请求名是否在白名单内，请求名 `xxxx` 会被拒。
6. **参数覆盖规则按模型名匹配**。`internal/parameteroverride/rules.go:326-336` 以精确或前缀方式比较规则中的模型名与客户端请求名。规则里写 `xxxx[1M]`、请求发 `xxxx` 时规则静默失效。
7. **模型页声明了与 `/v1/models` 一致的不变量**。`internal/control/project_model_collection.go:255-256` 注释明确「每个对外名称都是客户端可用的模型名……与 `/v1/models` 的可见集合一致」。
8. **项目里已出现 `[1m]` 小写形态**。请求日志的 `client_model` 不剥离后缀，`claude-sonnet-4-5[1m]` 会原样入库（见任务 `09-16-request-log-anthropic-beta` 的 Background）。后缀识别必须同时覆盖两种大小写。

## Requirements

- **R1 后缀识别**：名称以 `[1M]` 或 `[1m]` 结尾时（大小写不敏感地匹配 `[1m]`），其去除后缀的基名为有效别名。基名为空时不产生派生名。
- **R2 路由注册**：路由索引（`ExecutionCandidates` 与 `ExecutionRouteCatalog`）为带后缀的名称额外注册基名，两者指向同一上游模型与同一目标。派生名与显式名重复时只注册一次。
- **R3 列表展示**：`/v1/models` 同时返回带后缀名与基名。因此基名必须进入索引，而不是在查找处做回退。模型页（项目模型集合）沿用同一名称集合，保持其「与 `/v1/models` 可见集合一致」的既有不变量。
- **R4 白名单兼容**：访问密钥模型白名单按后缀规范化比较。白名单只配 `xxxx[1M]` 时，请求 `xxxx` 既被放行、也出现在 `/v1/models` 里。
- **R5 参数覆盖兼容**：参数覆盖规则的模型名按规范化后的基名参与匹配，使为 `xxxx[1M]` 配置的规则对请求 `xxxx` 生效。
- **R6 冲突检测**：保存期与编译期的重名检测都以「显式名 + 派生名」为口径。派生基名被另一个模型条目占用时，在保存阶段即报 `MODEL_NAME_CONFLICT`，不允许写入。
- **R7 不变量保持**：`ExternalModelNames` 的返回值形状不变，`client_models` 仍严格等于 `[id, ...aliases]`，前端断言继续成立；`client_models` 与 `group_options` 的模型候选集不包含派生名。
- **R8 配置不被改写**：派生名是运行时的路由等价关系，不写入数据库中的 `aliases`。用户保存 `xxxx[1M]` 后，配置里仍然只有这一个别名。
- **R9 存量数据**：已保存的带后缀别名（升级前写入）无需数据迁移即可获得基名路由能力。

## Acceptance Criteria

- [ ] 分组配置上游 ID `deepseek-flash`、别名 `claude-deepseek-flash[1M]`。以 `claude-deepseek-flash` 发起 Anthropic 协议请求可正常路由并返回上游响应，不再出现 503 `no_available_candidate`。
- [ ] 同一配置下以 `claude-deepseek-flash[1M]` 发起请求同样可路由。
- [ ] 小写形式别名 `claude-deepseek-flash[1m]` 的基名行为与 `[1M]` 完全一致。
- [ ] `/v1/models` 返回的模型集合同时包含 `claude-deepseek-flash[1M]` 与 `claude-deepseek-flash`。
- [ ] 响应体中的模型名回写为客户端实际请求的名称。
- [ ] 访问密钥白名单只勾选 `claude-deepseek-flash[1M]` 时，请求 `claude-deepseek-flash` 被放行，且 `/v1/models` 中两个名称都可见。
- [ ] 为 `claude-deepseek-flash[1M]` 配置的参数覆盖规则对请求 `claude-deepseek-flash` 生效。
- [ ] 另一个模型条目已占用 `claude-deepseek-flash` 时，保存带后缀别名的配置返回 `MODEL_NAME_CONFLICT` 并给出冲突名称与条目下标。
- [ ] 别名恰为 `[1M]`（基名为空）时保存成功且不产生空名路由。
- [ ] 保存别名 `claude-deepseek-flash[1M]` 后再次读取分组模型，`aliases` 仍为 `["claude-deepseek-flash[1M]"]`，`client_models` 仍为 `["deepseek-flash", "claude-deepseek-flash[1M]"]`。
- [ ] 不含 `[1M]`/`[1m]` 后缀的模型，其路由、列表、白名单、参数覆盖行为与改动前完全一致。
- [ ] 前端模型页与分组模型页在含后缀别名的配置下正常渲染，无 `InvalidResponseError`。
- [ ] 存量数据中存在「别名 `xxxx[1M]`」与「另一条目 ID `xxxx`」撞名时，服务不中断（继续以升级前的配置运行），管理后台分组页正常打开，用户删除冲突别名后新配置可发布生效。

## Out of Scope

- **改写用户配置以物化派生别名**。曾评估在保存时把基名写入 `aliases`，好处是前端展示天然一致，代价是用户重读配置会看到自己没写过的别名。采用运行时等价关系（R8）。
- **`[1M]` 之外的后缀**（`[2M]`、`[128K]` 等）。当前只有 1M 上下文存在该客户端约定，按 YAGNI 不做通用后缀机制。
- **`/v1/models/{id}` 单模型查询端点**。gpt-load 未实现该端点，不在本次范围。
- **请求日志 `client_model` 的后缀归一**。日志记录客户端实际发出的名称是正确行为，属于 `09-16-request-log-anthropic-beta` 任务的范围。
- **上游是否真正按 1M 上下文处理**。网关侧不可观测。
- **前端新增后缀提示或说明文案**。
