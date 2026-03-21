package bluegreen

import "context"

// AppQuery is a SQL statement that an application version would execute.
type AppQuery struct {
	// Name is a human-readable label shown in workshop output and test failures.
	Name string
	// SQL is a parameterised query. Parameters use $1, $2, … placeholders.
	SQL string
	// Args are the example argument values (used for documentation only).
	Args []any
}

// AppCompatResult captures the outcome of running both app versions against
// the current schema.
type AppCompatResult struct {
	// BluePass is true when all blue-app queries are structurally valid.
	BluePass bool
	// GreenPass is true when all green-app queries are structurally valid.
	GreenPass bool
	// BlueErrors lists any query validation failures for the blue app.
	BlueErrors []string
	// GreenErrors lists any query validation failures for the green app.
	GreenErrors []string
}

// AppSimulator provides SQL query sets for both the current (blue) and new
// (green) application version. These queries are run against FakeDatabaseMigrator
// at each phase boundary to verify backward/forward compatibility.
type AppSimulator interface {
	// BlueQueries returns queries from the current production app (v1).
	// These use the OLD schema shape: full_name column.
	BlueQueries() []AppQuery

	// GreenQueries returns queries from the new app version (v2).
	// These use the NEW schema shape: display_name + phone columns.
	// During the expand phase, green INSERT also writes full_name (dual-write).
	GreenQueries() []AppQuery
}

// CustomerApp is the concrete AppSimulator for the workshop scenario:
// renaming full_name → display_name and adding phone to inventory.customers.
type CustomerApp struct{}

// NewCustomerApp returns a ready-to-use CustomerApp.
func NewCustomerApp() *CustomerApp { return &CustomerApp{} }

// BlueQueries returns v1 app queries that reference full_name.
func (a *CustomerApp) BlueQueries() []AppQuery {
	return []AppQuery{
		{
			Name: "GetCustomer",
			SQL:  "SELECT id, email, full_name, status FROM inventory.customers WHERE id = $1",
			Args: []any{1},
		},
		{
			Name: "InsertCustomer",
			SQL:  "INSERT INTO inventory.customers (email, full_name) VALUES ($1, $2)",
			Args: []any{"new@example.com", "New User"},
		},
		{
			Name: "UpdateName",
			SQL:  "UPDATE inventory.customers SET full_name = $1 WHERE id = $2",
			Args: []any{"Alice Updated", 1},
		},
	}
}

// GreenQueries returns v2 app queries that reference display_name and phone.
// During the expand window, full_name is still present in the schema and the
// expand SQL's UPDATE backfills display_name from full_name for all existing
// rows. New rows written by the green app only populate display_name and phone;
// full_name is nullable so older blue-app reads still see it for their own inserts.
func (a *CustomerApp) GreenQueries() []AppQuery {
	return []AppQuery{
		{
			Name: "GetCustomer",
			SQL:  "SELECT id, email, display_name, phone, status FROM inventory.customers WHERE id = $1",
			Args: []any{1},
		},
		{
			Name: "InsertCustomer",
			SQL:  "INSERT INTO inventory.customers (email, display_name, phone) VALUES ($1, $2, $3)",
			Args: []any{"new@example.com", "New User", "+1234567890"},
		},
		{
			Name: "UpdateName",
			SQL:  "UPDATE inventory.customers SET display_name = $1 WHERE id = $2",
			Args: []any{"Alice Updated", 1},
		},
	}
}

// RunAppCompat exercises both query sets against the provided migrator and
// returns a compatibility report without failing the caller.
func RunAppCompat(ctx context.Context, app AppSimulator, db DatabaseMigrator) AppCompatResult {
	result := AppCompatResult{BluePass: true, GreenPass: true}

	for _, q := range app.BlueQueries() {
		if _, err := db.QueryCheck(ctx, q.SQL); err != nil {
			result.BluePass = false
			result.BlueErrors = append(result.BlueErrors, q.Name+": "+err.Error())
		}
	}
	for _, q := range app.GreenQueries() {
		if _, err := db.QueryCheck(ctx, q.SQL); err != nil {
			result.GreenPass = false
			result.GreenErrors = append(result.GreenErrors, q.Name+": "+err.Error())
		}
	}
	return result
}
