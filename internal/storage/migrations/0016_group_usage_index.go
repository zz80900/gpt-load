package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

const ID0016 = "0016_group_usage_index"
const requestLogGroupIndex0016 = "idx_request_logs_group_completed"

type requestLog0016 struct {
	GroupID       uint  `gorm:"column:group_id;index:idx_request_logs_group_completed,priority:1"`
	CompletedAtMS int64 `gorm:"column:completed_at_ms;index:idx_request_logs_group_completed,priority:2,sort:desc"`
}

func (requestLog0016) TableName() string { return "request_logs" }

// Up0016 只新增精确窗口查询索引，不改日志、用量或转发语义。
func Up0016(db *gorm.DB) error {
	if err := ValidateRecoverable0016(db); err != nil {
		return err
	}
	if db.Migrator().HasIndex(&requestLog0016{}, requestLogGroupIndex0016) {
		return nil
	}
	return db.Migrator().CreateIndex(&requestLog0016{}, requestLogGroupIndex0016)
}

func ValidateRecoverable0016(db *gorm.DB) error {
	if !db.Migrator().HasTable(&requestLog0016{}) {
		return fmt.Errorf("group usage index: request_logs table is missing")
	}
	return nil
}

func Validate0016(db *gorm.DB) error {
	if err := ValidateRecoverable0016(db); err != nil {
		return err
	}
	if !db.Migrator().HasIndex(&requestLog0016{}, requestLogGroupIndex0016) {
		return fmt.Errorf("group usage index is missing")
	}
	return nil
}
