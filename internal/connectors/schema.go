package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"app/internal/domain"
)

type schemaEntry struct {
	id     int
	schema string
	fields map[string]bool // field name -> required (true = required, false = optional)
}

// InMemorySchemaRegistry implements SchemaRegistry with simplified BACKWARD compatibility.
// BACKWARD rule: new schema can read data written by old schema.
// Allowed: adding optional (nullable) fields.
// Rejected: removing fields, renaming fields, making optional fields required.
type InMemorySchemaRegistry struct {
	mu      sync.RWMutex
	counter atomic.Int64
	subjects map[string][]schemaEntry // subject -> ordered versions
}

func NewInMemorySchemaRegistry() *InMemorySchemaRegistry {
	return &InMemorySchemaRegistry{
		subjects: make(map[string][]schemaEntry),
	}
}

// jsonSchema is a minimal schema representation for BACKWARD compat checking.
// Fields without "required" flag are treated as optional.
type jsonSchema struct {
	Fields []jsonField `json:"fields"`
}

type jsonField struct {
	Name     string `json:"name"`
	Required bool   `json:"required,omitempty"`
}

func parseFields(schema string) (map[string]bool, error) {
	if schema == "" {
		return map[string]bool{}, nil
	}
	var js jsonSchema
	if err := json.Unmarshal([]byte(schema), &js); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	m := make(map[string]bool, len(js.Fields))
	for _, f := range js.Fields {
		m[f.Name] = f.Required
	}
	return m, nil
}

// checkBackwardCompat verifies the new schema is BACKWARD compatible with the previous one.
// Backward compatibility means the new schema can read data produced by the old schema.
// Rules:
//   - All fields in the old schema must exist in the new schema (no removals/renames).
//   - New fields must be optional (required=false).
func checkBackwardCompat(prev, next map[string]bool) error {
	// Every old field must still exist in the new schema.
	for name := range prev {
		if _, ok := next[name]; !ok {
			return fmt.Errorf("BACKWARD incompatible: field %q removed", name)
		}
	}
	// New fields must be optional.
	for name, required := range next {
		if _, existed := prev[name]; !existed && required {
			return fmt.Errorf("BACKWARD incompatible: new field %q is required; new fields must be optional", name)
		}
	}
	return nil
}

func (r *InMemorySchemaRegistry) Register(_ context.Context, subject string, schema string, mode domain.CompatibilityMode) (int, error) {
	fields, err := parseFields(schema)
	if err != nil {
		return 0, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	versions := r.subjects[subject]
	if len(versions) > 0 && mode == domain.CompatBackward {
		prev := versions[len(versions)-1].fields
		if err := checkBackwardCompat(prev, fields); err != nil {
			return 0, err
		}
	}

	id := int(r.counter.Add(1))
	r.subjects[subject] = append(r.subjects[subject], schemaEntry{
		id:     id,
		schema: schema,
		fields: fields,
	})
	return id, nil
}

func (r *InMemorySchemaRegistry) GetLatest(_ context.Context, subject string) (string, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	versions := r.subjects[subject]
	if len(versions) == 0 {
		return "", 0, fmt.Errorf("subject %q: %w", subject, ErrNotFound)
	}
	latest := versions[len(versions)-1]
	return latest.schema, latest.id, nil
}

// ErrNotFound is re-exported for convenience — matches store.ErrNotFound semantics.
var ErrNotFound = fmt.Errorf("not found")
