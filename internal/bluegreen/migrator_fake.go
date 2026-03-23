package bluegreen

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// FakeDatabaseMigrator is an in-memory DatabaseMigrator for tests.
// It maintains a column registry representing the current schema state
// so that QueryCheck can detect missing columns without a real database.
//
// Initial schema matches the workshop inventory.customers table:
// id, email, full_name, status, created_at, search_key.
type FakeDatabaseMigrator struct {
	mu          sync.Mutex
	columns     map[string]bool  // lowercased column names
	ReadOnly    bool
	ExecutedSQL []string
	QueryCounts map[string]int64 // SQL -> fixed count override for tests

	// Hooks for targeted test injection — set to override default behaviour.
	ExecuteSQLFn  func(statements []string) error
	QueryCheckFn  func(query string) (CheckResult, error)
	SetReadOnlyFn func(readOnly bool) error
}

// NewFakeDatabaseMigrator creates a migrator seeded with the original
// inventory.customers schema (before any migration).
func NewFakeDatabaseMigrator() *FakeDatabaseMigrator {
	return &FakeDatabaseMigrator{
		columns: map[string]bool{
			"id":         true,
			"email":      true,
			"full_name":  true,
			"status":     true,
			"created_at": true,
			"search_key": true,
		},
		QueryCounts: make(map[string]int64),
	}
}

func (f *FakeDatabaseMigrator) ExecuteSQL(ctx context.Context, statements []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.ExecuteSQLFn != nil {
		return f.ExecuteSQLFn(statements)
	}
	for _, stmt := range statements {
		f.ExecutedSQL = append(f.ExecutedSQL, stmt)
		if err := f.applyDDLLocked(stmt); err != nil {
			return err
		}
	}
	return nil
}

// applyDDLLocked parses simple ADD COLUMN / DROP COLUMN statements and updates
// the column registry. Must be called with mu held.
func (f *FakeDatabaseMigrator) applyDDLLocked(stmt string) error {
	upper := strings.ToUpper(strings.TrimSpace(stmt))

	addRe := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+\S+\s+ADD\s+COLUMN\s+(\w+)`)
	if m := addRe.FindStringSubmatch(stmt); len(m) == 2 {
		f.columns[strings.ToLower(m[1])] = true
		return nil
	}

	dropRe := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+\S+\s+DROP\s+COLUMN\s+(?:IF\s+EXISTS\s+)?(\w+)`)
	if m := dropRe.FindStringSubmatch(stmt); len(m) == 2 {
		col := strings.ToLower(m[1])
		if !f.columns[col] {
			if strings.Contains(upper, "IF EXISTS") {
				return nil
			}
			return fmt.Errorf("column %q does not exist", col)
		}
		delete(f.columns, col)
		return nil
	}

	// UPDATE / INSERT / SELECT — no schema change; accepted.
	return nil
}

// QueryCheck validates that all columns referenced in the query exist, then
// returns a CheckResult. If a QueryCounts override is set for this SQL that
// count is returned; otherwise 0 (no inconsistency found).
func (f *FakeDatabaseMigrator) QueryCheck(ctx context.Context, query string) (CheckResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.QueryCheckFn != nil {
		return f.QueryCheckFn(query)
	}
	if err := f.validateColumnRefsLocked(query); err != nil {
		return CheckResult{}, err
	}
	count := f.QueryCounts[query]
	return CheckResult{Count: count}, nil
}

// validateColumnRefsLocked parses column names from a SELECT/INSERT/UPDATE
// statement and verifies each exists in the column registry.
func (f *FakeDatabaseMigrator) validateColumnRefsLocked(query string) error {
	for _, col := range extractColumns(query) {
		if !f.columns[col] {
			return fmt.Errorf("column %q not found in schema (query: %s)", col, truncate(query, 60))
		}
	}
	return nil
}

// ValidateQuery checks that all columns referenced in the query exist in the
// fake column registry. Same logic as QueryCheck but returns only an error.
func (f *FakeDatabaseMigrator) ValidateQuery(ctx context.Context, query string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.validateColumnRefsLocked(query)
}

func (f *FakeDatabaseMigrator) SetReadOnly(ctx context.Context, readOnly bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SetReadOnlyFn != nil {
		return f.SetReadOnlyFn(readOnly)
	}
	f.ReadOnly = readOnly
	return nil
}

// ApplyExpand applies expand DDL statements directly to the column registry.
// Useful in unit tests that want to advance schema state without going through
// the full activity/workflow stack.
func (f *FakeDatabaseMigrator) ApplyExpand(statements []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range statements {
		_ = f.applyDDLLocked(s)
	}
}

// ApplyContract applies contract DDL statements directly to the column registry.
func (f *FakeDatabaseMigrator) ApplyContract(statements []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range statements {
		_ = f.applyDDLLocked(s)
	}
}

// HasColumn reports whether the named column is currently in the fake schema.
func (f *FakeDatabaseMigrator) HasColumn(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.columns[strings.ToLower(name)]
}

// ── SQL parsing helpers ───────────────────────────────────────────────────────

// extractColumns pulls column names from SQL SELECT/INSERT/UPDATE statements.
// Handles the most common patterns found in the AppSimulator queries.
func extractColumns(query string) []string {
	var cols []string
	seen := map[string]bool{}

	// SELECT col1, col2, ... FROM
	selectRe := regexp.MustCompile(`(?i)SELECT\s+(.*?)\s+FROM`)
	if m := selectRe.FindStringSubmatch(query); len(m) == 2 {
		for part := range strings.SplitSeq(m[1], ",") {
			col := cleanIdent(part)
			if col != "" && col != "*" && !isKeyword(col) {
				if !seen[col] {
					seen[col] = true
					cols = append(cols, col)
				}
			}
		}
	}

	// INSERT INTO tbl (col1, col2)
	insertRe := regexp.MustCompile(`(?i)INSERT\s+INTO\s+\S+\s*\(([^)]+)\)`)
	if m := insertRe.FindStringSubmatch(query); len(m) == 2 {
		for part := range strings.SplitSeq(m[1], ",") {
			col := cleanIdent(part)
			if col != "" && !isKeyword(col) {
				if !seen[col] {
					seen[col] = true
					cols = append(cols, col)
				}
			}
		}
	}

	// SET col = ...
	setRe := regexp.MustCompile(`(?i)SET\s+(\w+)\s*=`)
	for _, m := range setRe.FindAllStringSubmatch(query, -1) {
		if len(m) == 2 {
			col := strings.ToLower(m[1])
			if !isKeyword(col) && !seen[col] {
				seen[col] = true
				cols = append(cols, col)
			}
		}
	}

	// WHERE col IS / col = / col > etc.
	whereRe := regexp.MustCompile(`(?i)WHERE\s+(\w+)\s`)
	if m := whereRe.FindStringSubmatch(query); len(m) == 2 {
		col := strings.ToLower(m[1])
		if !isKeyword(col) && !seen[col] {
			seen[col] = true
			cols = append(cols, col)
		}
	}

	return cols
}

func cleanIdent(s string) string {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "()") {
		return ""
	}
	parts := strings.Split(s, ".")
	s = parts[len(parts)-1]
	return strings.ToLower(strings.Trim(s, `"' `))
}

var sqlKeywords = map[string]bool{
	"count": true, "from": true, "where": true, "and": true, "or": true,
	"not": true, "null": true, "is": true, "in": true, "as": true,
	"select": true, "insert": true, "update": true, "delete": true,
	"into": true, "values": true, "set": true, "true": true, "false": true,
}

func isKeyword(s string) bool { return sqlKeywords[strings.ToLower(s)] }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
