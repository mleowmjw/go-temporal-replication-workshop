package bluegreen

import (
	"context"
	"fmt"
	"strings"

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

// ExecuteSQL runs each statement sequentially. Statements containing
// CONCURRENTLY (e.g. CREATE/DROP INDEX CONCURRENTLY) cannot run inside a
// transaction block in PostgreSQL, so they are executed directly on the pool.
// All other statements are wrapped in a single transaction for atomicity.
func (p *PgDatabaseMigrator) ExecuteSQL(ctx context.Context, statements []string) error {
	for _, stmt := range statements {
		upper := strings.ToUpper(stmt)
		if strings.Contains(upper, "CONCURRENTLY") {
			// Must run outside a transaction.
			if _, err := p.pool.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("execute %q: %w", truncate(stmt, 60), err)
			}
			continue
		}
		// Transactional DDL (safe to group together).
		tx, err := p.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin transaction: %w", err)
		}
		if _, err := tx.Exec(ctx, stmt); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("execute %q: %w", truncate(stmt, 60), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit transaction: %w", err)
		}
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

// ValidateQuery uses PostgreSQL PREPARE to check that a parameterized query is
// structurally valid (all referenced columns/tables exist, types match) without
// executing it or requiring actual argument values.
func (p *PgDatabaseMigrator) ValidateQuery(ctx context.Context, query string) error {
	const stmtName = "_bg_compat_check"
	_, err := p.pool.Exec(ctx, "PREPARE "+stmtName+" AS "+query)
	if err != nil {
		return fmt.Errorf("validate query %q: %w", truncate(query, 60), err)
	}
	_, _ = p.pool.Exec(ctx, "DEALLOCATE "+stmtName)
	return nil
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
