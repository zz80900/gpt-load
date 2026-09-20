// Package migrations holds the ordered schema migration chain.
//
// # Naming convention
//
// Upstream migrations keep the upstream file name verbatim: <NNNN>_<snake_name>.
// Merging upstream must never require renumbering them.
//
// Fork-owned (private) migrations are named <NNNN>_zz_<snake_name>, where NNNN is
// the upstream number that was latest when the private migration was created.
// The migration is registered immediately after that upstream migration, and its
// Go identifiers carry a ZZ suffix (ID0014ZZ, Up0014ZZ, Validate0014ZZ,
// ValidateRecoverable0014ZZ) so they cannot collide with the identifiers upstream
// derives from the same number.
//
// Why "_zz_" and why directly after the anchor:
//
//   - Upstream will never produce an ID containing "_zz_", so a private
//     migration can never collide with a future upstream number.
//   - The ledger is read with ORDER BY id ASC and compared index by index with
//     the registry, so the registry order must equal the lexicographic ID order.
//     "_zz_" sorts directly after its anchor:
//     0014_affinity_kind < 0014_zz_anthropic_betas < 0015_group_usage_index.
//   - The discriminating character is 'a' < 'z' in a letter position, never
//     '_', so the ordering does not depend on the database collation (SQLite
//     BINARY, MySQL *_ci and PostgreSQL collations weight '_' differently but
//     agree on letters).
//
// Keeping a private migration adjacent to its anchor also keeps already applied
// ledgers a prefix of the registry, which is what the position-based comparison
// in package storage requires.
//
// When adding a private migration, use the current upstream tip number, name the
// file <that number>_zz_<snake_name>, and register it right after the upstream
// migration carrying that number. Do not renumber upstream migrations.
package migrations
