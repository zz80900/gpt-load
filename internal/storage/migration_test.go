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
