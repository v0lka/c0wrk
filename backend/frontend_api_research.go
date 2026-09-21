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
	"github.com/v0lka/c0wrk/core/papers"
	"github.com/v0lka/c0wrk/core/research"
)

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

// ResearchStatusDTO is the structured response for GetResearchStatus. It is the
// full Research-panel view model: the mode state plus the parsed research root
// (graph + metrics).
//
// RESEARCH is always on for real projects, so for them Enabled is true and
// ResearchRoot points at <workspace>/.research. For the No Project
// pseudo-project (no workspace) it is the neutral "empty state": Enabled=false
// with ResearchRoot and Root empty/nil.
type ResearchStatusDTO struct {
	// Enabled reports whether RESEARCH is available for the project (true for
	// a real, non-No-Project project).
	Enabled bool `json:"enabled"`

	// ProjectID is the project these results pertain to.
	ProjectID string `json:"project_id"`

	// ResearchRoot is the absolute path to the research workspace directory
	// (config.ProjectResearchPath); "" for the No Project pseudo-project.
	ResearchRoot string `json:"research_root"`

	// Root is the parsed research root (index, projects, each project's graph
	// + metrics + brief + prior-art). It is nil for the No Project
	// pseudo-project or when the directory could not be parsed (treated as an
	// empty state).
	Root *research.ResearchRoot `json:"root,omitempty"`

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
// GetResearchStatus
// ---------------------------------------------------------------------------

// GetResearchStatus returns the live RESEARCH mode state for a project.
// RESEARCH is always on for real projects: the research root is the project's
// canonical <workspace>/.research, and the returned DTO carries the parsed
// root (graph + metrics + project list) with Enabled=true. The No Project
// pseudo-project has no workspace, so RESEARCH is unavailable there — it
// returns a neutral empty state (Enabled=false, nil Root) rather than an
// error.
func (f *FrontendAPI) GetResearchStatus(projectID string) (*ResearchStatusDTO, error) {
	if projectID == "" {
		return nil, errors.New("project_id is required")
	}
	if f.projectManager == nil {
		return nil, errors.New("project subsystem not initialized")
	}

	proj, err := f.projectManager.GetProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to load project %q: %w", projectID, err)
	}
	if proj == nil {
		return nil, fmt.Errorf("project %q not found", projectID)
	}

	// No Project has no workspace: RESEARCH is unavailable there, so return a
	// neutral empty state (no error) for the panel's empty view.
	if proj.IsNoProject {
		status := &ResearchStatusDTO{
			Enabled:   false,
			ProjectID: projectID,
		}
		applyResearchPins(status, proj.ResearchPins)
		return status, nil
	}

	researchRoot := config.ProjectResearchPath(proj.WorkspacePath)
	root := f.parseResearchRootBestEffort(researchRoot)
	status := &ResearchStatusDTO{
		Enabled:      true,
		ProjectID:    projectID,
		ResearchRoot: researchRoot,
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

	// RESEARCH is always on for real projects: the root is <workspace>/.research.
	researchRoot := config.ProjectResearchPath(proj.WorkspacePath)

	// Parse the full root to get the active project.
	root := f.parseResearchRootBestEffort(researchRoot)
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
// yet (an empty research root, or a root with no projects), it returns the
// setup recommendation (research-init) rather than an error, so the dashboard
// always has a next step to show.
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

	// RESEARCH is always on for real projects: the root is <workspace>/.research.
	// A missing/unparseable root (no research artifacts yet) yields the setup
	// recommendation.
	root := f.parseResearchRootBestEffort(config.ProjectResearchPath(proj.WorkspacePath))
	if root == nil {
		return f.setupNextStep(projectID), nil
	}

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
// that has no active R-NNN yet (an empty or unparseable research root).
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
	if effectiveResearchRoot(proj) != researchRoot {
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
	if effectiveResearchRoot(proj) != researchRoot {
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
	// while this RPC waited on the mutex: a concurrent pin toggle on a
	// different card (losing its pin), or another research-row writer that
	// moved the effective root (guarded below). All writers of the projects
	// row serialize on this same per-root mutex.
	proj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return err
	}
	if effectiveResearchRoot(proj) != researchRoot {
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
	if effectiveResearchRoot(proj) != researchRoot {
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
// pin/delete RPCs and the paper writers) when the project's effective research
// root changed between the call's initial load and its mutex-guarded re-load:
// the pin paths / the observed root were resolved against the old root, so
// persisting them would misattribute work to the new one. No row write happens
// in that case; a retry against the fresh state resolves.
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

// researchRootForMutation loads the project and returns its research root,
// derived from the workspace path (<workspace>/.research) with workspace
// containment enforced (SECURITY.md; defense in depth even though the root is
// deterministic). The returned project record predates the caller's mutex
// acquisition, so callers that persist the project back must re-load the row
// under the per-root mutation mutex and merge only their field delta (see
// SetResearchPinned); this helper only requires the read path.
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

	researchRoot := config.ProjectResearchPath(proj.WorkspacePath)
	contained, withinErr := config.IsWithinPath(proj.WorkspacePath, researchRoot)
	if withinErr != nil {
		return "", nil, fmt.Errorf("failed to validate research root containment: %w", withinErr)
	}
	if !contained {
		return "", nil, fmt.Errorf("research root %q must be inside the project workspace %q", researchRoot, proj.WorkspacePath)
	}
	return researchRoot, proj, nil
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

// seedGlobalPacks seeds every c0wrk-owned pack into the GLOBAL agent
// directories (~/.c0wrk/.agents/{skills,agents}) once per launch:
// research.SeedSkills (the seven research-* methodology skills) and
// papers.SeedSkills (the study-paper skill) into config.SkillsDir(agentDir),
// and research.SeedAgents (the research Subagent Profile) into
// config.AgentsDir(agentDir). It is called from NewFrontendAPI BEFORE the
// skill/agent directory watchers are created, so the freshly created
// directories are themselves watched on the same launch.
//
// The seeding is idempotent and non-destructive: missing pack entries are
// written, pack-marked outdated entries are upgraded to the current pack
// version, and user-owned (marker-less, diverging) directories are preserved
// untouched (see the classification contract in core/research/skillpack.go and
// core/papers/skillpack.go). Failures are per-pack and logged, never fatal.
//
// researchSeedMu serializes the run against any other pack writer; the skill
// and agent caches are invalidated afterwards so the next ListSkills /
// ListAgents call reflects the seeded entries.
func (f *FrontendAPI) seedGlobalPacks() {
	if f.agentDir == "" {
		return
	}

	f.researchSeedMu.Lock()
	defer f.researchSeedMu.Unlock()

	skillsDir := config.SkillsDir(f.agentDir)
	if res, err := research.SeedSkills(skillsDir, f.log()); err != nil {
		f.log().Warn("global research skill-pack seeding failed",
			"skills_dir", skillsDir, "error", err)
	} else {
		f.log().Info("global research skill-pack seeded",
			"skills_dir", skillsDir,
			"seeded", len(res.Seeded), "updated", len(res.Updated),
			"current", len(res.Current), "preserved", len(res.Preserved))
	}

	if res, err := papers.SeedSkills(skillsDir, f.log()); err != nil {
		f.log().Warn("global paper skill-pack seeding failed",
			"skills_dir", skillsDir, "error", err)
	} else {
		f.log().Info("global paper skill-pack seeded",
			"skills_dir", skillsDir,
			"seeded", len(res.Seeded), "updated", len(res.Updated),
			"current", len(res.Current), "preserved", len(res.Preserved))
	}

	agentsDir := config.AgentsDir(f.agentDir)
	if res, err := research.SeedAgents(agentsDir, f.log()); err != nil {
		f.log().Warn("global research agent-pack seeding failed",
			"agents_dir", agentsDir, "error", err)
	} else {
		f.log().Info("global research agent-pack seeded",
			"agents_dir", agentsDir,
			"seeded", len(res.Seeded), "updated", len(res.Updated),
			"current", len(res.Current), "preserved", len(res.Preserved))
	}

	// The seeded directories are new c0wrk-owned content; drop any cached
	// skill/agent listing so the next read rescans them.
	f.invalidateSkillCache()
	f.invalidateAgentCache()
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
