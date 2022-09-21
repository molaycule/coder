package codersdk

import (
	"github.com/google/uuid"
)

type WorkspaceAppHealth string

const (
	WorkspaceAppHealthDisabled     WorkspaceAppHealth = "disabled"
	WorkspaceAppHealthInitializing WorkspaceAppHealth = "initializing"
	WorkspaceAppHealthHealthy      WorkspaceAppHealth = "healthy"
	WorkspaceAppHealthUnhealthy    WorkspaceAppHealth = "unhealthy"
)

type WorkspaceApp struct {
	ID uuid.UUID `json:"id"`
	// Name is a unique identifier attached to an agent.
	Name    string `json:"name"`
	Command string `json:"command,omitempty"`
	// Icon is a relative path or external URL that specifies
	// an icon to be displayed in the dashboard.
	Icon string `json:"icon,omitempty"`
	// HealthcheckURL specifies the url to check for the app health.
	HealthcheckURL string `json:"healthcheck_url"`
	// HealthcheckInterval specifies the seconds between each health check.
	HealthcheckInterval int32 `json:"healthcheck_interval"`
	// HealthcheckThreshold specifies the number of consecutive failed health checks before returning "unhealthy".
	HealthcheckThreshold int32 `json:"healthcheck_threshold"`
	// Health specifies the current status of the app's health.
	Health WorkspaceAppHealth `json:"health"`
}

// @typescript-ignore PostWorkspaceAppHealthsRequest
type PostWorkspaceAppHealthsRequest struct {
	// Healths is a map of the workspace app name and the health of the app.
	Healths map[string]WorkspaceAppHealth
}
