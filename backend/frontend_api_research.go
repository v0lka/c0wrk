package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

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

	// PinnedResearch lists the project's pinned research document paths —
	// each the brief of a pinned R-NNN, relative to the research root with
	// forward slashes (mirrors the persisted ProjectInfo.ResearchPins).
	// Always non-nil (empty slice, not null) so the wire shape is stable.
	PinnedResearch []string `json:"pinned_research"`

	// PinnedHypotheses maps a hypothesis id (H-NNN) to the pinned card paths
	// belonging to that hypothesis (relative to the research root, forward
	// slashes). The value is a list because the same H-NNN exists across
	// R-NNN projects. Always non-nil (empty map, not null).
	PinnedHypotheses map[string][]string `json:"pinned_hypotheses"`
}

// ResearchGraphDTO is the lightweight response for GetResearchGraph. It
// carries only the hypothesis graph (nodes + edges) and computed metrics
// for a single research project — no brief, seed result, or root metadata.
// Used by the frontend's incremental file-change update path so the full
// status fetch is avoided when only hypothesis cards changed.
type ResearchGraphDTO struct {
	ProjectID string                      `json:"project_id"`
	Graph     ResearchGraph               `json:"graph"`
	Metrics   ResearchMetrics             `json:"metrics"`
	HasReport bool                        `json:"has_report"`
	Log       []research.ResearchLogEntry `json:"log"`
}

// ResearchGraph holds the hypothesis graph (nodes and edges) for a single
// research project.
type ResearchGraph struct {
	Nodes []research.HypothesisNode `json:"nodes"`
	Edges []research.HypothesisEdge `json:"edges"`
}

// ResearchMetrics holds the computed progress metrics for a research project.
// ByStatus keys are stringified HypothesisStatus values.
type ResearchMetrics struct {
	Total            int            `json:"total"`
	ByStatus         map[string]int `json:"by_status"`
	ConfirmationRate float64        `json:"confirmation_rate"`
	Depth            int            `json:"depth"`
	Breadth          int            `json:"breadth"`
	ActiveFront      []string       `json:"active_front,omitempty"`
}

// ResearchSeedResultDTO mirrors research.SeedSkillsResult for the frontend:
// which skills were newly seeded, overwritten, left current, preserved
// (user-owned), or modified (pack-marked but locally diverged).
type ResearchSeedResultDTO struct {
	Seeded    []string `json:"seeded"`
	Updated   []string `json:"updated"`
	Current   []string `json:"current"`
	Preserved []string `json:"preserved"`
	Modified  []string `json:"modified"`
}

// ResearchNextStepDTO is the small response for GetResearchNextStep: the single
// recommended next research action for the active project's current phase.
// Target is empty when the action is not scoped to a single hypothesis.
//
// ProjectID is DUAL-NAMESPACE BY DESIGN (documented in
// specs/domains/research.md): it names the SUBJECT of the recommendation —
// the active R-NNN when one exists, the c0wrk project UUID otherwise (the
// research-init setup state, before any R-NNN exists). It is NOT a stable
// identity for the requesting c0wrk project: frontend consumers must not
// key cross-project state off this field alone (the research store guards
// project switches itself and drops the recommendation on switch).
type ResearchNextStepDTO struct {
	ProjectID string `json:"project_id"`
	Action    string `json:"action"`
	Target    string `json:"target,omitempty"`
	Reason    string `json:"reason"`
	Skill     string `json:"skill"`
}

// HypothesisUpdateFields is the structured update payload for UpdateHypothesis.
// Pointer fields distinguish "leave unchanged" (nil) from "set to empty"
// (a non-nil pointer to "" / an empty Parents slice). Identifier, created,
// and completed are not editable through this path.
type HypothesisUpdateFields struct {
	Title    *string `json:"title,omitempty"`
	Status   *string `json:"status,omitempty"`
	Result   *string `json:"result,omitempty"`
	Timebox  *string `json:"timebox,omitempty"`
	Decision *string `json:"decision,omitempty"`
	// Long-form card sections (verbatim Markdown bodies).
	Statement             *string `json:"statement,omitempty"`
	VerificationCriterion *string `json:"verification_criterion,omitempty"`
	ExperimentNotes       *string `json:"experiment_notes,omitempty"`
	// Parents replaces the card's parent set; validated server-side
	// (existence, no self-reference, no cycle) before any write.
	Parents *[]string `json:"parents,omitempty"`
}

// NewHypothesisCard is the structured create payload for CreateHypothesis.
type NewHypothesisCard struct {
	Title                 string   `json:"title"`
	Statement             string   `json:"statement,omitempty"`
	VerificationCriterion string   `json:"verification_criterion,omitempty"`
	Timebox               string   `json:"timebox,omitempty"`
	Parents               []string `json:"parents,omitempty"`
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

	// Track the active research root so the workspace watcher callback can
	// emit research:file_changed events for file modifications inside it.
	// Mirror DisableResearch's guard: the active-project trio drives
	// CreateSession, ListSessions, resolveWorkspacePath (file tree), git
	// status, and the skills-cache key, and EnableResearch may legitimately be
	// invoked for a project that is NOT the active one (a stale project id
	// during a project-switch race, or any non-UI binding caller). Writing the
	// trio unconditionally would silently retarget those operations without
	// any of the project-switch flow (no watcher swap, no project:switched
	// event, no session refresh). Research-root tracking is only needed for
	// the ACTIVE project's watcher; the persisted proj.ResearchRoot below
	// makes a later switch to this project pick the root up anyway
	// (switchProject sets activeResearchRoot from the project record).
	f.activeProjectMu.Lock()
	if f.activeProjectID == projectID {
		f.activeProjectPath = proj.WorkspacePath
		f.activeResearchRoot = researchRoot
	}
	f.activeProjectMu.Unlock()

	// Recursively watch the research artifact tree so the file watcher
	// detects edits to hypothesis cards / brief / graph in nested
	// subdirectories (.research/R-NNN/hypotheses/…). The watcher was created
	// at project-switch time but did not watch the research tree (research
	// was off then); add it now. Best-effort: a nil watcher (e.g. early in
	// startup) is skipped — switchProjectSetupWatcher picks it up on the next
	// project switch.
	f.watcherMu.Lock()
	if f.watcher != nil {
		if werr := f.watcher.WatchTree(researchRoot); werr != nil {
			f.log().Debug("failed to watch research tree on enable",
				"root", researchRoot, "error", werr)
		}
	}
	f.watcherMu.Unlock()

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
			"current", len(seedRes.Current), "preserved", len(seedRes.Preserved),
			"modified", len(seedRes.Modified))
	}

	// Seed the built-in research Subagent Profile into the project's local
	// agents directory so research steps can be delegated via #research or
	// delegate(agent:"research"). Idempotent and non-destructive — mirrors the
	// skill-pack seeding above.
	agentsDir := config.ProjectAgentsPath(proj.WorkspacePath)
	agentSeedRes, agentSeedErr := research.SeedAgents(agentsDir, f.log())
	if agentSeedErr != nil {
		// Seeding failure is non-fatal to enabling RESEARCH mode itself, but
		// surface it so the user knows the research profile is missing.
		f.log().Warn("research.EnableResearch: agent seeding failed",
			"project_id", projectID, "agents_dir", agentsDir, "error", agentSeedErr)
	}
	if agentSeedRes != nil {
		f.log().Info("research.EnableResearch: agents seeded",
			"project_id", projectID,
			"seeded", len(agentSeedRes.Seeded), "updated", len(agentSeedRes.Updated),
			"current", len(agentSeedRes.Current), "preserved", len(agentSeedRes.Preserved),
			"modified", len(agentSeedRes.Modified))
	}

	// Persist the research root on the project.
	proj.ResearchRoot = researchRoot
	if err := f.projStore.SaveProject(context.Background(), *proj); err != nil {
		return nil, fmt.Errorf("failed to persist research root: %w", err)
	}

	// Invalidate the skill cache so the next ListSkills re-scans and picks up
	// the freshly seeded research-* skills.
	f.invalidateSkillCache()

	// Invalidate the agent cache so the next ListAgents re-scans and picks up
	// the freshly seeded research profile.
	f.invalidateAgentCache()

	// Reload the skill catalog for any already-running sessions of this project
	// so the research-* skills are discoverable without a restart. Without this,
	// a session created before RESEARCH was enabled would emit research router
	// hints referencing skills it has not loaded, so natural-language matching
	// would silently no-op until restart.
	if f.app != nil {
		if manager := f.app.Manager(); manager != nil {
			manager.RescanSkillsForProject(projectID)
			manager.RescanAgentsForProject(projectID)
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
	applyResearchPins(status, proj.ResearchPins)

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

	// Clear the tracked research root so the watcher stops emitting
	// research:file_changed events. Only activeResearchRoot is cleared — the
	// active project ID/path must be preserved so git, workspace, and session
	// operations continue to target the correct project after toggling
	// RESEARCH off.
	f.activeProjectMu.Lock()
	researchRootToUnwatch := ""
	if f.activeProjectID == projectID {
		researchRootToUnwatch = f.activeResearchRoot
		f.activeResearchRoot = ""
	}
	f.activeProjectMu.Unlock()

	// Stop watching the research artifact tree so file edits inside it no
	// longer emit research:file_changed. The tree was added by EnableResearch
	// (or switchProjectSetupWatcher); it must be explicitly removed because
	// fsnotify watches persist until Remove/Close.
	if researchRootToUnwatch != "" {
		f.watcherMu.Lock()
		if f.watcher != nil {
			if werr := f.watcher.UnwatchTree(researchRootToUnwatch); werr != nil {
				f.log().Debug("failed to unwatch research tree on disable",
					"root", researchRootToUnwatch, "error", werr)
			}
		}
		f.watcherMu.Unlock()
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
		status := &ResearchStatusDTO{
			Enabled:   false,
			ProjectID: projectID,
		}
		applyResearchPins(status, proj.ResearchPins)
		return status, nil
	}

	root := f.parseResearchRootBestEffort(proj.ResearchRoot)
	status := &ResearchStatusDTO{
		Enabled:      true,
		ProjectID:    projectID,
		ResearchRoot: proj.ResearchRoot,
		Root:         root,
	}
	applyResearchPins(status, proj.ResearchPins)
	return status, nil
}

// ---------------------------------------------------------------------------
// GetResearchGraph
// ---------------------------------------------------------------------------

// GetResearchGraph returns only the hypothesis graph and computed metrics for
// a single research project. It produces a smaller wire payload than
// GetResearchStatus (it omits the index, brief, prior-art, and seed result),
// which is useful for incremental file-change updates where only hypothesis
// cards have been modified. Note: the parse cost is identical to
// GetResearchStatus — both call parseResearchRootBestEffort (which parses the
// full research root); only the serialized JSON response is smaller.
func (f *FrontendAPI) GetResearchGraph(projectID string) (*ResearchGraphDTO, error) {
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
		return &ResearchGraphDTO{
			ProjectID: projectID,
		}, nil
	}

	// Parse the full root to get the active project.
	root := f.parseResearchRootBestEffort(proj.ResearchRoot)
	if root == nil {
		return &ResearchGraphDTO{
			ProjectID: projectID,
		}, nil
	}

	active := research.PickActiveProject(root)
	if active == nil {
		return &ResearchGraphDTO{
			ProjectID: projectID,
		}, nil
	}

	// Build the response from the active project's graph, metrics, and log.
	return researchGraphDTOFromProject(active), nil
}

// researchGraphDTOFromProject builds a ResearchGraphDTO from a parsed active
// research project (graph + metrics + report flag + log). Shared by
// GetResearchGraph and the mutation RPCs (UpdateHypothesis / CreateHypothesis)
// so they all serialize the same shape.
func researchGraphDTOFromProject(active *research.ResearchProject) *ResearchGraphDTO {
	dto := &ResearchGraphDTO{
		ProjectID: active.ID,
		HasReport: active.HasReport,
		Log:       active.Log,
	}

	for _, n := range active.Graph.Nodes {
		dto.Graph.Nodes = append(dto.Graph.Nodes, *n)
	}
	dto.Graph.Edges = active.Graph.Edges

	m := active.Metrics
	dto.Metrics.Total = m.Total
	dto.Metrics.ByStatus = make(map[string]int, len(m.ByStatus))
	for k, v := range m.ByStatus {
		dto.Metrics.ByStatus[string(k)] = v
	}
	dto.Metrics.ConfirmationRate = m.ConfirmationRate
	dto.Metrics.Depth = m.Depth
	dto.Metrics.Breadth = m.Breadth
	dto.Metrics.ActiveFront = m.ActiveFront

	return dto
}

// ---------------------------------------------------------------------------
// GetResearchNextStep
// ---------------------------------------------------------------------------

// GetResearchNextStep returns the single recommended next research action for
// a project, derived from the active R-NNN's current phase. A non-empty
// hypothesisID scopes the recommendation to THAT hypothesis (the dashboard's
// selected card) via RecommendNextStepForHypothesis: an open/in-progress
// hypothesis recommends an experiment on it, a terminal hypothesis without a
// recorded Decision recommends deciding on it. An empty hypothesisID means
// the project-level recommendation; an unknown hypothesis id (or one already
// carrying a Decision) falls back to it too. When there is no active R-NNN
// yet (an empty research root, a root with no projects, or RESEARCH not
// enabled), it returns the setup recommendation (research-init) rather than
// an error, so the dashboard always has a next step to show.
func (f *FrontendAPI) GetResearchNextStep(projectID, hypothesisID string) (*ResearchNextStepDTO, error) {
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

	// RESEARCH disabled (no persisted root) → setup recommendation.
	if proj.ResearchRoot == "" {
		return f.setupNextStep(projectID), nil
	}

	root := f.parseResearchRootBestEffort(proj.ResearchRoot)
	active := research.PickActiveProject(root) // handles nil root → nil
	rec := research.RecommendNextStepForHypothesis(active, hypothesisID)

	// Report the active R-NNN as the subject of the recommendation when one
	// exists; otherwise fall back to the requested project ID.
	dtoProjectID := projectID
	if active != nil {
		dtoProjectID = active.ID
	}
	return &ResearchNextStepDTO{
		ProjectID: dtoProjectID,
		Action:    string(rec.Action),
		Target:    rec.Target,
		Reason:    rec.Reason,
		Skill:     rec.Skill,
	}, nil
}

// setupNextStep returns the research-init setup recommendation for a project
// that has no active R-NNN yet (RESEARCH disabled, or an empty research root).
func (f *FrontendAPI) setupNextStep(projectID string) *ResearchNextStepDTO {
	rec := research.RecommendNextStep(nil)
	return &ResearchNextStepDTO{
		ProjectID: projectID,
		Action:    string(rec.Action),
		Target:    rec.Target,
		Reason:    rec.Reason,
		Skill:     rec.Skill,
	}
}

// ---------------------------------------------------------------------------
// UpdateHypothesis / CreateHypothesis
// ---------------------------------------------------------------------------

// UpdateHypothesis applies a structured update to a hypothesis card and its
// graph entries for the research project (R-NNN) named by researchID, then
// returns that project's refreshed graph. researchID must identify a research
// project that lives under the requesting project's research root: a foreign
// R-NNN (one belonging to another project's root) does not resolve there and
// is rejected before any file is touched, and the update targets the caller's
// expected project instead of blindly following the backend's active one —
// which may have moved on since the caller loaded its graph (cross-project /
// cross-R-NNN save race). Status transitions are validated against the
// methodology's state machine (open → in-progress → confirmed/refuted/
// cancelled; no backward transitions); a Parents update is validated against
// the reconciled graph (parents must exist, no self-reference, no cycle) and
// synchronized across the card's Parent(s) row, the Mermaid incoming edges,
// and the catalog's Parent(s) column. Any invalid update returns an error
// and leaves the card and graph unchanged.
func (f *FrontendAPI) UpdateHypothesis(projectID, researchID, hypothesisID string, fields HypothesisUpdateFields) (*ResearchGraphDTO, error) {
	researchRoot, _, err := f.researchRootForMutation(projectID)
	if err != nil {
		return nil, err
	}

	hid := research.NormalizeID(hypothesisID)
	if hid == "" {
		return nil, errors.New("invalid hypothesis id")
	}
	rid := research.NormalizeResearchID(researchID)
	if rid == "" {
		return nil, errors.New("invalid research project id (want R-NNN)")
	}

	// Serialize the whole load→mutate→write chain on this root: concurrent
	// UpdateHypothesis/CreateHypothesis calls otherwise interleave their
	// read-modify-write of card+graph (lost updates, torn card-vs-graph
	// writes) and race the max+1 H-NNN id assignment of CreateHypothesis.
	mu := f.researchMutationMu(researchRoot)
	mu.Lock()
	defer mu.Unlock()

	// Ownership check: resolve the expected R-NNN inside the REQUESTING
	// project's root. A research project of another project's root does not
	// resolve here and is rejected before any mutation runs.
	projectDir, err := research.ProjectDir(researchRoot, rid)
	if err != nil {
		return nil, err
	}

	upd := research.HypothesisUpdate{
		Title:                 fields.Title,
		Status:                fields.Status,
		Result:                fields.Result,
		Timebox:               fields.Timebox,
		Decision:              fields.Decision,
		Statement:             fields.Statement,
		VerificationCriterion: fields.VerificationCriterion,
		ExperimentNotes:       fields.ExperimentNotes,
		Parents:               fields.Parents,
	}
	if err := research.UpdateHypothesis(researchRoot, projectDir, hid, upd); err != nil {
		return nil, err
	}

	hypDir := filepath.Join(projectDir, "hypotheses")
	f.emitResearchFileChanged(researchRoot, projectID, []string{
		filepath.Join(hypDir, hid+".md"),
		filepath.Join(hypDir, "graph.md"),
	})

	return f.researchGraphAfterMutation(researchRoot, rid), nil
}

// CreateHypothesis creates a new hypothesis card (assigning the next H-NNN id)
// and updates the graph (Mermaid node + edges + catalog row) for the active
// R-NNN of a project, returning the refreshed graph. The whole
// resolve→allocate→write chain runs under the per-root mutation mutex so
// concurrent creators cannot both observe the same max H-NNN and overwrite
// each other's card (lost update / duplicate id).
func (f *FrontendAPI) CreateHypothesis(projectID string, newCard NewHypothesisCard) (*ResearchGraphDTO, error) {
	researchRoot, _, err := f.researchRootForMutation(projectID)
	if err != nil {
		return nil, err
	}

	mu := f.researchMutationMu(researchRoot)
	mu.Lock()
	defer mu.Unlock()

	projectDir, err := research.ActiveProjectDir(researchRoot)
	if err != nil {
		return nil, err
	}

	hid, err := research.CreateHypothesis(researchRoot, projectDir, research.NewHypothesis{
		Title:                 newCard.Title,
		Statement:             newCard.Statement,
		VerificationCriterion: newCard.VerificationCriterion,
		Timebox:               newCard.Timebox,
		Parents:               newCard.Parents,
	})
	if err != nil {
		return nil, err
	}

	hypDir := filepath.Join(projectDir, "hypotheses")
	f.emitResearchFileChanged(researchRoot, projectID, []string{
		filepath.Join(hypDir, hid+".md"),
		filepath.Join(hypDir, "graph.md"),
	})

	// rid "" → the response follows the active project, which is exactly the
	// project CreateHypothesis just wrote into.
	return f.researchGraphAfterMutation(researchRoot, ""), nil
}

// ---------------------------------------------------------------------------
// SetActiveResearch / DeleteResearch / pins
// ---------------------------------------------------------------------------

// SetActiveResearch makes the research project (R-NNN) named by researchID the
// active one for a project by rewriting the research root's index.md so the
// project's row becomes the last table entry — the chronological
// "last entry = active" rule PickActiveProject applies (a missing row is
// appended, a missing index.md created with the canonical skeleton). Like the
// hypothesis mutations, the whole resolve→write chain runs under the per-root
// mutation mutex, the research root is workspace-containment-checked, and rid
// must resolve under the REQUESTING project's root — a foreign R-NNN (one
// belonging to another project's root) is rejected before any file is touched.
// It returns the refreshed research status (with the new active project's
// graph and the project's pins) and emits a research:changed event
// (action="active_changed").
func (f *FrontendAPI) SetActiveResearch(projectID, researchID string) (*ResearchStatusDTO, error) {
	researchRoot, proj, err := f.researchRootForMutation(projectID)
	if err != nil {
		return nil, err
	}
	rid := research.NormalizeResearchID(researchID)
	if rid == "" {
		return nil, errors.New("invalid research project id (want R-NNN)")
	}

	// Serialize against concurrent root mutations: activation rewrites
	// index.md, which hypothesis mutations and deletions read.
	mu := f.researchMutationMu(researchRoot)
	mu.Lock()
	defer mu.Unlock()

	if err := research.SetActiveResearch(researchRoot, rid); err != nil {
		return nil, err
	}

	root := f.parseResearchRootBestEffort(researchRoot)
	status := &ResearchStatusDTO{
		Enabled:      true,
		ProjectID:    projectID,
		ResearchRoot: researchRoot,
		Root:         root,
	}
	applyResearchPins(status, proj.ResearchPins)

	f.emitEvent(EventResearchChanged, map[string]string{
		"project_id": projectID,
		"action":     "active_changed",
	})

	return status, nil
}

// DeleteResearch removes the research project (R-NNN) named by researchID from
// a project's research root: every index.md entry line referencing it, then
// its R-NNN-* directory tree in full. rid must resolve under the REQUESTING
// project's root (the ownership check runs before any file is touched), and
// the core-layer deletion is containment-checked (a symlinked or escaping
// project directory is rejected). Pins referencing the deleted project — its
// brief in the pinned research list and its cards in the pinned hypotheses
// map — are removed from ProjectInfo.ResearchPins and the cleaned record is
// persisted. It returns the refreshed research status (the remaining
// projects, with the next active one selected by the index rule) and emits a
// research:changed event (action="project_deleted").
func (f *FrontendAPI) DeleteResearch(projectID, researchID string) (*ResearchStatusDTO, error) {
	if f.projStore == nil {
		return nil, errors.New("project subsystem not initialized")
	}
	researchRoot, proj, err := f.researchRootForMutation(projectID)
	if err != nil {
		return nil, err
	}
	rid := research.NormalizeResearchID(researchID)
	if rid == "" {
		return nil, errors.New("invalid research project id (want R-NNN)")
	}

	mu := f.researchMutationMu(researchRoot)
	mu.Lock()
	defer mu.Unlock()

	// Ownership check BEFORE deleting — once the directory is gone the id no
	// longer resolves. The resolved directory also fixes the pin prefix
	// cleaned below.
	projectDir, err := research.ProjectDir(researchRoot, rid)
	if err != nil {
		return nil, err
	}
	pinDir, err := filepath.Rel(researchRoot, projectDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve project directory %q against the research root: %w", projectDir, err)
	}
	pinDir = filepath.ToSlash(pinDir)

	if err := research.DeleteResearchProject(researchRoot, rid); err != nil {
		return nil, err
	}

	// Clean the deleted R-NNN's pins (its brief + its hypothesis cards) and
	// persist the cleaned record. proj is a fresh load owned by this call, so
	// the in-place pin cleanup is safe.
	pins, pinsChanged := removePinnedUnderDir(proj.ResearchPins, pinDir)
	if pinsChanged {
		proj.ResearchPins = pins
		if err := f.projStore.SaveProject(context.Background(), *proj); err != nil {
			return nil, fmt.Errorf("failed to persist cleaned research pins: %w", err)
		}
	}

	root := f.parseResearchRootBestEffort(researchRoot)
	status := &ResearchStatusDTO{
		Enabled:      true,
		ProjectID:    projectID,
		ResearchRoot: researchRoot,
		Root:         root,
	}
	applyResearchPins(status, proj.ResearchPins)

	f.emitEvent(EventResearchChanged, map[string]string{
		"project_id": projectID,
		"action":     "project_deleted",
	})

	return status, nil
}

// SetResearchPinned pins (or unpins) the research project (R-NNN) named by
// researchID for a project: pinning records the project's brief document path
// (relative to the research root, forward slashes) in
// ProjectInfo.ResearchPins.Research; unpinning removes it. rid must resolve
// under the REQUESTING project's root — a foreign or unknown R-NNN is rejected
// before anything is touched. Both directions are idempotent. No event is
// emitted: the caller's resolved promise is its refresh signal.
func (f *FrontendAPI) SetResearchPinned(projectID, researchID string, pinned bool) error {
	if f.projStore == nil {
		return errors.New("project subsystem not initialized")
	}
	researchRoot, proj, err := f.researchRootForMutation(projectID)
	if err != nil {
		return err
	}
	rid := research.NormalizeResearchID(researchID)
	if rid == "" {
		return errors.New("invalid research project id (want R-NNN)")
	}

	// Serialize pin updates against the root mutations that also rewrite
	// pins: DeleteResearch cleans this R-NNN's pins under the same lock, and
	// without it a concurrent pin toggle would save a stale pins snapshot and
	// resurrect the deleted project's pins (lost update).
	mu := f.researchMutationMu(researchRoot)
	mu.Lock()
	defer mu.Unlock()

	projectDir, err := research.ProjectDir(researchRoot, rid)
	if err != nil {
		return err
	}
	briefPath := researchDocRelPath(researchRoot, projectDir, "brief.md")

	next, changed := togglePinnedPath(proj.ResearchPins.Research, briefPath, pinned)
	if !changed {
		return nil // already in the requested state
	}
	proj.ResearchPins.Research = next
	if err := f.projStore.SaveProject(context.Background(), *proj); err != nil {
		return fmt.Errorf("failed to persist research pins: %w", err)
	}
	return nil
}

// SetHypothesisPinned pins (or unpins) the hypothesis card (H-NNN) named by
// hypothesisID within the research project (R-NNN) named by researchID:
// pinning records the card's path (relative to the research root, forward
// slashes) under ProjectInfo.ResearchPins.Hypotheses[hypothesisID]; unpinning
// removes it and drops the key when its list becomes empty. The list-per-key
// shape supports the same H-NNN pinned across several R-NNN projects. rid must
// resolve under the REQUESTING project's root. Pinning additionally requires
// the card file to exist (no pins to phantom cards); unpinning deliberately
// works for a card whose file has since been deleted, so stale pins are
// always removable. Both directions are idempotent. No event is emitted.
func (f *FrontendAPI) SetHypothesisPinned(projectID, researchID, hypothesisID string, pinned bool) error {
	if f.projStore == nil {
		return errors.New("project subsystem not initialized")
	}
	researchRoot, proj, err := f.researchRootForMutation(projectID)
	if err != nil {
		return err
	}
	hid := research.NormalizeID(hypothesisID)
	if hid == "" {
		return errors.New("invalid hypothesis id")
	}
	rid := research.NormalizeResearchID(researchID)
	if rid == "" {
		return errors.New("invalid research project id (want R-NNN)")
	}

	mu := f.researchMutationMu(researchRoot)
	mu.Lock()
	defer mu.Unlock()

	projectDir, err := research.ProjectDir(researchRoot, rid)
	if err != nil {
		return err
	}
	cardPath := researchDocRelPath(researchRoot, projectDir, path.Join("hypotheses", hid+".md"))

	if pinned {
		// Fail closed against pinning a card that does not exist — the pin
		// would render as a permanently broken entry.
		if _, err := os.Stat(filepath.Join(projectDir, "hypotheses", hid+".md")); err != nil {
			return fmt.Errorf("hypothesis card %s not found in %s: %w", hid, rid, err)
		}
	}

	next, changed := togglePinnedPath(proj.ResearchPins.Hypotheses[hid], cardPath, pinned)
	if !changed {
		return nil // already in the requested state
	}
	if len(next) == 0 {
		delete(proj.ResearchPins.Hypotheses, hid)
	} else {
		if proj.ResearchPins.Hypotheses == nil {
			proj.ResearchPins.Hypotheses = make(map[string][]string)
		}
		proj.ResearchPins.Hypotheses[hid] = next
	}
	if err := f.projStore.SaveProject(context.Background(), *proj); err != nil {
		return fmt.Errorf("failed to persist research pins: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// pin path helpers (pure)
// ---------------------------------------------------------------------------

// researchDocRelPath renders the pin-path form of a research document inside a
// project directory: the directory's path relative to the research root
// (forward slashes) joined with name. Pins store root-relative forward-slash
// paths — the same style as index.md brief links — so they are stable across
// platforms. It falls back to the directory's base name when the relative
// path cannot be computed.
func researchDocRelPath(researchRoot, projectDir, name string) string {
	rel, err := filepath.Rel(researchRoot, projectDir)
	if err != nil {
		rel = filepath.Base(projectDir)
	}
	return path.Join(filepath.ToSlash(rel), name)
}

// togglePinnedPath adds p to list when pinned (idempotently — an already
// present pin is a no-op) or removes every occurrence when unpinned. It
// returns the next list and whether it changed.
func togglePinnedPath(list []string, p string, pinned bool) ([]string, bool) {
	if pinned {
		for _, existing := range list {
			if existing == p {
				return list, false
			}
		}
		return append(list, p), true
	}
	kept := make([]string, 0, len(list))
	removed := false
	for _, existing := range list {
		if existing == p {
			removed = true
			continue
		}
		kept = append(kept, existing)
	}
	if !removed {
		return list, false
	}
	return kept, true
}

// removePinnedUnderDir drops every pinned path under dir — a research project
// directory relative to the research root — from pins: entries in the pinned
// research list and card paths in every hypothesis entry, dropping hypothesis
// keys whose list becomes empty. It returns the cleaned pins and whether
// anything changed. The Hypotheses map is mutated in place; callers must own
// the passed pins.
func removePinnedUnderDir(pins project.ResearchPins, dir string) (project.ResearchPins, bool) {
	prefix := dir + "/"
	changed := false

	keptResearch := make([]string, 0, len(pins.Research))
	for _, p := range pins.Research {
		if strings.HasPrefix(p, prefix) {
			changed = true
			continue
		}
		keptResearch = append(keptResearch, p)
	}
	pins.Research = keptResearch

	for hid, cards := range pins.Hypotheses {
		keptCards := make([]string, 0, len(cards))
		for _, c := range cards {
			if strings.HasPrefix(c, prefix) {
				changed = true
				continue
			}
			keptCards = append(keptCards, c)
		}
		if len(keptCards) == 0 {
			delete(pins.Hypotheses, hid)
		} else {
			pins.Hypotheses[hid] = keptCards
		}
	}
	return pins, changed
}

// researchMutationMu returns the mutex serializing hypothesis mutations for a
// research root, creating it on first use (see the researchRootsMu /
// researchRootMus field docs in frontend_api.go for the rationale).
func (f *FrontendAPI) researchMutationMu(researchRoot string) *sync.Mutex {
	f.researchRootsMu.Lock()
	defer f.researchRootsMu.Unlock()
	if f.researchRootMus == nil {
		f.researchRootMus = make(map[string]*sync.Mutex)
	}
	mu := f.researchRootMus[researchRoot]
	if mu == nil {
		mu = &sync.Mutex{}
		f.researchRootMus[researchRoot] = mu
	}
	return mu
}

// researchRootForMutation loads the project, verifies RESEARCH is enabled, and
// returns the project's research root (with
// workspace containment enforced — SECURITY.md; defense in depth even though
// the root was already validated at enable time) together with the loaded
// project record, whose ResearchPins feed the RPC responses. Callers that
// persist the project back must additionally check projStore themselves (this
// helper only requires the read path).
func (f *FrontendAPI) researchRootForMutation(projectID string) (string, *project.ProjectInfo, error) {
	if projectID == "" {
		return "", nil, errors.New("project_id is required")
	}
	if f.projectManager == nil {
		return "", nil, errors.New("project subsystem not initialized")
	}

	proj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return "", nil, err
	}
	if proj.ResearchRoot == "" {
		return "", nil, errors.New("RESEARCH mode is not enabled for this project")
	}

	contained, withinErr := config.IsWithinPath(proj.WorkspacePath, proj.ResearchRoot)
	if withinErr != nil {
		return "", nil, fmt.Errorf("failed to validate research root containment: %w", withinErr)
	}
	if !contained {
		return "", nil, fmt.Errorf("research root %q must be inside the project workspace %q", proj.ResearchRoot, proj.WorkspacePath)
	}
	return proj.ResearchRoot, proj, nil
}

// researchGraphAfterMutation re-parses the research root after a mutation and
// returns a graph DTO. When rid names a parsed project, that project's graph
// is returned — a save targeting a non-active R-NNN must not flip the panel
// to the active project's graph. An empty rid (or one that no longer resolves)
// falls back to the active project, matching CreateHypothesis's active-target
// semantics. An empty DTO is returned when the root is not yet parseable —
// which should not happen right after a successful write.
func (f *FrontendAPI) researchGraphAfterMutation(researchRoot, rid string) *ResearchGraphDTO {
	root := f.parseResearchRootBestEffort(researchRoot)
	if root == nil {
		return &ResearchGraphDTO{}
	}
	for _, p := range root.Projects {
		if p.ID == rid {
			return researchGraphDTOFromProject(p)
		}
	}
	active := research.PickActiveProject(root) // handles nil root → nil
	if active == nil {
		return &ResearchGraphDTO{}
	}
	return researchGraphDTOFromProject(active)
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
		Modified:  r.Modified,
	}
}

// applyResearchPins fills a ResearchStatusDTO's pin fields from the project's
// persisted pins. Collections are copied and normalized to non-nil so the wire
// shape is a stable empty slice/map instead of null (the frontend's boundary
// guard accepts both, but a stable shape keeps consumers simple).
func applyResearchPins(dto *ResearchStatusDTO, pins project.ResearchPins) {
	pinned := make([]string, len(pins.Research))
	copy(pinned, pins.Research)
	hypotheses := make(map[string][]string, len(pins.Hypotheses))
	for hid, cards := range pins.Hypotheses {
		paths := make([]string, len(cards))
		copy(paths, cards)
		hypotheses[hid] = paths
	}
	dto.PinnedResearch = pinned
	dto.PinnedHypotheses = hypotheses
}
