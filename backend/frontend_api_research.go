package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/research"
)

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

// ResearchStatusDTO is the structured response for GetResearchStatus. It is the
// full Research-panel view model: the toggle state plus the parsed research
// root (graph + metrics) when RESEARCH mode is enabled.
//
// When Enabled is false, ResearchRoot and Root are empty/nil — this is the
// "empty state" the frontend renders when RESEARCH is off (or the project has
// no research root yet).
type ResearchStatusDTO struct {
	// Enabled reports whether RESEARCH mode is active for the project (a real,
	// non-No-Project project with a non-empty persisted ResearchRoot).
	Enabled bool `json:"enabled"`

	// ProjectID is the project these results pertain to.
	ProjectID string `json:"project_id"`

	// ResearchRoot is the absolute path to the research workspace directory
	// (config.ProjectResearchPath) when enabled; "" otherwise.
	ResearchRoot string `json:"research_root"`

	// Root is the parsed research root (index, projects, each project's graph
	// + metrics + brief + prior-art). It is nil when Enabled is false or when
	// the directory could not be parsed (treated as an empty state).
	Root *research.ResearchRoot `json:"root,omitempty"`

	// SeedResult, populated only by EnableResearch, reports the per-skill
	// outcome of seeding the research skill-pack into the project's local
	// .agents/skills directory. Nil for GetResearchStatus / DisableResearch.
	SeedResult *ResearchSeedResultDTO `json:"seed_result,omitempty"`
}

// ResearchSeedResultDTO mirrors research.SeedSkillsResult for the frontend:
// which skills were newly seeded, overwritten, left current, or preserved
// (user-owned).
type ResearchSeedResultDTO struct {
	Seeded    []string `json:"seeded"`
	Updated   []string `json:"updated"`
	Current   []string `json:"current"`
	Preserved []string `json:"preserved"`
}

// ---------------------------------------------------------------------------
// EnableResearch
// ---------------------------------------------------------------------------

// EnableResearch activates RESEARCH mode for a project. It:
//   - Resolves the research root directory (rootPath, or the project's default
//     <workspace>/.research when rootPath is empty) and creates it if missing.
//   - Seeds the seven research-* methodology skills into the project's local
//     .agents/skills directory (idempotent, non-destructive — see Task 4).
//   - Persists the research root on the project (ProjectInfo.ResearchRoot) so
//     the toggle survives restarts.
//   - Invalidates the skill cache so ListSkills picks up the seeded skills.
//   - Emits a research:changed event (action="enabled").
//
// It returns the parsed research status (graph + metrics + seed result) so the
// frontend can render the panel immediately. Enabling on an already-enabled
// project is idempotent (re-seeds, re-persists, re-emits).
func (f *FrontendAPI) EnableResearch(projectID, rootPath string) (*ResearchStatusDTO, error) {
	if projectID == "" {
		return nil, errors.New("project_id is required")
	}
	if f.projectManager == nil || f.projStore == nil {
		return nil, errors.New("project subsystem not initialized")
	}

	proj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return nil, err
	}

	// Resolve the research root. Default to the project's canonical research
	// directory when no explicit root is supplied. When an explicit rootPath is
	// given, it MUST reside within the project workspace (centralized path
	// containment — SECURITY.md): an out-of-workspace root is rejected rather
	// than silently persisted, since this is a UI binding and a future caller
	// could otherwise point research artifacts outside the workspace.
	researchRoot := rootPath
	if researchRoot == "" {
		researchRoot = config.ProjectResearchPath(proj.WorkspacePath)
	} else {
		// Explicit root: enforce workspace containment.
		contained, withinErr := config.IsWithinPath(proj.WorkspacePath, researchRoot)
		if withinErr != nil {
			return nil, fmt.Errorf("failed to validate research root path containment: %w", withinErr)
		}
		if !contained {
			return nil, fmt.Errorf("research root %q must be inside the project workspace %q", researchRoot, proj.WorkspacePath)
		}
	}
	if abs, absErr := filepath.Abs(researchRoot); absErr == nil {
		researchRoot = abs
	}

	// Create the research root directory (config.ProjectResearchPath is
	// explicitly created-lazily by this activating layer).
	if err := os.MkdirAll(researchRoot, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create research root %q: %w", researchRoot, err)
	}

	// Seed the research skill-pack into the project's local skills directory.
	skillsDir := config.ProjectSkillsPath(proj.WorkspacePath)
	seedRes, seedErr := research.SeedSkills(skillsDir, f.log())
	if seedErr != nil {
		// Seeding failure is non-fatal to enabling RESEARCH mode itself, but
		// surface it so the user knows the methodology skills are missing.
		f.log().Warn("research.EnableResearch: skill seeding failed",
			"project_id", projectID, "skills_dir", skillsDir, "error", seedErr)
	}
	if seedRes != nil {
		f.log().Info("research.EnableResearch: skills seeded",
			"project_id", projectID,
			"seeded", len(seedRes.Seeded), "updated", len(seedRes.Updated),
			"current", len(seedRes.Current), "preserved", len(seedRes.Preserved))
	}

	// Persist the research root on the project.
	proj.ResearchRoot = researchRoot
	if err := f.projStore.SaveProject(context.Background(), *proj); err != nil {
		return nil, fmt.Errorf("failed to persist research root: %w", err)
	}

	// Invalidate the skill cache so the next ListSkills re-scans and picks up
	// the freshly seeded research-* skills.
	f.invalidateSkillCache()

	// Reload the skill catalog for any already-running sessions of this project
	// so the research-* skills are discoverable without a restart. Without this,
	// a session created before RESEARCH was enabled would emit research router
	// hints referencing skills it has not loaded, so natural-language matching
	// would silently no-op until restart.
	if f.app != nil {
		if manager := f.app.Manager(); manager != nil {
			manager.RescanSkillsForProject(projectID)
		}
	}

	// Parse the research root for the response.
	root := f.parseResearchRootBestEffort(researchRoot)

	status := &ResearchStatusDTO{
		Enabled:      true,
		ProjectID:    projectID,
		ResearchRoot: researchRoot,
		Root:         root,
		SeedResult:   toSeedResultDTO(seedRes),
	}

	f.emitEvent(EventResearchChanged, map[string]string{
		"project_id": projectID,
		"action":     "enabled",
	})

	return status, nil
}

// ---------------------------------------------------------------------------
// DisableResearch
// ---------------------------------------------------------------------------

// DisableResearch deactivates RESEARCH mode for a project by clearing its
// persisted research root. It does NOT delete the research workspace directory
// or remove the seeded skills — only the toggle is cleared, so re-enabling
// later restores the prior state without data loss. Emits a research:changed
// event (action="disabled").
func (f *FrontendAPI) DisableResearch(projectID string) error {
	if projectID == "" {
		return errors.New("project_id is required")
	}
	if f.projectManager == nil || f.projStore == nil {
		return errors.New("project subsystem not initialized")
	}

	proj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return err
	}

	// Clear the toggle.
	proj.ResearchRoot = ""
	if err := f.projStore.SaveProject(context.Background(), *proj); err != nil {
		return fmt.Errorf("failed to clear research root: %w", err)
	}

	f.emitEvent(EventResearchChanged, map[string]string{
		"project_id": projectID,
		"action":     "disabled",
	})

	return nil
}

// ---------------------------------------------------------------------------
// GetResearchStatus
// ---------------------------------------------------------------------------

// GetResearchStatus returns the live RESEARCH mode state for a project: the
// toggle plus the parsed research root (graph + metrics + project list) when
// enabled. When RESEARCH is disabled (no research root, or the No Project
// pseudo-project), it returns an empty-state DTO (Enabled=false, nil Root)
// rather than an error.
func (f *FrontendAPI) GetResearchStatus(projectID string) (*ResearchStatusDTO, error) {
	if projectID == "" {
		return nil, errors.New("project_id is required")
	}
	if f.projectManager == nil {
		return nil, errors.New("project subsystem not initialized")
	}

	proj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return nil, err
	}

	// Empty state when no research root is persisted.
	if proj.ResearchRoot == "" {
		return &ResearchStatusDTO{
			Enabled:   false,
			ProjectID: projectID,
		}, nil
	}

	root := f.parseResearchRootBestEffort(proj.ResearchRoot)
	return &ResearchStatusDTO{
		Enabled:      true,
		ProjectID:    projectID,
		ResearchRoot: proj.ResearchRoot,
		Root:         root,
	}, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// loadProjectForResearch loads a project by ID, rejecting the No Project
// pseudo-project (RESEARCH mode is only meaningful for real projects with a
// workspace).
func (f *FrontendAPI) loadProjectForResearch(projectID string) (*project.ProjectInfo, error) {
	proj, err := f.projectManager.GetProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to load project %q: %w", projectID, err)
	}
	if proj == nil {
		return nil, fmt.Errorf("project %q not found", projectID)
	}
	if proj.IsNoProject {
		return nil, errors.New("RESEARCH mode is not available for the No Project pseudo-project")
	}
	return proj, nil
}

// parseResearchRootBestEffort parses a research root directory into a
// ResearchRoot model, tolerating a missing/unreadable directory by returning
// nil (so the frontend renders an empty enabled state rather than erroring).
// A parse error is logged but not propagated: the toggle is still "enabled",
// and partial content is the norm for a freshly-initialized research root.
func (f *FrontendAPI) parseResearchRootBestEffort(researchRoot string) *research.ResearchRoot {
	if researchRoot == "" {
		return nil
	}
	root, err := research.ParseResearchRoot(researchRoot)
	if err != nil {
		f.log().Debug("research.GetResearchStatus: root not yet parseable",
			"root", researchRoot, "error", err)
		return nil
	}
	return root
}

// toSeedResultDTO converts a research.SeedSkillsResult into the frontend-facing
// DTO, returning nil when the input is nil (so the JSON field is omitted).
func toSeedResultDTO(r *research.SeedSkillsResult) *ResearchSeedResultDTO {
	if r == nil {
		return nil
	}
	return &ResearchSeedResultDTO{
		Seeded:    r.Seeded,
		Updated:   r.Updated,
		Current:   r.Current,
		Preserved: r.Preserved,
	}
}
