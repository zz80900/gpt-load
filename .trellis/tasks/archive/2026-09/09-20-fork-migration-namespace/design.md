# 设计：私有迁移号段与上游隔离

## 1. 命名约定（本设计的核心）

| 归属 | 形态 | 示例 |
|---|---|---|
| 上游迁移 | `<NNNN>_<snake_name>`，**与上游逐字相同** | `0015_group_usage_index` |
| 私有迁移 | `<NNNN>_zz_<snake_name>`，`NNNN` = 创建时上游的最新编号 | `0014_zz_anthropic_betas` |

私有迁移的 ID 必须在**字典序上紧邻它挂靠的上游迁移之后**。`_zz_` 标记同时满足两点：

1. 上游永远不会产出 `_zz_` 形态的 ID → 永不撞号；
2. 与相邻上游迁移的判别字符落在字母位（`a` < `z`），不落在下划线位，因此
   **不依赖数据库排序规则**（SQLite 的 BINARY、MySQL 的 `_ci`、PostgreSQL 的
   glibc collation 对 `_` 的权重不同，但都不影响字母位比较）。

Go 侧常量同样带 `ZZ` 后缀，避免与上游按编号派生的标识符冲突：

```go
const ID0014ZZ = "0014_zz_anthropic_betas"
func Up0014ZZ(db *gorm.DB) error
func Validate0014ZZ(db *gorm.DB) error
func ValidateRecoverable0014ZZ(db *gorm.DB) error
```

## 2. 目标注册表

```
index  ID                              来源
  0..13  0001_initial … 0014_affinity_kind      上游，逐字
   14    0014_zz_anthropic_betas               私有（原 0015_anthropic_betas）
   15    0015_group_usage_index                上游（原本地 0016）
   16    0016_credential_quota_history         上游（原本地 0017）
   17    0017_request_log_operation_index      上游（原本地 0018）
```

字典序自检：`0014_affinity_kind` < `0014_zz_anthropic_betas` < `0015_group_usage_index`
< `0016_…` < `0017_…` ✓

副作用（正向）：`anthropic_betas_migration_test.go:36` 的 `migrations[14]` 与
`quota_history_migration_test.go` 的 `migrations[:16]` / `migrations[16]` **下标均不变**，
只需改期望的 ID 字符串。

## 3. 校验器变更

`internal/storage/migration.go:153` `validateMigrationRegistry`：

- 保留：ID 正则校验（`0014_zz_anthropic_betas` 仍匹配原正则，无需改动）、
  `Up`/`Validate`/`ValidateRecoverable` 非空校验；
- **移除**：`number != position` 的「编号 == 位置」约束；
- **新增**：ID 严格字典序递增（与账本 `ORDER BY id ASC` 的读取顺序对齐）。

新不变式的语义：*注册表顺序 == ID 字典序*。这正是位置化账本比对所依赖的全部前提，
比原约束更弱也更准确。

错误文案改为 `migration registry entry %d has non-ascending ID %q`。
注意 `db_test.go:723` 断言的是**账本**错误文案（`unknown or non-contiguous migration`，
`migration.go:202`），不在本次改动范围内，保持不动。

## 4. 账本改写（存量库兼容）

### 映射表

```
0015_anthropic_betas             -> 0014_zz_anthropic_betas
0016_group_usage_index           -> 0015_group_usage_index
0017_credential_quota_history    -> 0016_credential_quota_history
0018_request_log_operation_index -> 0017_request_log_operation_index
```

### 为什么安全（已逐条推演）

1. **无主键冲突**：目标 ID 集合与旧 ID 集合无交集，故改写顺序任意，即便逐行 UPDATE 也不会互撞。
2. **前缀语义保持**：改写后按 ID 重排，账本仍是新注册表的前缀。三种存量中间态逐一验证：
   - 仅 `{…0014, 0015_anthropic_betas}` → `{…0014, 0014_zz_anthropic_betas}`，是新注册表前缀 ✓
   - `{…0014, 0015_anthropic_betas, 0016_group_usage_index}`
     → `{…0014, 0014_zz_anthropic_betas, 0015_group_usage_index}` ✓
   - zz.9 全量账本 → 与新注册表逐项相等 ✓
3. **幂等**：改写后不残留任何旧 ID，再次执行即空操作。
4. **MySQL 恢复标记**：`migration_recovery.go:47` 的 `<id>#building` 也需一并改写，
   否则中断态会在位置比对时炸掉。映射表对 `<id>#building` 同样适用。

### 执行位置

放在 `applyMigrationsLocked` 内、账本表确保存在之后、`ORDER BY id ASC` 读取（`migration.go:190`）之前。
该函数已经在各自的事务/锁作用域内被调用（MySQL/PG 走连接锁 `migration.go:133`，
SQLite 走单 `BEGIN IMMEDIATE` `migration_sqlite.go:58`），因此改写天然受锁保护，
与其他迁移原子提交。

实现使用可移植写法（GORM `Model(&schemaMigration{}).Where("id = ?", old).Update("id", new)`），
三方言行为一致；不写方言分支。

## 5. 兼容性与回滚

- **前向**：旧镜像 → 新镜像，账本被改写，正常启动。
- **回滚是单向门**：改写后若退回旧镜像，旧注册表期望 `0016_group_usage_index`
  位于 index 15，而账本已是 `0015_group_usage_index`，位置比对失败 → **实例起不来**。
  唯一退路是数据库备份。此风险需在发布说明中明确。

## 6. 后续私有迁移的约定

新增私有迁移时，命名 `<新增时的上游最新编号>_zz_<名称>`，插在该上游迁移之后。
这样对「先应用私有迁移、后应用后续上游迁移」的中间态实例，账本仍是前缀。
此约定需写入 `internal/storage/migrations` 的包注释或仓库文档，避免后人重新踩坑。

## 7. 风险与未决

| 风险 | 说明 | 处置 |
|---|---|---|
| MySQL / PostgreSQL 路径无法本机验证 | 三库测试由 `GPT_LOAD_DATABASE_TEST_DSN` 门控，本机未设置，且**未安装 Docker**，无法起真库 | 改写逻辑刻意只用可移植 GORM 写法、不写方言分支；SQLite 侧覆盖中间态与幂等测试。MySQL 恢复标记的改写需**代码审查确认**，并在发布说明中列为未实证项 |
| 排序规则差异 | 不同库对 `_` 权重不同 | 判别字符一律落在字母位（见 §1），已规避 |
| 迁移位置误判 | 位置化账本对顺序极敏感 | 校验器新增字典序递增约束，任何顺序错误在启动即报错而非静默 |

## 8. 影响文件

- `internal/storage/migration.go` — 校验器 + 注册表 + 调用改写
- `internal/storage/migration_legacy_ids.go`（新增）— 改写映射与实现
- `internal/storage/migrations/0015_anthropic_betas.go` → `0014_zz_anthropic_betas.go`（含常量改名）
- `internal/storage/migrations/0016_group_usage_index.go` → `0015_group_usage_index.go`
- `internal/storage/migrations/0017_credential_quota_history.go` → `0016_credential_quota_history.go`
- `internal/storage/migrations/0018_request_log_operation_index.go` → `0017_request_log_operation_index.go`（含 `_test.go`）
- 测试：`database_integration_test.go`、`db_test.go`、`migration_test.go`、
  `anthropic_betas_migration_test.go`、`quota_history_migration_test.go`
- 新增测试：校验器（接受 `_zz_`、拒绝乱序/重复）、账本改写（三种中间态 + 幂等 + fresh）
