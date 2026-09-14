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
