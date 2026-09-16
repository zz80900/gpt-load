package storage

import (
	"fmt"
	"os"
	"testing"

	"gorm.io/gorm"
)

func TestAnthropicBetasMigrationContract(t *testing.T) {
	testAnthropicBetasMigration(t, openInternalMigrationTestDatabase)
}
func TestExternalAnthropicBetasMigrationContract(t *testing.T) {
	dsn := os.Getenv("GPT_LOAD_DATABASE_TEST_DSN")
	if dsn == "" {
		t.Skip("GPT_LOAD_DATABASE_TEST_DSN is not set")
	}
	testAnthropicBetasMigration(t, func(t *testing.T) *gorm.DB { return openExternalIncrementalMigrationDatabase(t, dsn) })
}
func testAnthropicBetasMigration(t *testing.T, open func(*testing.T) *gorm.DB) {
	for _, scenario := range []string{"fresh", "upgrade", "interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			db := open(t)
			if scenario != "fresh" {
				if err := applyMigrationRegistry(db, migrations[:14]); err != nil {
					t.Fatal(err)
				}
				if err := db.Table("request_logs").Create(map[string]any{
					"id": "existing", "completed_at_ms": 1, "access_key_id": 1, "protocol": "anthropic", "client_model": "test", "upstream_model": "test", "status": "success", "status_code": 200, "duration_ms": 1, "error_summary": "", "affinity_hit": true,
				}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "interrupted" && len(migrations) > 14 {
				entry := migrations[14]
				up := entry.Up
				entry.Up = func(tx *gorm.DB) error {
					if err := up(tx); err != nil {
						return err
					}
					return fmt.Errorf("interrupt after anthropic betas DDL")
				}
				registry := append(append([]migration(nil), migrations[:14]...), entry)
				if err := applyMigrationRegistry(db, registry); err == nil {
					t.Fatal("expected interruption")
				}
			}
			if err := AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
			if !db.Migrator().HasColumn("request_logs", "anthropic_betas") {
				t.Fatal("anthropic_betas is missing")
			}
			if scenario != "fresh" {
				var row struct {
					AffinityHit    bool
					AnthropicBetas string
				}
				if err := db.Table("request_logs").Where("id = ?", "existing").Take(&row).Error; err != nil {
					t.Fatal(err)
				}
				if !row.AffinityHit || row.AnthropicBetas != "" {
					t.Fatalf("legacy betas changed: %#v", row)
				}
			}
			if err := db.Table("request_logs").Create(map[string]any{
				"id": "new", "completed_at_ms": 1, "access_key_id": 1, "protocol": "anthropic", "client_model": "test", "upstream_model": "test", "status": "success", "status_code": 200, "duration_ms": 1, "error_summary": "", "affinity_hit": true, "anthropic_betas": "context-1m-2025-08-07",
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
		})
	}
}
