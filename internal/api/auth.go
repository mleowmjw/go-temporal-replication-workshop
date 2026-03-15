package api

import (
	"net/http"

	"app/internal/domain"
)

// Authorizer checks whether a request is allowed to perform an action on a tenant.
type Authorizer interface {
	Authorize(r *http.Request, tenant domain.TenantID, action string) error
}

// AllowAllAuthorizer permits every request — suitable for local development and tests.
type AllowAllAuthorizer struct{}

func (AllowAllAuthorizer) Authorize(_ *http.Request, _ domain.TenantID, _ string) error {
	return nil
}
