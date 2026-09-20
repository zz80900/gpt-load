package migrations

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

const ID0014ZZ = "0014_zz_anthropic_betas"

// Up0014ZZ 原子增加客户端声明的 beta 能力列，旧日志保留空值，避免推测历史请求的 beta 声明。
func Up0014ZZ(db *gorm.DB) error {
	if err := ValidateRecoverable0014ZZ(db); err != nil {
		return err
	}
	if !db.Migrator().HasColumn("request_logs", "anthropic_betas") {
		if err := db.Exec("ALTER TABLE request_logs ADD COLUMN anthropic_betas VARCHAR(1024) NOT NULL DEFAULT ''").Error; err != nil {
			return fmt.Errorf("add anthropic betas: %w", err)
		}
	}
	return Validate0014ZZ(db)
}
func ValidateRecoverable0014ZZ(db *gorm.DB) error {
	if !db.Migrator().HasTable("request_logs") {
		return fmt.Errorf("request_logs table is missing")
	}
	if db.Migrator().HasColumn("request_logs", "anthropic_betas") {
		return Validate0014ZZ(db)
	}
	return nil
}
func Validate0014ZZ(db *gorm.DB) error {
	columns, err := db.Migrator().ColumnTypes("request_logs")
	if err != nil {
		return err
	}
	for _, column := range columns {
		if column.Name() != "anthropic_betas" {
			continue
		}
		if !strings.Contains(strings.ToLower(column.DatabaseTypeName()), "char") {
			return fmt.Errorf("anthropic betas must be varchar")
		}
		if nullable, known := column.Nullable(); !known || nullable {
			return fmt.Errorf("anthropic betas must be non-null")
		}
		if length, known := column.Length(); known && length != 1024 {
			return fmt.Errorf("anthropic betas length must be 1024")
		}
		value, known := column.DefaultValue()
		if !known || (value != "" && value != "''" && value != "''::character varying") {
			return fmt.Errorf("anthropic betas must default to empty")
		}
		return nil
	}
	return fmt.Errorf("anthropic betas is missing")
}
