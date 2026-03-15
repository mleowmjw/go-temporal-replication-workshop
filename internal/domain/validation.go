package domain

import (
	"errors"
	"fmt"
)

// ValidateTenant checks required fields on a Tenant.
func ValidateTenant(t Tenant) error {
	if t.ID == "" {
		return errors.New("tenant id is required")
	}
	if t.Name == "" {
		return errors.New("tenant name is required")
	}
	return nil
}

// ValidatePipelineSpec checks required fields on a PipelineSpec.
func ValidatePipelineSpec(s PipelineSpec) error {
	if s.TenantID == "" {
		return errors.New("tenant id is required")
	}
	if s.PipelineID == "" {
		return errors.New("pipeline id is required")
	}
	if s.Name == "" {
		return errors.New("pipeline name is required")
	}
	if err := validateSourceSpec(s.Source); err != nil {
		return fmt.Errorf("source: %w", err)
	}
	if err := validateSinkSpec(s.Sink); err != nil {
		return fmt.Errorf("sink: %w", err)
	}
	return nil
}

func validateSourceSpec(s SourceSpec) error {
	if s.Type == "" {
		return errors.New("source type is required")
	}
	switch s.Type {
	case SourcePostgres:
	default:
		return fmt.Errorf("unsupported source type %q", s.Type)
	}
	if s.SecretRef == "" {
		return errors.New("secret ref is required")
	}
	if len(s.Tables) == 0 {
		return errors.New("at least one table is required")
	}
	return nil
}

func validateSinkSpec(s SinkSpec) error {
	if s.Type == "" {
		return errors.New("sink type is required")
	}
	switch s.Type {
	case SinkSearch, SinkLake, SinkPostgres:
	default:
		return fmt.Errorf("unsupported sink type %q", s.Type)
	}
	if s.Target == "" {
		return errors.New("sink target is required")
	}
	return nil
}
