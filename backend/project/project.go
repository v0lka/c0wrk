// Package project provides project-scoped persistence and lifecycle management
// for the desktop UI layer.
package project

import (
	"context"
	"errors"

	"github.com/v0lka/c0wrk/core"
)

// NoProjectID is the well-known identifier for the "No Project" pseudo-project.
// Sessions under this project receive per-session workspaces and code-oriented
// tools are disabled.
// Defined in core/types.go; re-exported here for convenience.
const NoProjectID = core.NoProjectID

// ProjectInfo is the public-facing project metadata.
type ProjectInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	WorkspacePath string `json:"workspace_path"`
	IsExternal    bool   `json:"is_external"`
	IsNoProject   bool   `json:"is_no_project"`
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

// WorkDirectoryRecord is the persistence record for an auxiliary working
// directory exposed to the agent as an additional containment root.
//
// Scope (project vs session) and owner are implied by which store method is
// called, so they are not fields here. The minimal agent-facing shape lives in
// core.WorkDirectory.
type WorkDirectoryRecord struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
}

// ErrWorkDirAlreadyExists is returned when a work directory with the same path
// already exists for a given owner. Both project- and session-scoped stores
// translate a unique-constraint violation into this sentinel.
var ErrWorkDirAlreadyExists = errors.New("work directory already exists for this project or session")

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

	// Project-scoped work directories
	SaveProjectWorkDir(ctx context.Context, projectID string, rec WorkDirectoryRecord) error
	ListProjectWorkDirs(ctx context.Context, projectID string) ([]WorkDirectoryRecord, error)
	UpdateProjectWorkDirDescription(ctx context.Context, projectID, id, description string) error
	DeleteProjectWorkDir(ctx context.Context, projectID, id string) error

	Close() error
}
