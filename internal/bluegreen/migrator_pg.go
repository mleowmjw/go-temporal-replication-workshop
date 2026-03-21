package bluegreen

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgDatabaseMigrator implements DatabaseMigrator against a real PostgreSQL
// database using pgx connection pool. Used for end-to-end runs.
type PgDatabaseMigrator struct {
	pool *pgxpool.Pool
}

// NewPgDatabaseMigrator creates a migrator that uses the provided connection pool.
func NewPgDatabaseMigrator(pool *pgxpool.Pool) *PgDatabaseMigrator {
	return &PgDatabaseMigrator{pool: pool}
}

// ExecuteSQL runs each statement sequentially within a single transaction.
// PostgreSQL DDL is transactional, so the entire batch is atomic — on any
// error the transaction is rolled back and the error is returned.
func (p *PgDatabaseMigrator) ExecuteSQL(ctx context.Context, statements []string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, stmt := range statements {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("execute %q: %w", truncate(stmt, 60), err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// QueryCheck executes the supplied query with QueryRow and scans the first
// column as an int64 count. The query should be a SELECT COUNT(*) or similar
// aggregation that returns exactly one row with one integer column.
func (p *PgDatabaseMigrator) QueryCheck(ctx context.Context, query string) (CheckResult, error) {
	var count int64
	if err := p.pool.QueryRow(ctx, query).Scan(&count); err != nil {
		return CheckResult{}, fmt.Errorf("query check %q: %w", truncate(query, 60), err)
	}
	return CheckResult{Count: count}, nil
}

// SetReadOnly toggles the session-level read-only flag on the current database.
// Using SET LOCAL is intentionally avoided so that the setting persists beyond
// the current transaction; a session-level SET is appropriate for the workshop's
// manual read-only window.
func (p *PgDatabaseMigrator) SetReadOnly(ctx context.Context, readOnly bool) error {
	value := "off"
	if readOnly {
		value = "on"
	}
	_, err := p.pool.Exec(ctx, "SET default_transaction_read_only TO "+value)
	if err != nil {
		return fmt.Errorf("set read-only=%v: %w", readOnly, err)
	}
	return nil
}
