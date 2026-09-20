# 私有迁移号段与上游隔离

## Goal

让私有（fork）迁移不再与上游争抢同一套 `NNNN` 编号，使每次合并上游代码时
`internal/storage` 的迁移注册表与账本断言不再冲突，也不再需要把上游迁移逐个顺延。

用户价值：合并上游从「每次手工重编号 + 改 4 处断言」降为「上游原文照收」。

## Background

### 冲突根因

`internal/storage/migration.go:153` 的 `validateMigrationRegistry` 要求迁移的 4 位编号
**必须等于其在注册表中的 1-based 位置**：

```go
number, err := strconv.Atoi(matches[1])
if err != nil || number != position {   // position == index+1
    return fmt.Errorf("migration registry entry %d has non-contiguous ID %q", ...)
}
```

该不变式迫使上游与私有方共用一条全局序号：私有方在中间插入一个编号后，上游其后的
**所有**迁移都要顺延，且上游每次新增迁移都会与私有方已占用的编号再次相撞。

### 账本语义（决定兼容边界）

`internal/storage/migration.go:189` 起：账本以 `ORDER BY id ASC` 读出已应用 ID（`:190`）；
该列表必须与注册表**按下标逐项相等**，否则报
`schema_migrations contains unknown or non-contiguous migration %q`（`:200`）；
待执行集合为 `entries[len(applied):]`（`:215`）。

推论：**注册表顺序必须等于 ID 的字典序，且已应用集合必须是注册表的前缀**。

### fork 自有迁移只有一个

对比 `git ls-tree upstream/main internal/storage/migrations/` 与本地：

| 本地文件 | 来源 |
|---|---|
| `0015_anthropic_betas.go` | **fork 自有**（提交 `82bca076`，2026-09-16），为 `request_logs` 增加 `anthropic_betas` 列 |
| `0016_group_usage_index.go` | 上游 `0015_group_usage_index`，为避让 fork 被顺延 |
| `0017_credential_quota_history.go` | 上游 `0016_credential_quota_history`，顺延 |
| `0018_request_log_operation_index.go` | 上游 `0017_request_log_operation_index`，顺延 |

上游自身序列：`0014_affinity_kind` → `0015_group_usage_index` →
`0016_credential_quota_history` → `0017_request_log_operation_index`。

### 存量账本现状

镜像 `v2.0.0-zz.1`–`zz.9` 已发布，部署实例的 `schema_migrations` 中可能已存在
`0015_anthropic_betas`、`0016_group_usage_index`、`0017_credential_quota_history`、
`0018_request_log_operation_index`。

### 其它相关机制

- MySQL 逐条迁移并写恢复标记 `<id>#building`（`internal/storage/migration_recovery.go:47`）
- SQLite 在单个 `BEGIN IMMEDIATE` 内跑完整条链、事务外关外键（`internal/storage/migration_sqlite.go:16`）
- 断言整条链的测试：`database_integration_test.go`、`db_test.go`、`migration_test.go`；
  按切片下标定位的 `anthropic_betas_migration_test.go:36`（`migrations[14]`）、
  `quota_history_migration_test.go:60`（`migrations[16]`）
- `db_test.go:723` 断言账本错误文案 `unknown or non-contiguous migration`，本次不改该文案

## Requirements

- R1 私有迁移使用上游不可能产出的命名：
  私有迁移命名为 `<NNNN>_zz_<snake_name>`（`NNNN` = 创建时上游的最新编号），
  并紧邻该上游迁移之后排序。Go 常量同步带 `ZZ` 后缀（`ID0014ZZ` / `Up0014ZZ` /
  `Validate0014ZZ` / `ValidateRecoverable0014ZZ`），避免与上游按编号派生的标识符相撞。
- R2 上游迁移在本地保持**与上游逐字相同的编号与文件名**，合并时原文照收，不再顺延。
- R3 存量实例升级后能正常启动：账本被一次性、幂等地改写为规范 ID（含 MySQL 的
  `<id>#building` 恢复标记）。
- R4 注册表顺序与 ID 字典序一致、已应用集合仍为前缀；不改变 MySQL 恢复标记、
  SQLite 单事务、外键校验等既有语义。
- R5 校验器由「编号 == 位置」改为「ID 严格字典序递增且唯一」，使 R1/R2 成立；
  判别字符须落在字母位而非下划线位，以规避各库对 `_` 权重不同的排序规则差异。
- R6 新约定落地为代码注释/文档，使后续新增私有迁移不再重踩。

## Acceptance Criteria

- [ ] `validateMigrationRegistry` 不再要求「编号 == 位置」，改为校验 ID 严格递增且唯一，
      错误文案为 `has non-ascending ID`
- [ ] `0016_group_usage_index` → `0015_group_usage_index`、
      `0017_credential_quota_history` → `0016_credential_quota_history`、
      `0018_request_log_operation_index` → `0017_request_log_operation_index`
      （文件名、常量、注册表、断言全部同步）
- [ ] `0015_anthropic_betas` 规范为 `0014_zz_anthropic_betas`（文件名、`ID0014ZZ` 等常量、
      注册表），且仍位于 `migrations[14]`
- [ ] 账本改写对 sqlite 生效，且**重复执行无副作用**
- [ ] fresh 安装账本与注册表逐项一致
- [ ] 三种存量中间态升级后账本均为新注册表前缀：
      仅 `{…0014, 0015_anthropic_betas}`、`{…0014, 0015_anthropic_betas, 0016_group_usage_index}`、
      zz.9 全量账本
- [ ] 升级后 `anthropic_betas` 列与 `request_logs` 相关索引均仍在
- [ ] 恢复标记 `0015_anthropic_betas#building` 被规范为 `0014_zz_anthropic_betas#building`
- [ ] `go build -p 1 ./...`、`go vet -p 1 ./...`、`./internal/storage/...` 与
      `./internal/app/...` 测试通过

## Out of Scope

- 不改动上游迁移自身的 DDL / 语义
- 不引入第二套迁移框架或独立账本表
- 不把账本比对从「前缀」改为「子集」（会加大与上游 runner 的分歧）
- 不处理 `v2.0.0-rc.*` 与上游同名标签的分歧（另行处理）

## Risks

- **单向门**：账本改写后若退回旧镜像，旧注册表的位置比对会失败，实例无法启动；
  退路只有数据库备份。需写入发布说明。
- **未实证项**：MySQL / PostgreSQL 路径无法本机验证——三库测试由
  `GPT_LOAD_DATABASE_TEST_DSN` 门控，本机未设置且未安装 Docker。
  SQLite 侧全覆盖；MySQL 恢复标记改写依赖代码审查确认。
