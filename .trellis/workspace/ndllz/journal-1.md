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
