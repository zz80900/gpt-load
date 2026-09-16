# Database Guidelines

> Database patterns and conventions for this project.

---

## Overview

<!--
Document your project's database conventions here.

Questions to answer:
- What ORM/query library do you use?
- How are migrations managed?
- What are the naming conventions for tables/columns?
- How do you handle transactions?
-->

(To be filled by the team)

---

## Query Patterns

<!-- How should queries be written? Batch operations? -->

(To be filled by the team)

---

## Migrations

<!-- How to create and run migrations -->

Migrations live in `internal/storage/migrations/`, one file per version, and are
registered in `internal/storage/migration.go`. Every migration implements the
same three-function template so a half-applied DDL can be detected and resumed:

```go
const ID0015 = "0015_anthropic_betas"

// Up0015 is idempotent: recoverable preconditions, then the DDL, then validation.
func Up0015(db *gorm.DB) error {
	if err := ValidateRecoverable0015(db); err != nil {
		return err
	}
	if !db.Migrator().HasColumn("request_logs", "anthropic_betas") {
		if err := db.Exec("ALTER TABLE ...").Error; err != nil {
			return fmt.Errorf("add anthropic betas: %w", err)
		}
	}
	return Validate0015(db)
}

// ValidateRecoverable0015 fails only when the state cannot be repaired in place.
func ValidateRecoverable0015(db *gorm.DB) error { /* table missing -> error, column present -> Validate0015 */ }

// Validate0015 asserts the final shape: type, nullability, length, default.
func Validate0015(db *gorm.DB) error { /* ColumnTypes checks */ }
```

**Why**: MySQL DDL auto-commits, so a crash between two statements leaves a
partially migrated schema. `ValidateRecoverable*` separates "already done, safe
to continue" from "wrong shape, stop and report".

### Adding a column to `request_logs`

A new column touches more places than the migration itself. Miss one and the
build, the startup check, or an unrelated migration test fails:

| Location | Change |
| --- | --- |
| `internal/storage/migrations/00NN_<name>.go` | New three-function migration |
| `internal/storage/migration.go` | Append the registry entry |
| `internal/storage/migration_test.go` | Append the ID to `wantIDs` |
| `internal/storage/db_test.go` | Append the ID to `wantMigrationIDs` |
| `internal/storage/database_integration_test.go` | Bump the chain length and assert the new tail ID |
| `internal/storage/models/request.go` | Add the field, `type:varchar(N);not null;default:''` |

> **Warning**: every existing test that inserts a *legacy* row with
> `db.Omit(...)` must list the new field too. The GORM model always carries it,
> so writing into a database that only has the older columns fails with
> `table request_logs has no column named <new_column>`. Grep `db.Omit(` across
> `internal/storage/` before running the suite.

Column type and width must match the sibling fields of the same table — use the
`type:varchar(N)` form, not an ad-hoc `size:` tag.

---

## Naming Conventions

<!-- Table names, column names, index names -->

(To be filled by the team)

---

## Common Mistakes

<!-- Database-related mistakes your team has made -->

(To be filled by the team)
