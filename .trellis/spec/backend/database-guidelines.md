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
const ID0014ZZ = "0014_zz_anthropic_betas"

// Up0014ZZ is idempotent: recoverable preconditions, then the DDL, then validation.
func Up0014ZZ(db *gorm.DB) error {
	if err := ValidateRecoverable0014ZZ(db); err != nil {
		return err
	}
	if !db.Migrator().HasColumn("request_logs", "anthropic_betas") {
		if err := db.Exec("ALTER TABLE ...").Error; err != nil {
			return fmt.Errorf("add anthropic betas: %w", err)
		}
	}
	return Validate0014ZZ(db)
}

// ValidateRecoverable0014ZZ fails only when the state cannot be repaired in place.
func ValidateRecoverable0014ZZ(db *gorm.DB) error { /* table missing -> error, column present -> Validate0014ZZ */ }

// Validate0014ZZ asserts the final shape: type, nullability, length, default.
func Validate0014ZZ(db *gorm.DB) error { /* ColumnTypes checks */ }
```

**Why**: MySQL DDL auto-commits, so a crash between two statements leaves a
partially migrated schema. `ValidateRecoverable*` separates "already done, safe
to continue" from "wrong shape, stop and report".

### Upstream vs fork-owned migration IDs

Upstream migrations keep their upstream file name verbatim: `<NNNN>_<snake_name>`.
Merging upstream must never require renumbering them.

A fork-owned (private) migration takes the upstream number that was latest when it
was written plus a `_zz_` marker: `<NNNN>_zz_<snake_name>`. It is registered
immediately after that upstream migration, and its Go identifiers carry a `ZZ`
suffix (`ID0014ZZ`, `Up0014ZZ`, `Validate0014ZZ`, `ValidateRecoverable0014ZZ`) so
they cannot collide with the identifiers upstream derives from the same number.

`_zz_` can never collide with an upstream ID, and it sorts directly after its
anchor with the discriminating character in a letter position
(`0014_affinity_kind` < `0014_zz_anthropic_betas` < `0015_group_usage_index`), so
the ordering does not depend on the database collation. Staying adjacent to the
anchor is what keeps an already applied ledger a prefix of the registry, which is
the invariant the position-based ledger comparison requires. The full rationale
lives in `internal/storage/migrations/doc.go`.

Renaming a fork-owned migration does not renumber upstream files; instead
`internal/storage/migration_legacy_ids.go` rewrites the old ledger IDs of the
already published images, including the MySQL `<id>#building` resume markers, once
and idempotently at startup.

### Registry and ledger contract

Two invariants the runner enforces on every startup:

1. **Registry order == ID lexicographic order.** `validateMigrationRegistry` rejects an
   entry whose ID is not strictly greater than its predecessor.
2. **The applied ledger is a prefix of the registry.** `applyMigrationsLocked` reads
   `schema_migrations` with `ORDER BY id ASC` and requires `entries[i].ID == applied[i]`;
   pending work is `entries[len(applied):]`.

Invariant 2 is why a fork migration must stay *anchored*: an instance that applied it
before the next upstream migration exists must still see a prefix afterwards.

Validation and error matrix:

| Condition | Error |
| --- | --- |
| ID does not match `^\d{4}_[a-z0-9]+(?:_[a-z0-9]+)*$` | `migration registry entry N has invalid ID` |
| ID <= its predecessor (this also catches duplicates) | `migration registry entry N has non-ascending ID` |
| `Up` / `Validate` / `ValidateRecoverable` is nil | `migration registry entry N (...) is incomplete` |
| Ledger holds an ID the registry does not expect at that position | `schema_migrations contains unknown or non-contiguous migration` |

Tests required, with their assertion points:

| Test | Asserts |
| --- | --- |
| `migration_test.go` | `wantIDs` matches the registry 1:1; an anchored `_zz_` ID is accepted; descending / duplicate / fork-before-anchor are rejected |
| `db_test.go` | `wantMigrationIDs` equals the ledger after a full run |
| `database_integration_test.go` | chain length and every tail ID, in order |
| `migration_legacy_ids_test.go` | every published legacy ledger shape rewrites to a registry prefix, is idempotent, and leaves the schema (column + indexes) intact |

#### Wrong vs Correct

**Wrong** — anchoring a new fork migration to the next unused number:

```go
// upstream is at 0017; upstream will publish 0018 next
const IDFork = "0018_zz_audit_log"
```

An instance that applies this today, and then takes an upstream release containing
`0018_*`, ends up with a ledger that is no longer a prefix — upstream's ID sorts
*before* the fork one — and startup fails with
`schema_migrations contains unknown or non-contiguous migration`.

**Correct** — anchor to the upstream tail that exists right now:

```go
// sorts after 0017_request_log_operation_index and before any future 0018_*
const ID0017ZZ = "0017_zz_audit_log"
```

### Adding a column to `request_logs`

A new column touches more places than the migration itself. Miss one and the
build, the startup check, or an unrelated migration test fails:

| Location | Change |
| --- | --- |
| `internal/storage/migrations/00NN_<name>.go` | New three-function migration. Fork-owned ones use `00NN_zz_<name>.go` with a `ZZ` identifier suffix when `00NN` is already taken upstream |
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

### Renumbering upstream migrations when merging upstream

**Symptom**: every upstream merge produces conflicts across `migration.go`,
`migration_test.go`, `db_test.go` and `database_integration_test.go`, and upstream's
IDs have to be shifted down one by one.

**Cause**: a fork-owned migration used to sit inside upstream's number space, pushing
the whole upstream tail down by one forever.

**Fix**: fork-owned migrations use `<NNNN>_zz_<name>` anchored to the upstream tail
(see [Upstream vs fork-owned migration IDs](#upstream-vs-fork-owned-migration-ids)).
Upstream files are then taken **verbatim** and never renumbered.

**Prevention**: `validateMigrationRegistry` no longer requires `number == position`,
so nothing forces the shift any more. If a merge seems to demand renumbering an
upstream migration, stop — that is the bug, not the requirement.

### Assuming a ledger rewrite can be rolled back

> **Warning**: the `_zz_` rename rewrote the ledger of every instance that already ran
> a pre-split image. Returning to an older image after that point **fails startup**:
> the old registry expects the legacy IDs at their old positions. The only way back is
> a database backup. Treat the upgrade as a one-way door and say so in release notes.

### Trusting a green `go build` for migration changes

**Symptom**: a rename looks complete and the image builds, yet `go vet` or `go test`
fails on a file that was never touched.

**Cause**: `_test.go` files are not part of `go build`, so broken test-side
references survive until someone runs the suite.

**Prevention**: after any migration or ID change, run `go vet ./...` (it compiles test
files) before declaring the change done.
