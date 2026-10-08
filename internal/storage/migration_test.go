package storage

import (
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"

	migrationfiles "gpt-load/internal/storage/migrations"
)

func TestMigrationRegistryContainsOrderedMigrations(t *testing.T) {
	wantIDs := []string{
		migrationfiles.ID0001,
		migrationfiles.ID0002,
		migrationfiles.ID0003,
		migrationfiles.ID0004,
		migrationfiles.ID0005,
		migrationfiles.ID0006,
		migrationfiles.ID0007,
		migrationfiles.ID0008,
		migrationfiles.ID0009,
		migrationfiles.ID0010,
		migrationfiles.ID0011,
		migrationfiles.ID0012,
		migrationfiles.ID0013,
		migrationfiles.ID0014,
		migrationfiles.ID0014ZZ,
		migrationfiles.ID0015,
		migrationfiles.ID0016,
		migrationfiles.ID0017,
		migrationfiles.ID0018,
		migrationfiles.ID0019,
		migrationfiles.ID0020,
		migrationfiles.ID0021,
		migrationfiles.ID0022,
		migrationfiles.ID0023,
		migrationfiles.ID0024,
		migrationfiles.ID0025,
		migrationfiles.ID0026,
		migrationfiles.ID0027,
		migrationfiles.ID0028,
		migrationfiles.ID0029,
		migrationfiles.ID0029ZZ,
	}
	if len(migrations) != len(wantIDs) {
		t.Fatalf("migration registry length = %d, want %d", len(migrations), len(wantIDs))
	}
	for index, entry := range migrations {
		if entry.ID != wantIDs[index] || entry.Up == nil ||
			entry.Validate == nil || entry.ValidateRecoverable == nil {
			t.Fatalf("migration registry entry %d = %#v", index, entry)
		}
	}
}

func TestMigrationRegistryUsesOneOrderedChainForFreshAndExistingDatabases(t *testing.T) {
	entries, calls := testMigrationRegistry()

	fresh := openInternalMigrationTestDatabase(t)
	if err := applyMigrationRegistry(fresh, entries); err != nil {
		t.Fatalf("migrate fresh database: %v", err)
	}
	if !reflect.DeepEqual(*calls, []string{"0001_test", "0002_test"}) {
		t.Fatalf("fresh migration calls = %v, want [0001_test 0002_test]", *calls)
	}

	existing := openInternalMigrationTestDatabase(t)
	if err := existing.AutoMigrate(&schemaMigration{}); err != nil {
		t.Fatalf("create existing migration ledger: %v", err)
	}
	if err := existing.Create(&schemaMigration{ID: entries[0].ID}).Error; err != nil {
		t.Fatalf("record existing migration: %v", err)
	}
	*calls = nil
	if err := applyMigrationRegistry(existing, entries); err != nil {
		t.Fatalf("migrate existing database: %v", err)
	}
	if !reflect.DeepEqual(*calls, []string{"0002_test"}) {
		t.Fatalf("existing migration calls = %v, want [0002_test]", *calls)
	}
}

func TestApplyMigrationRegistryRejectsOutOfOrderEntries(t *testing.T) {
	entries, _ := testMigrationRegistry()
	entries[0], entries[1] = entries[1], entries[0]

	err := applyMigrationRegistry(openInternalMigrationTestDatabase(t), entries)
	if err == nil || !strings.Contains(err.Error(), "migration registry entry 2 has non-ascending ID") {
		t.Fatalf("applyMigrationRegistry() error = %v, want out-of-order registry rejection", err)
	}
}

func TestMigrationRegistryAcceptsAnchoredForkIDs(t *testing.T) {
	entries := []migration{
		registryEntry("0001_initial"),
		registryEntry("0014_affinity_kind"),
		registryEntry("0014_zz_anthropic_betas"),
		registryEntry("0015_group_usage_index"),
	}
	if err := validateMigrationRegistry(entries); err != nil {
		t.Fatalf("validateMigrationRegistry() error = %v, want anchored fork ID accepted", err)
	}
}

func TestMigrationRegistryRejectsNonAscendingIDs(t *testing.T) {
	for name, entries := range map[string][]migration{
		"descending":         {registryEntry("0002_second"), registryEntry("0001_first")},
		"duplicate":          {registryEntry("0001_first"), registryEntry("0001_first")},
		"fork before anchor": {registryEntry("0014_zz_anthropic_betas"), registryEntry("0014_affinity_kind")},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateMigrationRegistry(entries)
			if err == nil || !strings.Contains(err.Error(), "has non-ascending ID") {
				t.Fatalf("validateMigrationRegistry() error = %v, want non-ascending ID rejection", err)
			}
		})
	}
}

func registryEntry(id string) migration {
	return migration{
		ID:                  id,
		Up:                  func(*gorm.DB) error { return nil },
		Validate:            func(*gorm.DB) error { return nil },
		ValidateRecoverable: func(*gorm.DB) error { return nil },
	}
}

func testMigrationRegistry() ([]migration, *[]string) {
	calls := make([]string, 0, 2)
	entry := func(id string) migration {
		return migration{
			ID: id,
			Up: func(*gorm.DB) error {
				calls = append(calls, id)
				return nil
			},
			Validate:            func(*gorm.DB) error { return nil },
			ValidateRecoverable: func(*gorm.DB) error { return nil },
		}
	}
	return []migration{entry("0001_test"), entry("0002_test")}, &calls
}

// publishedLedgerWithoutForkTail 是已发布镜像 v2.0.0-zz.16 写下的账本 ID 序列：
// 上游 0001..0021 加上当时的私有迁移 0014_zz_anthropic_betas，不含任何尾部私有迁移。
//
// 必须按 ID 逐条挑，不能写成「当前注册表的前 N 项」——后者永远等于注册表自己的前缀，
// 因此测不出「私有迁移被插在上游迁移中游」这类错误。
var publishedLedgerWithoutForkTail = []string{
	"0001_initial", "0002_access_key_cost_limits", "0003_remove_observation_fresh_until",
	"0004_usage_stats_group_activity_index", "0005_proxy_config", "0006_error_decision",
	"0007_access_key_lifecycle", "0008_remove_inject_usage_options", "0009_price_multipliers",
	"0010_model_cooldown", "0011_custom_access_keys", "0012_access_key_mask_prefix",
	"0013_validation_protocol", "0014_affinity_kind", "0014_zz_anthropic_betas",
	"0015_group_usage_index", "0016_credential_quota_history", "0017_request_log_operation_index",
	"0018_auto_model", "0019_auto_decision_attribution", "0020_client_model_overrides",
	"0021_request_audit",
}

func migrationByID(id string) (migration, bool) {
	for _, entry := range migrations {
		if entry.ID == id {
			return entry, true
		}
	}
	return migration{}, false
}

// TestMigrationUpgradesPublishedLedger 覆盖真实升级路径：已发布镜像留下的账本 + 新注册表。
//
// 2026-10-08 v2.0.0-zz.17 事故的回归测试：当时私有迁移锚定在 0020、被插到已发布的
// 0021_request_audit 之前，既有实例的账本不再是注册表前缀，启动直接失败
// （schema_migrations contains unknown or non-contiguous migration "0021_request_audit"）。
// 私有迁移只能锚定「当前上游末尾编号」（见 migrations/doc.go）。
func TestMigrationUpgradesPublishedLedger(t *testing.T) {
	t.Parallel()
	entries := make([]migration, 0, len(publishedLedgerWithoutForkTail))
	for _, id := range publishedLedgerWithoutForkTail {
		entry, ok := migrationByID(id)
		if !ok {
			t.Fatalf("published ledger ID %q is missing from the registry", id)
		}
		entries = append(entries, entry)
	}
	db := openInternalMigrationTestDatabase(t)
	if err := applyMigrationRegistry(db, entries); err != nil {
		t.Fatalf("apply published ledger: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("upgrade published ledger: %v", err)
	}
	assertLedgerMatchesRegistry(t, db)
}

// TestMigrationRewritesZz17ForkLedgerID 覆盖另一种真实残留：只有在空库上跑起来过的
// v2.0.0-zz.17 实例才会写下旧的私有迁移 ID，重写后其账本恰好等于新注册表。
func TestMigrationRewritesZz17ForkLedgerID(t *testing.T) {
	t.Parallel()
	db := openInternalMigrationTestDatabase(t)
	if err := applyMigrationRegistry(db, migrations); err != nil {
		t.Fatalf("apply registry: %v", err)
	}
	if err := db.Exec(
		"UPDATE "+migrationLedgerTable+" SET id = ? WHERE id = ?",
		"0020_zz_group_model_auto_sync", migrationfiles.ID0029ZZ,
	).Error; err != nil {
		t.Fatalf("seed v2.0.0-zz.17 ledger ID: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("upgrade v2.0.0-zz.17 ledger: %v", err)
	}
	assertLedgerMatchesRegistry(t, db)
}
