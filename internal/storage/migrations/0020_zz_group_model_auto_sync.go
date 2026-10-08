package migrations

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

const ID0020ZZ = "0020_zz_group_model_auto_sync"

type groupAutoSyncModels0020ZZ struct {
	AutoSyncModels bool `gorm:"column:auto_sync_models;not null;default:false"`
}

func (groupAutoSyncModels0020ZZ) TableName() string { return "groups" }

// Up0020ZZ adds the per-group model auto-sync switch. Existing rows stay off.
func Up0020ZZ(db *gorm.DB) error {
	model := &groupAutoSyncModels0020ZZ{}
	if !db.Migrator().HasColumn(model, "auto_sync_models") {
		if err := db.Migrator().AddColumn(model, "AutoSyncModels"); err != nil {
			return fmt.Errorf("add groups.auto_sync_models: %w", err)
		}
	}
	return Validate0020ZZ(db)
}

func ValidateRecoverable0020ZZ(db *gorm.DB) error {
	model := &groupAutoSyncModels0020ZZ{}
	if !db.Migrator().HasTable(model) {
		return fmt.Errorf("validate recoverable group model auto sync: table %q is missing", "groups")
	}
	if db.Migrator().HasColumn(model, "auto_sync_models") {
		return Validate0020ZZ(db)
	}
	return nil
}

func Validate0020ZZ(db *gorm.DB) error {
	model := &groupAutoSyncModels0020ZZ{}
	if !db.Migrator().HasColumn(model, "auto_sync_models") {
		return fmt.Errorf("validate group model auto sync: column %q.auto_sync_models is missing", "groups")
	}
	columns, err := db.Migrator().ColumnTypes(model)
	if err != nil {
		return fmt.Errorf("inspect groups.auto_sync_models: %w", err)
	}
	for _, column := range columns {
		if !strings.EqualFold(column.Name(), "auto_sync_models") {
			continue
		}
		typeName := strings.ToLower(column.DatabaseTypeName())
		if !strings.Contains(typeName, "bool") && !strings.Contains(typeName, "tinyint") && typeName != "numeric" {
			return fmt.Errorf("validate group model auto sync: column groups.auto_sync_models is not boolean")
		}
		if nullable, known := column.Nullable(); !known || nullable {
			return fmt.Errorf("validate group model auto sync: column groups.auto_sync_models is nullable")
		}
		value, known := column.DefaultValue()
		if !known || (value != "false" && value != "0" && value != "false::boolean") {
			return fmt.Errorf("validate group model auto sync: column groups.auto_sync_models does not default to false")
		}
		return nil
	}
	return fmt.Errorf("validate group model auto sync: column groups.auto_sync_models is missing")
}
