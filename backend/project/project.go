// Package project provides project-scoped persistence and lifecycle management
// for the desktop UI layer.
package project

import (
	"context"
	"errors"

	"github.com/v0lka/c0wrk/core"
)

// NoProjectID is the well-known identifier of the "No Project" pseudo-project:
// per-session workspaces, index-dependent tools disabled.
// See specs/domains/workspace.md.
const NoProjectID = core.NoProjectID

// ResearchPins is the per-project set of pinned research artifacts shown in
// the RESEARCH UI: root-relative document paths (Research), pinned card
// paths per H-NNN (Hypotheses), pinned paper cards (Papers). Path style and
// pin RPCs: specs/domains/papers.md.
type ResearchPins struct {
	Research   []string            `json:"research"`
	Hypotheses map[string][]string `json:"hypotheses"`
	Papers     []string            `json:"papers,omitempty"`
}

// ProjectInfo is the public-facing project metadata.
type ProjectInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	WorkspacePath string `json:"workspace_path"`
	IsExternal    bool   `json:"is_external"`
	IsNoProject   bool   `json:"is_no_project"`
	// ResearchPins persists as NULL when all-empty, so absent pins
	// round-trip as absent.
	ResearchPins ResearchPins `json:"research_pins"`
	CreatedAt    string       `json:"created_at"`
	LastActiveAt string       `json:"last_active_at"`
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
// directory (an extra agent containment root). Scope and owner are implied
// by which store method is called, so they are not fields. RPC surface:
// specs/contracts/desktop-frontend.md (Work Directories).
type WorkDirectoryRecord struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
}

// ErrWorkDirAlreadyExists is returned when a work directory with the same
// path exists for an owner (both stores translate the UNIQUE violation).
var ErrWorkDirAlreadyExists = errors.New("work directory already exists for this project or session")

// AppStateKeyLastActiveProjectID persists the most recently activated
// project ID (NoProjectID included) for restart restore.
const AppStateKeyLastActiveProjectID = "last_active_project_id"

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
	// SaveSavedSessionID updates only saved_session_id, preserving open tabs.
	SaveSavedSessionID(ctx context.Context, projectID, sessionID string) error

	// App-level key-value state (e.g. last active project for restart restore)
	SaveAppState(ctx context.Context, key, value string) error
	LoadAppState(ctx context.Context, key string) (string, error)

	// Project-scoped work directories
	SaveProjectWorkDir(ctx context.Context, projectID string, rec WorkDirectoryRecord) error
	ListProjectWorkDirs(ctx context.Context, projectID string) ([]WorkDirectoryRecord, error)
	UpdateProjectWorkDirDescription(ctx context.Context, projectID, id, description string) error
	DeleteProjectWorkDir(ctx context.Context, projectID, id string) error

	Close() error
}
