// Package project provides project-scoped persistence and lifecycle management
// for the desktop UI layer.
package project

import "context"

// ProjectInfo is the public-facing project metadata.
type ProjectInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	WorkspacePath string `json:"workspace_path"`
	IsExternal    bool   `json:"is_external"`
	CreatedAt     string `json:"created_at"`
	LastActiveAt  string `json:"last_active_at"`
}

// ProjectUIState stores per-project UI state used during project switches.
type ProjectUIState struct {
	ProjectID      string   `json:"project_id"`
	SavedSessionID string   `json:"saved_session_id"`
	OpenTabs       []string `json:"open_tabs"`
	ActiveFile     string   `json:"active_file"`
	UpdatedAt      string   `json:"updated_at"`
}

// ProjectStore provides persistent storage for projects.
type ProjectStore interface {
	SaveProject(ctx context.Context, info ProjectInfo) error
	LoadProject(ctx context.Context, id string) (*ProjectInfo, error)
	ListProjects(ctx context.Context) ([]ProjectInfo, error)
	DeleteProject(ctx context.Context, id string) error
	RenameProject(ctx context.Context, id, name string) error
	UpdateProjectActivity(ctx context.Context, id string) error
	SaveUIState(ctx context.Context, state ProjectUIState) error
	LoadUIState(ctx context.Context, projectID string) (*ProjectUIState, error)
	Close() error
}
