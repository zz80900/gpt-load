package storage

import (
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"

	migrationfiles "gpt-load/internal/storage/migrations"
)

// legacyForkLedgerIDs are the ledger IDs written by the published fork images
// (v2.0.0-zz.1 .. zz.9). They are literals on purpose: the expectation must not
// drift together with the production mapping.
var legacyForkLedgerIDs = []string{
	"0015_anthropic_betas",
	"0016_group_usage_index",
	"0017_credential_quota_history",
	"0018_request_log_operation_index",
}

// canonicalForkLedgerIDs are the canonical counterparts of legacyForkLedgerIDs.
// They are spelled out so a registry renumbering cannot quietly change what the
// published fork ledgers are rewritten into.
var canonicalForkLedgerIDs = []string{
	migrationfiles.ID0014ZZ,
	migrationfiles.ID0015,
	migrationfiles.ID0016,
	migrationfiles.ID0017,
}

func TestLegacyForkLedgerIsNormalizedOnUpgrade(t *testing.T) {
	for _, state := range []struct {
		name    string
		applied int
	}{
		{name: "before the fork migration", applied: 14},
		{name: "only the fork migration", applied: 15},
		{name: "through the group usage index", applied: 16},
		{name: "full zz.9 ledger", applied: 18},
	} {
		t.Run(state.name, func(t *testing.T) {
			db := openInternalMigrationTestDatabase(t)
			seedLegacyForkLedger(t, db, state.applied)

			// The rewrite runs on every startup, so a second pass must be a
			// no-op that leaves the canonical ledger untouched.
			for range 2 {
				if err := AutoMigrate(db); err != nil {
					t.Fatalf("AutoMigrate() error = %v", err)
				}
				assertLedgerMatchesRegistry(t, db)
				assertNoLegacyLedgerIDs(t, db)
			}
			assertForkSchemaPreserved(t, db)
		})
	}
}

func TestFreshLedgerMatchesRegistryAfterNamespaceChange(t *testing.T) {
	db := openInternalMigrationTestDatabase(t)
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate() error = %v", err)
	}
	assertLedgerMatchesRegistry(t, db)
	assertNoLegacyLedgerIDs(t, db)
	assertForkSchemaPreserved(t, db)
}

// TestLegacyForkResumeMarkerIsNormalized drives the MySQL resume marker rewrite
// directly. MySQL DDL auto-commits, so an instance interrupted while applying a
// fork-era migration is left with "<id>#building" in the ledger instead of the
// migration row. Without the rewrite the position comparison in
// applyMigrationsLocked would pair that marker with the wrong registry entry and
// refuse to start. The rewrite itself is what is asserted here; consuming the
// marker afterwards is MySQL-only (applyMySQLMigration) and is covered by the
// recovery tests.
func TestLegacyForkResumeMarkerIsNormalized(t *testing.T) {
	if len(canonicalForkLedgerIDs) != len(legacyForkLedgerIDs) {
		t.Fatalf(
			"legacy fixture length = %d, want %d",
			len(canonicalForkLedgerIDs),
			len(legacyForkLedgerIDs),
		)
	}
	for index, legacyID := range legacyForkLedgerIDs {
		t.Run(legacyID, func(t *testing.T) {
			canonical := canonicalForkLedgerIDs[index]
			if got := canonicalForLegacyForkID(legacyID); got != canonical {
				t.Fatalf("legacy ID %q maps to %q, want %q", legacyID, got, canonical)
			}
			db := openInternalMigrationTestDatabase(t)
			if err := db.AutoMigrate(&schemaMigration{}); err != nil {
				t.Fatalf("create migration ledger: %v", err)
			}
			if err := db.Create(&schemaMigration{ID: migrationResumeMarker(legacyID)}).Error; err != nil {
				t.Fatalf("seed legacy resume marker: %v", err)
			}
			want := []string{migrationResumeMarker(canonical)}
			for range 2 {
				if err := normalizeLegacyMigrationLedger(db); err != nil {
					t.Fatalf("normalizeLegacyMigrationLedger() error = %v", err)
				}
				var ids []string
				if err := db.Table(migrationLedgerTable).Order("id ASC").Pluck("id", &ids).Error; err != nil {
					t.Fatalf("read ledger: %v", err)
				}
				if !reflect.DeepEqual(ids, want) {
					t.Fatalf("ledger = %v, want %v", ids, want)
				}
			}
		})
	}
}

// canonicalForLegacyForkID resolves the registry entry a legacy fork ledger ID
// was renamed into.
func canonicalForLegacyForkID(legacyID string) string {
	for canonical, legacy := range legacyForkLedgerTargets() {
		if legacy == legacyID {
			return canonical
		}
	}
	return ""
}

// seedLegacyForkLedger applies the canonical prefix up to applied entries and
// then rewrites the fork migration rows back to the IDs the published fork
// images wrote, reproducing the exact ledger an existing instance carries.
func seedLegacyForkLedger(t *testing.T, db *gorm.DB, applied int) {
	t.Helper()
	if err := applyMigrationRegistry(db, migrations[:applied]); err != nil {
		t.Fatalf("apply canonical prefix %d: %v", applied, err)
	}
	targets := legacyForkLedgerTargets()
	for index := 14; index < applied; index++ {
		canonical := migrations[index].ID
		legacy, ok := targets[canonical]
		if !ok {
			t.Fatalf("canonical migration %q has no recorded legacy ID", canonical)
		}
		if err := db.Exec(
			"UPDATE "+migrationLedgerTable+" SET id = ? WHERE id = ?",
			legacy,
			canonical,
		).Error; err != nil {
			t.Fatalf("seed legacy ledger ID %q: %v", legacy, err)
		}
	}
}

func legacyForkLedgerTargets() map[string]string {
	canonical := []string{
		migrations[14].ID,
		migrations[15].ID,
		migrations[16].ID,
		migrations[17].ID,
	}
	targets := make(map[string]string, len(canonical))
	for index, id := range canonical {
		targets[id] = legacyForkLedgerIDs[index]
	}
	return targets
}

func assertLedgerMatchesRegistry(t *testing.T, db *gorm.DB) {
	t.Helper()
	var ids []string
	if err := db.Table(migrationLedgerTable).Order("id ASC").Pluck("id", &ids).Error; err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if want := registeredMigrationIDs(); !reflect.DeepEqual(ids, want) {
		t.Fatalf("ledger = %v, want %v", ids, want)
	}
}

func assertNoLegacyLedgerIDs(t *testing.T, db *gorm.DB) {
	t.Helper()
	var ids []string
	if err := db.Table(migrationLedgerTable).Pluck("id", &ids).Error; err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	for _, id := range ids {
		for _, legacy := range legacyForkLedgerIDs {
			if strings.Contains(id, legacy) {
				t.Fatalf("ledger still contains legacy ID %q", id)
			}
		}
	}
}

func assertForkSchemaPreserved(t *testing.T, db *gorm.DB) {
	t.Helper()
	if !db.Migrator().HasColumn("request_logs", "anthropic_betas") {
		t.Fatal("request_logs.anthropic_betas is missing after the ledger rewrite")
	}
	for _, index := range []string{
		"idx_request_logs_group_completed",
		"idx_request_logs_operation_completed_id",
	} {
		if !db.Migrator().HasIndex("request_logs", index) {
			t.Fatalf("request_logs index %q is missing after the ledger rewrite", index)
		}
	}
	if !db.Migrator().HasTable("credential_quota_histories") {
		t.Fatal("credential_quota_histories is missing after the ledger rewrite")
	}
}
