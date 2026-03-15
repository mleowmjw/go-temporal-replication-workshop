package connectors_test

import (
	"context"
	"testing"

	"app/internal/connectors"
	"app/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

const (
	subjectA = "inventory.customers"
	v1Schema = `{"fields":[{"name":"id","required":true},{"name":"email","required":true}]}`
	// Add optional field — BACKWARD compatible.
	v2SchemaAddOptional = `{"fields":[{"name":"id","required":true},{"name":"email","required":true},{"name":"phone"}]}`
	// Remove existing field — BACKWARD incompatible.
	v2SchemaRemoveField = `{"fields":[{"name":"id","required":true}]}`
	// Add required field — BACKWARD incompatible.
	v2SchemaAddRequired = `{"fields":[{"name":"id","required":true},{"name":"email","required":true},{"name":"region","required":true}]}`
)

func TestInMemorySchemaRegistry_Register(t *testing.T) {
	t.Run("first registration succeeds", func(t *testing.T) {
		r := connectors.NewInMemorySchemaRegistry()
		id, err := r.Register(ctx, subjectA, v1Schema, domain.CompatBackward)
		require.NoError(t, err)
		assert.Equal(t, 1, id)
	})

	t.Run("add optional field is backward compatible", func(t *testing.T) {
		r := connectors.NewInMemorySchemaRegistry()
		_, err := r.Register(ctx, subjectA, v1Schema, domain.CompatBackward)
		require.NoError(t, err)
		id, err := r.Register(ctx, subjectA, v2SchemaAddOptional, domain.CompatBackward)
		require.NoError(t, err)
		assert.Equal(t, 2, id)
	})

	t.Run("remove field is backward incompatible", func(t *testing.T) {
		r := connectors.NewInMemorySchemaRegistry()
		_, err := r.Register(ctx, subjectA, v1Schema, domain.CompatBackward)
		require.NoError(t, err)
		_, err = r.Register(ctx, subjectA, v2SchemaRemoveField, domain.CompatBackward)
		assert.ErrorContains(t, err, "BACKWARD incompatible")
		assert.ErrorContains(t, err, "removed")
	})

	t.Run("add required field is backward incompatible", func(t *testing.T) {
		r := connectors.NewInMemorySchemaRegistry()
		_, err := r.Register(ctx, subjectA, v1Schema, domain.CompatBackward)
		require.NoError(t, err)
		_, err = r.Register(ctx, subjectA, v2SchemaAddRequired, domain.CompatBackward)
		assert.ErrorContains(t, err, "BACKWARD incompatible")
		assert.ErrorContains(t, err, "required")
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		r := connectors.NewInMemorySchemaRegistry()
		_, err := r.Register(ctx, subjectA, "{invalid}", domain.CompatBackward)
		assert.Error(t, err)
	})
}

func TestInMemorySchemaRegistry_GetLatest(t *testing.T) {
	r := connectors.NewInMemorySchemaRegistry()
	_, _, err := r.GetLatest(ctx, "missing")
	assert.ErrorContains(t, err, "not found")

	_, err = r.Register(ctx, subjectA, v1Schema, domain.CompatBackward)
	require.NoError(t, err)
	_, err = r.Register(ctx, subjectA, v2SchemaAddOptional, domain.CompatBackward)
	require.NoError(t, err)

	gotSchema, id, err := r.GetLatest(ctx, subjectA)
	require.NoError(t, err)
	assert.Equal(t, v2SchemaAddOptional, gotSchema)
	assert.Equal(t, 2, id)
}

func TestInMemorySecretProvider(t *testing.T) {
	p := connectors.NewInMemorySecretProvider()
	p.Set("pg-secret", map[string]string{"host": "localhost", "port": "5432"})

	vals, err := p.Resolve(ctx, "pg-secret")
	require.NoError(t, err)
	assert.Equal(t, "localhost", vals["host"])

	_, err = p.Resolve(ctx, "missing")
	assert.Error(t, err)
}
