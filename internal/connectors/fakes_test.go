package connectors_test

import (
	"errors"
	"testing"

	"app/internal/connectors"
	"app/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeCDCProvisioner_Defaults(t *testing.T) {
	f := connectors.NewFakeCDCProvisioner()

	require.NoError(t, f.PrepareSource(ctx, domain.SourceSpec{Database: "appdb", Schema: "inventory"}))

	h, err := f.StartCapture(ctx, domain.SourceSpec{Database: "appdb", Schema: "inventory"})
	require.NoError(t, err)
	assert.NotEmpty(t, h.ID)
	assert.Len(t, f.Captures, 1)

	require.NoError(t, f.StopCapture(ctx, h))
}

func TestFakeCDCProvisioner_InjectedErrors(t *testing.T) {
	errBoom := errors.New("boom")

	t.Run("PrepareSourceFn error", func(t *testing.T) {
		f := connectors.NewFakeCDCProvisioner()
		f.PrepareSourceFn = func(_ domain.SourceSpec) error { return errBoom }
		assert.ErrorIs(t, f.PrepareSource(ctx, domain.SourceSpec{}), errBoom)
	})

	t.Run("StartCaptureFn error", func(t *testing.T) {
		f := connectors.NewFakeCDCProvisioner()
		f.StartCaptureFn = func(_ domain.SourceSpec) (connectors.CaptureHandle, error) {
			return connectors.CaptureHandle{}, errBoom
		}
		_, err := f.StartCapture(ctx, domain.SourceSpec{})
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("StopCaptureFn error", func(t *testing.T) {
		f := connectors.NewFakeCDCProvisioner()
		f.StopCaptureFn = func(_ connectors.CaptureHandle) error { return errBoom }
		assert.ErrorIs(t, f.StopCapture(ctx, connectors.CaptureHandle{ID: "h1"}), errBoom)
	})
}

func TestFakeStreamProvisioner_Defaults(t *testing.T) {
	f := connectors.NewFakeStreamProvisioner()

	id, err := f.EnsureStream(ctx, "t1", "p1", "subject-a")
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	assert.Len(t, f.Streams, 1)

	require.NoError(t, f.DeleteStream(ctx, id))
	assert.Empty(t, f.Streams)

	// Deleting a non-existent stream is a no-op.
	require.NoError(t, f.DeleteStream(ctx, "nonexistent"))
}

func TestFakeStreamProvisioner_InjectedErrors(t *testing.T) {
	errBoom := errors.New("stream-err")

	t.Run("EnsureStreamFn error", func(t *testing.T) {
		f := connectors.NewFakeStreamProvisioner()
		f.EnsureStreamFn = func(_ domain.TenantID, _ domain.PipelineID, _ string) (string, error) {
			return "", errBoom
		}
		_, err := f.EnsureStream(ctx, "t1", "p1", "sub")
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("DeleteStreamFn error", func(t *testing.T) {
		f := connectors.NewFakeStreamProvisioner()
		f.DeleteStreamFn = func(_ string) error { return errBoom }
		assert.ErrorIs(t, f.DeleteStream(ctx, "s1"), errBoom)
	})
}

func TestFakeSinkProvisioner_Defaults(t *testing.T) {
	f := connectors.NewFakeSinkProvisioner()
	spec := domain.SinkSpec{Type: domain.SinkSearch, Target: "idx"}

	id, err := f.EnsureSink(ctx, spec)
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	assert.Len(t, f.Sinks, 1)

	require.NoError(t, f.DeleteSink(ctx, id))
	assert.Empty(t, f.Sinks)

	// Deleting a non-existent sink is a no-op.
	require.NoError(t, f.DeleteSink(ctx, "nonexistent"))
}

func TestFakeSinkProvisioner_InjectedErrors(t *testing.T) {
	errBoom := errors.New("sink-err")

	t.Run("EnsureSinkFn call-based error", func(t *testing.T) {
		f := connectors.NewFakeSinkProvisioner()
		f.EnsureSinkFn = func(n int, _ domain.SinkSpec) (string, error) {
			if n == 1 {
				return "", errBoom
			}
			return "sink-ok", nil
		}
		_, err := f.EnsureSink(ctx, domain.SinkSpec{})
		assert.ErrorIs(t, err, errBoom)

		id, err := f.EnsureSink(ctx, domain.SinkSpec{})
		require.NoError(t, err)
		assert.Equal(t, "sink-ok", id)
	})

	t.Run("DeleteSinkFn error", func(t *testing.T) {
		f := connectors.NewFakeSinkProvisioner()
		f.DeleteSinkFn = func(_ string) error { return errBoom }
		assert.ErrorIs(t, f.DeleteSink(ctx, "s1"), errBoom)
	})
}
