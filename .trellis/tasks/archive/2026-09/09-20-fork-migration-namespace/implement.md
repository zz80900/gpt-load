# 实施计划：私有迁移号段与上游隔离

## 前置约束

- 改号顺序必须**从大到小**（先 `0018→0017`，再 `0017→0016`，再 `0016→0015`，
  最后 `0015→0014_zz_`），每一步都为下一步腾出低位编号，避免中间态自撞。
- 所有验证命令使用 `-p 1`。本机 Go 工具链在 `-p 2` 及以上会以
  `Exception 0xc0000005` 崩在 gc 阶段（且崩溃非确定性，失败先重跑一次再判定）。

## 1. 迁移文件与常量改名

- [ ] `migrations/0018_request_log_operation_index.go` → `0017_request_log_operation_index.go`
      文件内 `0018` → `0017`（ID / Up / Validate / ValidateRecoverable / 私有类型 / 字符串字面量）
- [ ] `migrations/0018_request_log_operation_index_test.go` → `0017_…_test.go`，同步 `0018` → `0017`
- [ ] `migrations/0017_credential_quota_history.go` → `0016_credential_quota_history.go`，`0017` → `0016`
- [ ] `migrations/0016_group_usage_index.go` → `0015_group_usage_index.go`，`0016` → `0015`
- [ ] `migrations/0015_anthropic_betas.go` → `0014_zz_anthropic_betas.go`：
      `ID0015`→`ID0014ZZ`、`Up0015`→`Up0014ZZ`、`Validate0015`→`Validate0014ZZ`、
      `ValidateRecoverable0015`→`ValidateRecoverable0014ZZ`、
      字符串 `"0015_anthropic_betas"` → `"0014_zz_anthropic_betas"`
- [ ] 用 `git mv` 保留历史

## 2. 注册表与校验器

- [ ] `internal/storage/migration.go` 注册表按 design §2 的顺序与常量更新为 18 项
- [ ] `validateMigrationRegistry`：移除 `number != position`，新增「ID 严格字典序递增」，
      错误文案改为 `has non-ascending ID`
- [ ] 保留正则与 `Up`/`Validate`/`ValidateRecoverable` 非空校验

## 3. 账本改写

- [ ] 新增 `internal/storage/migration_legacy_ids.go`：design §4 的映射表 +
      改写函数（对目标 ID 与其 `#building` 恢复标记一并改写）
- [ ] 在 `applyMigrationsLocked` 中、账本表就绪之后、`ORDER BY id ASC` 读取之前调用
- [ ] 只用可移植 GORM 写法，不写方言分支
- [ ] 改写函数带上「不残留旧 ID 即空操作」的幂等注释

## 4. 既有测试期望更新

- [ ] `database_integration_test.go`：链长 18 与逐项 ID 改为新形态
- [ ] `db_test.go`：`wantMigrationIDs` 列表
- [ ] `migration_test.go`：`wantIDs` 列表
- [ ] `anthropic_betas_migration_test.go`：`migrations[14]` 下标不变，期望 ID 改为
      `0014_zz_anthropic_betas`
- [ ] `quota_history_migration_test.go`：`migrations[:16]` / `migrations[16]` **确认仍正确**
      （quota history 仍在 index 16），必要时只改期望字符串
- [ ] 全仓搜索旧 ID 字面量，确认无遗漏

## 5. 新增测试

- [ ] 校验器：接受 `0014_zz_*` 形态；拒绝乱序（交换两项）与重复 ID
- [ ] 账本改写（SQLite，三个存量中间态 + 全量账本）：
      改写后账本等于新注册表对应前缀，且 `anthropic_betas` 列 / 各索引仍在
- [ ] 幂等：连续两次 `applyMigrations` 均成功且账本不变
- [ ] fresh 安装：账本与注册表逐项一致
- [ ] 恢复标记改写：构造 `0015_anthropic_betas#building` 行，确认被规范为
      `0014_zz_anthropic_betas#building`

## 6. 约定落地

- [ ] 在 `internal/storage/migrations` 包注释（或就近文档）写明私有迁移命名约定
      `<上游当前编号>_zz_<名称>` 及其理由（前缀语义 + 排序规则无关）

## 7. 验证

```bash
# 格式（剔除 CRLF 干扰后比对）
tr -d '\r' < <file> | diff -q - <(gofmt <file>)

go build -p 1 ./...
go vet -p 1 ./...            # 必须跑：_test.go 不参与 build，只有 vet 能暴露测试编译错误
go test -p 1 -count=1 ./internal/storage/...
go test -p 1 -count=1 ./internal/app/...
```

- [ ] 以上全部通过
- [ ] 前端未改动，无需跑 type-check / lint / build

## 风险点与回退

| 项 | 说明 |
|---|---|
| 高危文件 | `internal/storage/migration.go`（所有实例的启动路径）、新增的改写逻辑 |
| 唯一单向门 | 账本改写一旦在真库执行，**退回旧镜像会导致实例无法启动**；退路只有数据库备份 |
| 未实证项 | MySQL / PostgreSQL 路径：三库测试由 `GPT_LOAD_DATABASE_TEST_DSN` 门控，本机未设置且无 Docker，**无法执行**。SQLite 侧可全覆盖；MySQL 恢复标记改写需代码审查确认 |
| 回退方式 | 改动全部在工作区，未提交前可 `git checkout -- .` 整体回退 |

## 开工前复查

- [ ] design §4 的「三种中间态仍是前缀」推演与实现逐条对齐
- [ ] 确认 `#building` 标记改写覆盖到位（MySQL 专用路径，无本地测试兜底）
- [ ] 确认下游没有任何硬编码旧迁移 ID 的位置（含 `internal/app`）
