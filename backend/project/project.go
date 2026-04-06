// Package project provides project-scoped persistence for the desktop UI.
package project

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
	SaveProject(info ProjectInfo) error
	LoadProject(id string) (*ProjectInfo, error)
	ListProjects() ([]ProjectInfo, error)
	DeleteProject(id string) error
	RenameProject(id, name string) error
	UpdateProjectActivity(id string) error
	Close() error
}
