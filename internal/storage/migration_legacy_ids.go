package storage

import (
	"fmt"

	"gorm.io/gorm"

	migrationfiles "gpt-load/internal/storage/migrations"
)

// legacyMigrationIDs maps the ledger IDs written by the already-published fork
// images (v2.0.0-zz.1 .. zz.9) to the canonical IDs of the current registry.
//
// The fork used to share the upstream NNNN number space, so inserting the
// fork-owned 0015_anthropic_betas pushed every later upstream migration down by
// one (upstream 0015_group_usage_index became 0016, and so on). Renaming the
// private migration to 0014_zz_anthropic_betas lets upstream files be taken
// verbatim again, which means existing instances need their ledger rewritten
// exactly once.
//
// The legacy ID set and the canonical ID set are disjoint, so rows may be
// rewritten in any order without a primary key collision. The legacy IDs are
// permanently dead: upstream never reuses a number, so none of them can come
// back as a canonical ID and the mapping needs no version guard. Rewriting is
// idempotent: once no legacy ID remains every following run is a no-op. The
// MySQL path issues the updates outside a transaction, so a crash between two
// renames leaves a partially rewritten ledger; the next startup finishes the
// job because every rename only fires while its source row still exists.
var legacyMigrationIDs = map[string]string{
	"0015_anthropic_betas":             migrationfiles.ID0014ZZ,
	"0016_group_usage_index":           migrationfiles.ID0015,
	"0017_credential_quota_history":    migrationfiles.ID0016,
	"0018_request_log_operation_index": migrationfiles.ID0017,
	// v2.0.0-zz.17 用一个锚定在 0020 的私有迁移建了分组自动同步开关列。那个位置
	// 插在已发布的上游 0021 之前，会让既有账本不再是注册表前缀、实例起不来，因此
	// 改为锚定当前上游 tip 的 0029_zz_*。只有 zz.17 在空库上跑起来过的实例会留下
	// 旧 ID，重写后其账本恰好等于新注册表。
	"0020_zz_group_model_auto_sync": migrationfiles.ID0029ZZ,
}

// normalizeLegacyMigrationLedger rewrites legacy ledger IDs into their canonical
// form, including the MySQL "<id>#building" resume markers, so an interrupted
// MySQL migration resumes instead of failing the position-based comparison.
//
// It only issues portable GORM updates, so MySQL, PostgreSQL and SQLite take the
// same code path. Callers invoke it inside the migration lock (MySQL and
// PostgreSQL) or the single BEGIN IMMEDIATE transaction (SQLite).
func normalizeLegacyMigrationLedger(db *gorm.DB) error {
	for legacyID, canonicalID := range legacyMigrationIDs {
		renames := []struct{ from, to string }{
			{from: legacyID, to: canonicalID},
			{from: migrationResumeMarker(legacyID), to: migrationResumeMarker(canonicalID)},
		}
		for _, rename := range renames {
			if err := db.Model(&schemaMigration{}).
				Where("id = ?", rename.from).
				Update("id", rename.to).Error; err != nil {
				return fmt.Errorf("normalize legacy migration %q: %w", rename.from, err)
			}
		}
	}
	return nil
}
