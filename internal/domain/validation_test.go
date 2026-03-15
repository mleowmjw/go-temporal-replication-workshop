package domain_test

import (
	"testing"

	"app/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTenant(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		require.NoError(t, domain.ValidateTenant(domain.Tenant{ID: "t1", Name: "Acme"}))
	})
	t.Run("missing id", func(t *testing.T) {
		err := domain.ValidateTenant(domain.Tenant{Name: "Acme"})
		assert.ErrorContains(t, err, "tenant id")
	})
	t.Run("missing name", func(t *testing.T) {
		err := domain.ValidateTenant(domain.Tenant{ID: "t1"})
		assert.ErrorContains(t, err, "tenant name")
	})
}

func validPipeline() domain.PipelineSpec {
	return domain.PipelineSpec{
		TenantID:   "t1",
		PipelineID: "p1",
		Name:       "my-pipeline",
		Source: domain.SourceSpec{
			Type:      domain.SourcePostgres,
			SecretRef: "pg-secret",
			Tables:    []string{"inventory.customers"},
		},
		Sink: domain.SinkSpec{
			Type:   domain.SinkSearch,
			Target: "customers-index",
		},
	}
}

func TestValidatePipelineSpec(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		require.NoError(t, domain.ValidatePipelineSpec(validPipeline()))
	})

	t.Run("missing tenant id", func(t *testing.T) {
		s := validPipeline()
		s.TenantID = ""
		assert.ErrorContains(t, domain.ValidatePipelineSpec(s), "tenant id")
	})

	t.Run("missing pipeline id", func(t *testing.T) {
		s := validPipeline()
		s.PipelineID = ""
		assert.ErrorContains(t, domain.ValidatePipelineSpec(s), "pipeline id")
	})

	t.Run("missing name", func(t *testing.T) {
		s := validPipeline()
		s.Name = ""
		assert.ErrorContains(t, domain.ValidatePipelineSpec(s), "pipeline name")
	})

	t.Run("missing source type", func(t *testing.T) {
		s := validPipeline()
		s.Source.Type = ""
		assert.ErrorContains(t, domain.ValidatePipelineSpec(s), "source type")
	})

	t.Run("unsupported source type", func(t *testing.T) {
		s := validPipeline()
		s.Source.Type = "mysql"
		assert.ErrorContains(t, domain.ValidatePipelineSpec(s), "unsupported source type")
	})

	t.Run("missing source secret ref", func(t *testing.T) {
		s := validPipeline()
		s.Source.SecretRef = ""
		assert.ErrorContains(t, domain.ValidatePipelineSpec(s), "secret ref")
	})

	t.Run("no tables", func(t *testing.T) {
		s := validPipeline()
		s.Source.Tables = nil
		assert.ErrorContains(t, domain.ValidatePipelineSpec(s), "at least one table")
	})

	t.Run("missing sink type", func(t *testing.T) {
		s := validPipeline()
		s.Sink.Type = ""
		assert.ErrorContains(t, domain.ValidatePipelineSpec(s), "sink type")
	})

	t.Run("unsupported sink type", func(t *testing.T) {
		s := validPipeline()
		s.Sink.Type = "redis"
		assert.ErrorContains(t, domain.ValidatePipelineSpec(s), "unsupported sink type")
	})

	t.Run("missing sink target", func(t *testing.T) {
		s := validPipeline()
		s.Sink.Target = ""
		assert.ErrorContains(t, domain.ValidatePipelineSpec(s), "sink target")
	})
}
