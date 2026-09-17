package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/papers"
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
		// The paper library follows the effective research root, so track it
		// here too. When RESEARCH is enabled with a custom root the library
		// moves to <researchRoot>/papers; the WatchTree(researchRoot) below
		// covers its subtree.
		f.activePapersRoot = config.PaperLibraryPathIn(researchRoot)
		// The comparisons directory follows the same root and is likewise
		// covered by the WatchTree(researchRoot) below.
		f.activeComparisonsRoot = config.ComparisonsPathIn(researchRoot)
	}
	f.activeProjectMu.Unlock()

	// Recursively watch the research artifact tree so the file watcher
	// detects edits to hypothesis cards / brief / graph in nested
	// subdirectories (.research/R-NNN/hypotheses/…). The watcher was created
	// at project-switch time (which registered the EFFECTIVE research root as a
	// recursive root — the default <workspace>/.research when RESEARCH was off)
	// so it did not watch the newly-activated tree; add it now. Unwatch the
	// previous effective root first when it differs: fsnotify watches persist
	// until Remove/Close, so a stale watch on the now-inactive default-root tree
	// would leak for the app's lifetime and fire spurious
	// workspace:tree_changed events. Best-effort: a nil watcher (e.g. early in
	// startup) is skipped — switchProjectSetupWatcher picks it up on the next
	// project switch.
	prevResearchRoot := effectiveResearchRoot(proj)
	f.watcherMu.Lock()
	if f.watcher != nil {
		if prevResearchRoot != "" && prevResearchRoot != researchRoot {
			if uerr := f.watcher.UnwatchTree(prevResearchRoot); uerr != nil {
				f.log().Debug("failed to unwatch previous research tree on enable",
					"root", prevResearchRoot, "error", uerr)
			}
		}
		if werr := f.watcher.WatchTree(researchRoot); werr != nil {
			f.log().Debug("failed to watch research tree on enable",
				"root", researchRoot, "error", werr)
		}
	}
	f.watcherMu.Unlock()

	// Reconcile the project-local c0wrk packs: the seven research-* skills,
	// the study-paper skill, and the research Subagent Profile — seeding ALL
	// missing entries, upgrading pack-marked outdated ones, preserving
	// user-owned directories (see reconcileResearchPacks). The project-local
	// study-paper copy is what makes the papers workflow robust: per-session
	// skill discovery resolves same-named skills first-wins with
	// <workspace>/.agents/skills at the highest priority, so the seeded copy
	// beats a same-named ~/.agents skill that knows nothing about c0wrk's
	// paper-library conventions.
	packRes := f.reconcileResearchPacks(projectID, proj.WorkspacePath)

	// Persist the research root on the project. All writers of the projects
	// row serialize on the per-EFFECTIVE-research-root research mutation mutex
	// — the same key the paper writers (SetPaperPinned / RecordFlashcardReview /
	// RunPaperLiterature) derive from papersReadContextFor, which is the
	// persisted root when RESEARCH is on and the default <workspace>/.research
	// when it is off. Keying on the raw ResearchRoot instead would, while
	// RESEARCH is off, take the "" mutex and let a concurrent pin save land on a
	// stale snapshot (the projects-row lost update). Keying on the NEW root
	// would likewise take a different mutex during an explicit-root re-enable.
	// Inside the lock this save re-loads the row, verifies the root did not
	// change meanwhile (the sentinel below), and merges only ResearchRoot — a
	// full-row save of the snapshot taken before seeding would clobber a pin
	// toggle committed meanwhile.
	persistMu := f.researchMutationMu(effectiveResearchRoot(proj))
	persistMu.Lock()
	persistProj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		persistMu.Unlock()
		return nil, err
	}
	if persistProj.ResearchRoot != proj.ResearchRoot {
		persistMu.Unlock()
		return nil, errResearchRootChanged
	}
	persistProj.ResearchRoot = researchRoot
	if err := f.projStore.SaveProject(context.Background(), *persistProj); err != nil {
		persistMu.Unlock()
		return nil, fmt.Errorf("failed to persist research root: %w", err)
	}
	persistMu.Unlock()

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
		SeedResult:   toSeedResultDTO(packRes),
	}
	applyResearchPins(status, persistProj.ResearchPins)

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

	// Clear the toggle. Take the same per-EFFECTIVE-research-root mutation
	// mutex the pin RPCs, EnableResearch, and the paper writers serialize on
	// (effectiveResearchRoot, so every writer of this row agrees on one key),
	// re-load the row inside the lock, and merge only this clear: a pin
	// committed while we waited on the mutex survives the save instead of being
	// overwritten by a stale full-row snapshot (the projects-row lost update).
	mu := f.researchMutationMu(effectiveResearchRoot(proj))
	mu.Lock()
	fresh, err := f.loadProjectForResearch(projectID)
	if err != nil {
		mu.Unlock()
		return err
	}
	// Sentinel guard (mirrors the pin/delete RPCs): the root observed on the
	// initial load changed while we waited for the mutex, so clearing it here
	// would clear a root this call never observed. Nothing was written; a
	// retry against the fresh state resolves.
	if fresh.ResearchRoot != proj.ResearchRoot {
		mu.Unlock()
		return errResearchRootChanged
	}
	fresh.ResearchRoot = ""
	if err := f.projStore.SaveProject(context.Background(), *fresh); err != nil {
		mu.Unlock()
		return fmt.Errorf("failed to clear research root: %w", err)
	}
	mu.Unlock()

	// Clear the tracked research root so the watcher stops emitting
	// research:file_changed events. Only activeResearchRoot is cleared — the
	// active project ID/path must be preserved so git, workspace, and session
	// operations continue to target the correct project after toggling
	// RESEARCH off. The paper-library / comparisons roots are RETRACKED (not
	// cleared): both follow the effective research root, which falls back to
	// the default once the toggle is cleared, and must keep being watched so a
	// paper edit still emits papers:changed with RESEARCH off.
	f.activeProjectMu.Lock()
	researchRootToUnwatch := ""
	researchRootToWatch := ""
	if f.activeProjectID == projectID {
		researchRootToUnwatch = f.activeResearchRoot
		f.activeResearchRoot = ""
		f.activePapersRoot = papersRootForProject(fresh)
		f.activeComparisonsRoot = comparisonsRootForProject(fresh)
		researchRootToWatch = effectiveResearchRoot(fresh)
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

	// The paper library and the comparisons directory both live INSIDE the
	// research tree, so the UnwatchTree above removed their watches too.
	// Re-watch the effective (now default) research root as a recursive root so
	// both subtrees stay watched independently of the toggle — and do so
	// WITHOUT creating any directory: the recursive root auto-adds papers/ and
	// comparisons/ when they are first written, so the toggle-off path no
	// longer materializes them in the user's repository. Best-effort: a nil
	// watcher (early startup) is skipped and switchProjectSetupWatcher
	// re-establishes it on the next project switch.
	if researchRootToWatch != "" {
		f.watcherMu.Lock()
		if f.watcher != nil {
			if wErr := f.watcher.WatchTree(researchRootToWatch); wErr != nil {
				f.log().Debug("failed to re-watch research tree on disable",
					"root", researchRootToWatch, "error", wErr)
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
	researchRoot, _, err := f.researchRootForMutation(projectID)
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

	// Re-load the row under the lock: the researchRootForMutation snapshot
	// predates it, so the pins in the returned status would miss a pin toggle
	// committed while we waited (SetResearchPinned emits no event, so that
	// staleness would persist until a later refresh). Guard against a
	// research-root change, exactly like DeleteResearch.
	proj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return nil, err
	}
	if proj.ResearchRoot != researchRoot {
		return nil, errResearchRootChanged
	}

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
	researchRoot, _, err := f.researchRootForMutation(projectID)
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

	// Re-load the row under the lock and persist only the pins delta: the
	// load in researchRootForMutation ran before the mutex, so saving that
	// snapshot could clobber row state committed while we waited (a pin
	// toggle, or a research-root change — guarded below). See
	// SetResearchPinned for the full lost-update rationale.
	proj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return nil, err
	}
	if proj.ResearchRoot != researchRoot {
		return nil, errResearchRootChanged
	}

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
	researchRoot, _, err := f.researchRootForMutation(projectID)
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

	// Re-load the row under the lock and persist only the pins delta. The
	// load in researchRootForMutation ran before the mutex was acquired, so
	// a full-row save of that snapshot would clobber whatever was committed
	// while this RPC waited on the mutex: a DisableResearch clear of
	// ResearchRoot (resurrecting the toggle), an EnableResearch root change
	// (guarded below), or another pin toggle on a different card (losing
	// its pin). All writers of the projects row — the pin RPCs, Enable and
	// Disable — serialize on this same per-root mutex.
	proj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return err
	}
	if proj.ResearchRoot != researchRoot {
		return errResearchRootChanged
	}

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
	researchRoot, _, err := f.researchRootForMutation(projectID)
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

	// Re-load the row under the lock and persist only the pins delta — the
	// pre-mutex snapshot must never be saved as a full row (see
	// SetResearchPinned for the lost-update rationale).
	proj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return err
	}
	if proj.ResearchRoot != researchRoot {
		return errResearchRootChanged
	}

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

// errResearchRootChanged is returned by the research-root writers (the
// pin/delete RPCs, EnableResearch, and DisableResearch) when the project's
// persisted research root changed between the call's initial load and its
// mutex-guarded re-load: the pin paths / the observed root were resolved
// against the old root, so persisting them would misattribute work to the new
// one. No row write happens in that case; a retry against the fresh state
// resolves. (EnableResearch may already have performed best-effort skill/agent
// seeding before it detects the change — those side effects are idempotent and
// non-destructive.)
var errResearchRootChanged = errors.New("research root changed concurrently")

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
// the root was already validated at enable time). The returned project record
// predates the caller's mutex acquisition, so callers that persist the project
// back must re-load the row under the per-root mutation mutex and merge only
// their field delta (see SetResearchPinned); this helper only requires the
// read path.
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

// researchPackReconcile collects the per-pack outcomes of one
// reconcileResearchPacks run. A nil member means that pack's seeding failed
// (already logged); it contributes no names to the merged DTO.
type researchPackReconcile struct {
	skills *research.SeedSkillsResult
	papers *papers.SeedResult
	agents *research.SeedAgentsResult
}

// changed reports whether the reconciliation wrote anything (new seeds or
// version upgrades). Callers use it to skip cache invalidation and session
// rescans when nothing moved.
func (r *researchPackReconcile) changed() bool {
	if r == nil {
		return false
	}
	skills := r.skills != nil && (len(r.skills.Seeded) > 0 || len(r.skills.Updated) > 0)
	paperPack := r.papers != nil && (len(r.papers.Seeded) > 0 || len(r.papers.Updated) > 0)
	agents := r.agents != nil && (len(r.agents.Seeded) > 0 || len(r.agents.Updated) > 0)
	return skills || paperPack || agents
}

// reconcileResearchPacks seeds and updates every c0wrk-owned pack into the
// project-local agent directories: research.SeedSkills (the seven research-*
// methodology skills), papers.SeedSkills (the study-paper skill), and
// research.SeedAgents (the research Subagent Profile). It is the single
// reconciliation shared by EnableResearch and the SwitchProject revalidation
// for research-enabled projects — seeding ALL missing pack entries and
// upgrading pack-marked outdated ones, while user-owned (marker-less,
// diverging) directories are preserved untouched (see the classification
// contract in core/research/skillpack.go and core/papers/skillpack.go).
//
// researchSeedMu serializes the whole run: EnableResearch and a concurrent
// SwitchProject revalidation can target the same directories, and the pack
// staging swap is not designed for two concurrent writers of one destination.
//
// Failures are per-pack, logged, and never returned as errors — a failed pack
// yields a nil result member and the research toggle/switch stays unaffected.
func (f *FrontendAPI) reconcileResearchPacks(projectID, workspacePath string) *researchPackReconcile {
	f.researchSeedMu.Lock()
	defer f.researchSeedMu.Unlock()

	res := &researchPackReconcile{}

	skillsDir := config.ProjectSkillsPath(workspacePath)
	skillsRes, skillsErr := research.SeedSkills(skillsDir, f.log())
	if skillsErr != nil {
		f.log().Warn("research pack reconciliation: skill seeding failed",
			"project_id", projectID, "skills_dir", skillsDir, "error", skillsErr)
	} else {
		res.skills = skillsRes
		f.log().Info("research skill-pack reconciled",
			"project_id", projectID,
			"seeded", len(skillsRes.Seeded), "updated", len(skillsRes.Updated),
			"current", len(skillsRes.Current), "preserved", len(skillsRes.Preserved),
			"modified", len(skillsRes.Modified))
	}

	papersRes, papersErr := papers.SeedSkills(skillsDir, f.log())
	if papersErr != nil {
		f.log().Warn("research pack reconciliation: paper skill seeding failed",
			"project_id", projectID, "skills_dir", skillsDir, "error", papersErr)
	} else {
		res.papers = papersRes
		f.log().Info("paper skill-pack reconciled",
			"project_id", projectID,
			"seeded", len(papersRes.Seeded), "updated", len(papersRes.Updated),
			"current", len(papersRes.Current), "preserved", len(papersRes.Preserved),
			"modified", len(papersRes.Modified))
	}

	agentsDir := config.ProjectAgentsPath(workspacePath)
	agentsRes, agentsErr := research.SeedAgents(agentsDir, f.log())
	if agentsErr != nil {
		f.log().Warn("research pack reconciliation: agent seeding failed",
			"project_id", projectID, "agents_dir", agentsDir, "error", agentsErr)
	} else {
		res.agents = agentsRes
		f.log().Info("research agent-pack reconciled",
			"project_id", projectID,
			"seeded", len(agentsRes.Seeded), "updated", len(agentsRes.Updated),
			"current", len(agentsRes.Current), "preserved", len(agentsRes.Preserved),
			"modified", len(agentsRes.Modified))
	}

	return res
}

// toSeedResultDTO merges the skill-pack outcomes of a reconciliation into the
// frontend-facing DTO (research-* + study-paper names in shared buckets),
// returning nil when every skill pack failed (so the JSON field is omitted).
// Skill names are unique across the two packs, so concatenation is
// unambiguous; buckets end sorted (each pack sorts its own, the merge
// re-sorts the concatenation). Agent-pack outcomes are log-only, mirroring
// the pre-merge DTO shape.
func toSeedResultDTO(r *researchPackReconcile) *ResearchSeedResultDTO {
	if r == nil || (r.skills == nil && r.papers == nil) {
		return nil
	}
	dto := &ResearchSeedResultDTO{
		Seeded:    []string{},
		Updated:   []string{},
		Current:   []string{},
		Preserved: []string{},
		Modified:  []string{},
	}
	if r.skills != nil {
		dto.Seeded = append(dto.Seeded, r.skills.Seeded...)
		dto.Updated = append(dto.Updated, r.skills.Updated...)
		dto.Current = append(dto.Current, r.skills.Current...)
		dto.Preserved = append(dto.Preserved, r.skills.Preserved...)
		dto.Modified = append(dto.Modified, r.skills.Modified...)
	}
	if r.papers != nil {
		dto.Seeded = append(dto.Seeded, r.papers.Seeded...)
		dto.Updated = append(dto.Updated, r.papers.Updated...)
		dto.Current = append(dto.Current, r.papers.Current...)
		dto.Preserved = append(dto.Preserved, r.papers.Preserved...)
		dto.Modified = append(dto.Modified, r.papers.Modified...)
	}
	for _, bucket := range []*[]string{&dto.Seeded, &dto.Updated, &dto.Current, &dto.Preserved, &dto.Modified} {
		sort.Strings(*bucket)
	}
	return dto
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
