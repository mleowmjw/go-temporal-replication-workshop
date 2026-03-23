package bluegreen

import "context"

// CheckResult holds the outcome of a single verify query.
type CheckResult struct {
	Name  string
	Count int64
}

// DatabaseMigrator abstracts all SQL operations against the target database.
// Two implementations exist:
//   - FakeDatabaseMigrator (migrator_fake.go) — in-memory column registry for unit tests
//   - PgDatabaseMigrator   (migrator_pg.go)   — real PostgreSQL via pgx, used in production
type DatabaseMigrator interface {
	// ExecuteSQL runs a list of DDL/DML statements in order.
	ExecuteSQL(ctx context.Context, statements []string) error

	// QueryCheck runs a verify query and returns its COUNT(*) result.
	// The query must be a SELECT COUNT(*) or similar single-row aggregation.
	QueryCheck(ctx context.Context, query string) (CheckResult, error)

	// ValidateQuery checks that a parameterized SQL statement is structurally
	// valid against the current schema (columns exist, types are compatible)
	// without executing it. Used by app-compatibility checks.
	ValidateQuery(ctx context.Context, query string) error

	// SetReadOnly toggles the database read-only mode.
	SetReadOnly(ctx context.Context, readOnly bool) error
}
