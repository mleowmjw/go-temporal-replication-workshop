package temporal_test

import (
	"context"
	"errors"
	"testing"

	"app/internal/connectors"
	"app/internal/domain"
	"app/internal/store"
	apptemporal "app/internal/temporal"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// actCtx is a plain background context — activities are called directly here, not via Temporal.
var actCtx = context.Background()

func newActivities(t *testing.T) (*apptemporal.Activities, *store.InMemoryMetadataStore) {
	t.Helper()
	ms := store.NewInMemoryMetadataStore()
	deps := apptemporal.Dependencies{
		Store:          ms,
		Secrets:        connectors.NewInMemorySecretProvider(),
		SchemaRegistry: connectors.NewInMemorySchemaRegistry(),
		CDC:            connectors.NewFakeCDCProvisioner(),
		Stream:         connectors.NewFakeStreamProvisioner(),
		Sink:           connectors.NewFakeSinkProvisioner(),
	}
	return apptemporal.NewActivities(deps), ms
}

func seedPipelineInStore(t *testing.T, ms *store.InMemoryMetadataStore, spec domain.PipelineSpec) {
	t.Helper()
	require.NoError(t, ms.CreateTenant(actCtx, domain.Tenant{ID: spec.TenantID, Name: "test"}))
	require.NoError(t, ms.CreatePipeline(actCtx, spec))
}

func TestValidatePipelineSpecActivity(t *testing.T) {
	acts, _ := newActivities(t)
	spec := domain.PipelineSpec{
		TenantID:   "t1",
		PipelineID: "p1",
		Name:       "test",
		Source: domain.SourceSpec{Type: domain.SourcePostgres, SecretRef: "s", Tables: []string{"t"}},
		Sink:   domain.SinkSpec{Type: domain.SinkSearch, Target: "idx"},
	}
	require.NoError(t, acts.ValidatePipelineSpecActivity(actCtx, spec))

	invalid := spec
	invalid.Name = ""
	assert.Error(t, acts.ValidatePipelineSpecActivity(actCtx, invalid))
}

func TestValidateSchemaPolicyActivity(t *testing.T) {
	acts, _ := newActivities(t)

	t.Run("no subject - skips validation", func(t *testing.T) {
		in := apptemporal.ValidateSchemaPolicyActivityInput{
			Spec:   domain.PipelineSpec{Schema: domain.SchemaSpec{}},
			Schema: "",
		}
		require.NoError(t, acts.ValidateSchemaPolicyActivity(actCtx, in))
	})

	t.Run("no prior schema - passes", func(t *testing.T) {
		in := apptemporal.ValidateSchemaPolicyActivityInput{
			Spec: domain.PipelineSpec{
				Schema: domain.SchemaSpec{
					Subject:       "new-subject",
					Compatibility: domain.CompatBackward,
				},
			},
			Schema: `{"fields":[{"name":"id","required":true}]}`,
		}
		require.NoError(t, acts.ValidateSchemaPolicyActivity(actCtx, in))
	})
}

func TestEnsureSchemaSubjectActivity(t *testing.T) {
	acts, _ := newActivities(t)

	t.Run("empty subject returns zero id", func(t *testing.T) {
		id, err := acts.EnsureSchemaSubjectActivity(actCtx, domain.PipelineSpec{})
		require.NoError(t, err)
		assert.Equal(t, 0, id)
	})

	t.Run("registers schema and returns id", func(t *testing.T) {
		spec := domain.PipelineSpec{
			Schema: domain.SchemaSpec{
				Subject:       "inventory.customers",
				Format:        `{"fields":[{"name":"id","required":true}]}`,
				Compatibility: domain.CompatBackward,
			},
		}
		id, err := acts.EnsureSchemaSubjectActivity(actCtx, spec)
		require.NoError(t, err)
		assert.Equal(t, 1, id)
	})

	t.Run("defaults to BACKWARD when no compat set", func(t *testing.T) {
		spec := domain.PipelineSpec{
			Schema: domain.SchemaSpec{
				Subject: "subject-no-compat",
				Format:  `{"fields":[{"name":"x"}]}`,
			},
		}
		id, err := acts.EnsureSchemaSubjectActivity(actCtx, spec)
		require.NoError(t, err)
		assert.Equal(t, 2, id) // second registration in this test instance
	})
}

func TestPrepareSourceActivity(t *testing.T) {
	acts, _ := newActivities(t)

	spec := domain.PipelineSpec{
		Source: domain.SourceSpec{Type: domain.SourcePostgres, SecretRef: "s", Tables: []string{"t"}},
	}
	require.NoError(t, acts.PrepareSourceActivity(actCtx, spec))
}

func TestPrepareSourceActivity_Error(t *testing.T) {
	ms := store.NewInMemoryMetadataStore()
	fakeCDC := connectors.NewFakeCDCProvisioner()
	fakeCDC.PrepareSourceFn = func(_ domain.SourceSpec) error {
		return errors.New("pg error")
	}
	acts := apptemporal.NewActivities(apptemporal.Dependencies{
		Store:          ms,
		Secrets:        connectors.NewInMemorySecretProvider(),
		SchemaRegistry: connectors.NewInMemorySchemaRegistry(),
		CDC:            fakeCDC,
		Stream:         connectors.NewFakeStreamProvisioner(),
		Sink:           connectors.NewFakeSinkProvisioner(),
	})
	assert.ErrorContains(t, acts.PrepareSourceActivity(actCtx, domain.PipelineSpec{}), "prepare source")
}

func TestEnsureStreamActivity(t *testing.T) {
	acts, _ := newActivities(t)
	spec := domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"}
	id, err := acts.EnsureStreamActivity(actCtx, spec)
	require.NoError(t, err)
	assert.NotEmpty(t, id)
}

func TestEnsureStreamActivity_Error(t *testing.T) {
	ms := store.NewInMemoryMetadataStore()
	fakeStream := connectors.NewFakeStreamProvisioner()
	fakeStream.EnsureStreamFn = func(_ domain.TenantID, _ domain.PipelineID, _ string) (string, error) {
		return "", errors.New("stream err")
	}
	acts := apptemporal.NewActivities(apptemporal.Dependencies{
		Store:          ms,
		Secrets:        connectors.NewInMemorySecretProvider(),
		SchemaRegistry: connectors.NewInMemorySchemaRegistry(),
		CDC:            connectors.NewFakeCDCProvisioner(),
		Stream:         fakeStream,
		Sink:           connectors.NewFakeSinkProvisioner(),
	})
	_, err := acts.EnsureStreamActivity(actCtx, domain.PipelineSpec{})
	assert.ErrorContains(t, err, "ensure stream")
}

func TestStopCaptureActivity(t *testing.T) {
	acts, _ := newActivities(t)
	require.NoError(t, acts.StopCaptureActivity(actCtx, connectors.CaptureHandle{ID: "cap-1"}))
}

func TestDeleteSinkActivity(t *testing.T) {
	acts, _ := newActivities(t)
	require.NoError(t, acts.DeleteSinkActivity(actCtx, "sink-1"))
}

func TestDeleteStreamActivity(t *testing.T) {
	acts, _ := newActivities(t)
	require.NoError(t, acts.DeleteStreamActivity(actCtx, "stream-1"))
}

func TestMarkPipelineActiveActivity(t *testing.T) {
	acts, ms := newActivities(t)
	spec := domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"}
	seedPipelineInStore(t, ms, spec)

	in := apptemporal.MarkPipelineActiveActivityInput{Spec: spec}
	require.NoError(t, acts.MarkPipelineActiveActivity(actCtx, in))

	state, err := ms.GetPipelineState(actCtx, "t1", "p1")
	require.NoError(t, err)
	assert.Equal(t, domain.StateActive, state)
}

func TestMarkPipelineErrorActivity(t *testing.T) {
	acts, ms := newActivities(t)
	spec := domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"}
	seedPipelineInStore(t, ms, spec)

	require.NoError(t, acts.MarkPipelineErrorActivity(actCtx, spec))

	state, err := ms.GetPipelineState(actCtx, "t1", "p1")
	require.NoError(t, err)
	assert.Equal(t, domain.StateError, state)
}

func TestTenantTaskQueue(t *testing.T) {
	q := apptemporal.TenantTaskQueue("acme")
	assert.Equal(t, "replication.acme", q)
}
