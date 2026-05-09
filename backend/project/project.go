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

// ProjectStore provides persistent storage for projects.
type ProjectStore interface {
	SaveProject(ctx context.Context, info ProjectInfo) error
	LoadProject(ctx context.Context, id string) (*ProjectInfo, error)
	ListProjects(ctx context.Context) ([]ProjectInfo, error)
	DeleteProject(ctx context.Context, id string) error
	RenameProject(ctx context.Context, id, name string) error
	UpdateProjectActivity(ctx context.Context, id string) error
	Close() error
}
