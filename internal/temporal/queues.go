package temporal

import "fmt"

const (
	// DefaultTaskQueue is used when no per-tenant queue is needed.
	DefaultTaskQueue = "replication-platform"

	// WorkerBuildID identifies this binary version for Temporal worker versioning.
	WorkerBuildID = "session-1-v1"
)

// TenantTaskQueue returns the per-tenant task queue name.
func TenantTaskQueue(tenantID string) string {
	return fmt.Sprintf("replication.%s", tenantID)
}
