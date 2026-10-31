package backend

import (
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

// ResearchStatusDTO is the full Research-panel view model: the mode state
// plus the parsed research root. Always-on for real projects (Enabled=true,
// ResearchRoot=<workspace>/.research); No Project gets the neutral empty
// state. See specs/domains/research.md.
type ResearchStatusDTO struct {
	// Enabled reports whether RESEARCH is available (true for a real project).
	Enabled bool `json:"enabled"`

	// ProjectID is the project these results pertain to.
	ProjectID string `json:"project_id"`

	// ResearchRoot is the absolute research workspace directory
	// (config.ProjectResearchPath); "" for No Project.
	ResearchRoot string `json:"research_root"`

	// Root is the parsed research root; nil for No Project or an
	// unparseable directory (treated as an empty state).
	Root *research.ResearchRoot `json:"root,omitempty"`

	// PinnedResearch lists the project's pinned brief paths, research-root-
	// relative with forward slashes. Always non-nil (stable wire shape).
	PinnedResearch []string `json:"pinned_research"`

	// PinnedHypotheses maps a hypothesis id (H-NNN) to its pinned card paths
	// (a list — the same H-NNN exists across R-NNN projects). Always non-nil.
	PinnedHypotheses map[string][]string `json:"pinned_hypotheses"`
}

// ResearchGraphDTO is the lightweight response for GetResearchGraph: only the
// hypothesis graph, metrics, report flag, and log — serving the incremental
// file-change update path. See specs/domains/research.md.
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

// ResearchNextStepDTO is the small response for GetResearchNextStep: the
// single recommended next research action for the active phase; Target is
// empty when the action is not scoped to one hypothesis.
//
// ProjectID is DUAL-NAMESPACE BY DESIGN (specs/domains/research.md): it names
// the SUBJECT of the recommendation (the active R-NNN when one exists, the
// c0wrk project UUID otherwise), NOT a stable identity for the requesting
// project — frontend consumers must not key cross-project state off it.
type ResearchNextStepDTO struct {
	ProjectID string `json:"project_id"`
	Action    string `json:"action"`
	Target    string `json:"target,omitempty"`
	Reason    string `json:"reason"`
	Skill     string `json:"skill"`
}

// HypothesisUpdateFields is the structured update payload for
// UpdateHypothesis. Pointer fields distinguish "leave unchanged" (nil) from
// "set to empty" (non-nil to "" / empty Parents); identifier, created, and
// completed are not editable here. Parents replacement is validated
// server-side (existence, no self-reference, no cycle) before any write.
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

// GetResearchStatus returns the live RESEARCH state for a project — always on
// for real projects (the parsed root with Enabled=true, the root
// effectiveResearchRoot resolves following the active session); No Project
// returns the neutral empty state rather than an error.
// See specs/domains/research.md.
func (f *FrontendAPI) GetResearchStatus(projectID string) (*ResearchStatusDTO, error) {
	f.seedAcquire()
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

	if proj.IsNoProject {
		status := &ResearchStatusDTO{
			Enabled:   false,
			ProjectID: projectID,
		}
		applyResearchPins(status, proj.ResearchPins)
		return status, nil
	}

	researchRoot := f.effectiveResearchRoot(proj)
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

// GetResearchGraph returns only the hypothesis graph and computed metrics —
// a smaller wire payload than GetResearchStatus for the incremental
// file-change path; the parse cost is identical (both parse the full root).
// See specs/domains/research.md.
func (f *FrontendAPI) GetResearchGraph(projectID string) (*ResearchGraphDTO, error) {
	f.seedAcquire()
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

	// The root follows the active session (see effectiveResearchRoot).
	researchRoot := f.effectiveResearchRoot(proj)

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

// GetResearchNextStep returns the single recommended next research action,
// derived from the active R-NNN's current phase; a non-empty hypothesisID
// scopes it to THAT hypothesis via RecommendNextStepForHypothesis (an unknown
// id, or one already carrying a Decision, falls back to the project-level
// recommendation). With no active R-NNN yet it returns the setup
// recommendation rather than an error. See specs/domains/research.md.
func (f *FrontendAPI) GetResearchNextStep(projectID, hypothesisID string) (*ResearchNextStepDTO, error) {
	f.seedAcquire()
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

	// The root follows the active session (see effectiveResearchRoot);
	// a missing/unparseable root (no research artifacts yet) yields the setup
	// recommendation.
	root := f.parseResearchRootBestEffort(f.effectiveResearchRoot(proj))
	if root == nil {
		return f.setupNextStep(projectID), nil
	}

	active := research.PickActiveProject(root) // handles nil root → nil
	rec := research.RecommendNextStepForHypothesis(active, hypothesisID)

	// The active R-NNN is the recommendation's subject when one exists;
	// otherwise fall back to the requested project ID (dual namespace — see
	// ResearchNextStepDTO).
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
// graph entries for the R-NNN named by researchID, then returns the refreshed
// graph. researchID must resolve under the REQUESTING project's root — a
// foreign R-NNN is rejected before any file is touched, and the update targets
// the caller's expected project rather than blindly following the backend's
// active one (cross-project save race). Status transitions follow the
// methodology's state machine; a Parents update is validated against the
// reconciled graph and synchronized across the card row, the Mermaid edges,
// and the catalog column. Any invalid update leaves card and graph unchanged.
// See specs/domains/research.md.
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
	// mutations would otherwise interleave their read-modify-write of
	// card+graph and race the max+1 H-NNN id assignment. See
	// specs/domains/research.md.
	mu := f.researchMutationMu(researchRoot)
	mu.Lock()
	defer mu.Unlock()

	// Ownership check: a research project of another project's root does not
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

// CreateHypothesis creates a new hypothesis card (assigning the next H-NNN
// id) and updates the graph for the active R-NNN of a project, returning the
// refreshed graph. The whole resolve→allocate→write chain runs under the
// per-root mutation mutex so concurrent creators cannot both observe the same
// max H-NNN and overwrite each other's card. See specs/domains/research.md.
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

// SetActiveResearch makes the R-NNN named by researchID the active one by
// rewriting index.md so its row becomes the last table entry — the
// chronological rule PickActiveProject applies. The resolve→write chain runs
// under the per-root mutation mutex, the root is containment-checked, and rid
// must resolve under the REQUESTING project's root. Returns the refreshed
// status and emits research:changed (action="active_changed").
// See specs/domains/research.md.
func (f *FrontendAPI) SetActiveResearch(projectID, researchID string) (*ResearchStatusDTO, error) {
	f.seedAcquire()
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

	// Re-load the row under the lock: the researchRootForMutation snapshot
	// predates it, so the pins in the returned status would miss a pin toggle
	// committed while we waited (SetResearchPinned emits no event, so that
	// staleness would persist until a later refresh). Guard against a
	// research-root change, exactly like DeleteResearch.
	proj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return nil, err
	}
	if f.effectiveResearchRoot(proj) != researchRoot {
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

// DeleteResearch removes the R-NNN named by researchID from a project's
// research root — every index.md entry line, then its R-NNN-* directory tree
// in full. rid must resolve under the REQUESTING project's root (ownership
// check before any file is touched); the core-layer deletion is
// containment-checked. Pins referencing the deleted project are removed and
// the cleaned record persisted. Emits research:changed
// (action="project_deleted"). See specs/domains/research.md.
func (f *FrontendAPI) DeleteResearch(projectID, researchID string) (*ResearchStatusDTO, error) {
	f.seedAcquire()
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
	// snapshot could clobber row state committed while we waited. See
	// SetResearchPinned for the full lost-update rationale.
	proj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return nil, err
	}
	if f.effectiveResearchRoot(proj) != researchRoot {
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

	// Clean the deleted R-NNN's pins and persist the cleaned record. proj is
	// a fresh load owned by this call, so the in-place cleanup is safe.
	pins, pinsChanged := removePinnedUnderDir(proj.ResearchPins, pinDir)
	if pinsChanged {
		proj.ResearchPins = pins
		// Bounded by storeOpCtx: this write runs under the per-root research
		// mutation mutex — an unbounded pool wait would park every
		// pin/flashcard/paper mutation behind it.
		pctx, pcancel := f.storeOpCtx()
		if err := f.projStore.SaveProject(pctx, *proj); err != nil {
			pcancel()
			return nil, fmt.Errorf("failed to persist cleaned research pins: %w", err)
		}
		pcancel()
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

// SetResearchPinned pins (or unpins) the R-NNN named by researchID for a
// project, recording the brief's research-root-relative path in
// ProjectInfo.ResearchPins.Research. rid must resolve under the REQUESTING
// project's root; both directions idempotent; no event emitted (the caller's
// resolved promise is its refresh signal). See specs/domains/research.md.
func (f *FrontendAPI) SetResearchPinned(projectID, researchID string, pinned bool) error {
	f.seedAcquire()
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

	// Re-load the row under the lock and persist only the pins delta: the
	// pre-mutex snapshot must never be saved as a full row — a full-row save
	// would clobber whatever was committed while this RPC waited on the
	// mutex. All writers of the projects row serialize on this same per-root
	// mutex.
	proj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return err
	}
	if f.effectiveResearchRoot(proj) != researchRoot {
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
	// Bounded by storeOpCtx (per-root mutation mutex — see DeleteResearch).
	pctx, pcancel := f.storeOpCtx()
	if err := f.projStore.SaveProject(pctx, *proj); err != nil {
		pcancel()
		return fmt.Errorf("failed to persist research pins: %w", err)
	}
	pcancel()
	return nil
}

// SetHypothesisPinned pins (or unpins) the hypothesis card (H-NNN) named by
// hypothesisID within the R-NNN named by researchID, recording the card's
// research-root-relative path under ProjectInfo.ResearchPins.Hypotheses
// (a list per key — the same H-NNN exists across R-NNN projects). Pinning
// requires the card file to exist; unpinning deliberately works for a
// since-deleted card, so stale pins stay removable. Idempotent; no event.
// See specs/domains/research.md.
func (f *FrontendAPI) SetHypothesisPinned(projectID, researchID, hypothesisID string, pinned bool) error {
	f.seedAcquire()
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
	if f.effectiveResearchRoot(proj) != researchRoot {
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
	// Bounded by storeOpCtx (per-root mutation mutex — see DeleteResearch).
	pctx, pcancel := f.storeOpCtx()
	if err := f.projStore.SaveProject(pctx, *proj); err != nil {
		pcancel()
		return fmt.Errorf("failed to persist research pins: %w", err)
	}
	pcancel()
	return nil
}

// ---------------------------------------------------------------------------
// pin path helpers (pure)
// ---------------------------------------------------------------------------

// researchDocRelPath renders the pin-path form of a research document: the
// project directory's path relative to the research root (forward slashes)
// joined with name — the index.md brief-link style, stable across platforms.
// Falls back to the base name when the relative path cannot be computed.
func researchDocRelPath(researchRoot, projectDir, name string) string {
	rel, err := filepath.Rel(researchRoot, projectDir)
	if err != nil {
		rel = filepath.Base(projectDir)
	}
	return path.Join(filepath.ToSlash(rel), name)
}

// togglePinnedPath adds p to list when pinned (idempotent — an already
// present pin is a no-op) or removes every occurrence when unpinned.
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

// removePinnedUnderDir drops every pinned path under dir (a research project
// directory relative to the research root) from pins, dropping hypothesis
// keys whose list becomes empty. The Hypotheses map is mutated in place;
// callers must own the passed pins.
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

// errResearchRootChanged is returned by the research-root writers (pin/delete
// RPCs and paper writers) when the effective research root changed between the
// initial load and the mutex-guarded re-load: the resolved paths were derived
// from the old root, and persisting them would misattribute work to the new
// one. No row write happens; a retry against the fresh state resolves.
// See specs/decisions/099-research-root-follows-active-session.md.
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

// researchRootForMutation loads the project and returns its research root —
// the effective root, following the active session — with workspace
// containment enforced (SECURITY.md; defense in depth even though the root is
// deterministic). The returned record predates the caller's mutex: callers
// that persist back must re-load under the per-root mutex and merge only
// their field delta (see SetResearchPinned).
func (f *FrontendAPI) researchRootForMutation(projectID string) (string, *project.ProjectInfo, error) {
	f.seedAcquire()
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

	// The root follows the active session; containment is validated against
	// the workspace the root was derived from — a managed session's tree
	// lives outside the checkout (see researchRootWithBoundary).
	researchRoot, boundary := f.researchRootWithBoundary(proj)
	contained, withinErr := config.IsWithinPath(boundary, researchRoot)
	if withinErr != nil {
		return "", nil, fmt.Errorf("failed to validate research root containment: %w", withinErr)
	}
	if !contained {
		return "", nil, fmt.Errorf("research root %q must be inside the workspace %q", researchRoot, boundary)
	}
	return researchRoot, proj, nil
}

// researchGraphAfterMutation re-parses the root after a mutation and returns
// a graph DTO — the rid-targeted project's graph when it resolves (a save
// against a non-active R-NNN must not flip the panel), else the active
// project's, matching CreateHypothesis's active-target semantics; an empty
// DTO when the root is not parseable (not expected right after a write).
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
// workspace). See specs/domains/research.md.
func (f *FrontendAPI) loadProjectForResearch(projectID string) (*project.ProjectInfo, error) {
	f.seedAcquire()
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

// parseResearchRootBestEffort parses a research root into a ResearchRoot
// model, tolerating a missing/unreadable directory by returning nil (the
// frontend renders an empty enabled state rather than erroring; partial
// content is the norm for a freshly-initialized research root).
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
// directories (~/.c0wrk/.agents/{skills,agents}) once per launch — the seven
// research-* skills, the study-paper skill, and the research Subagent
// Profile — from NewFrontendAPI, BEFORE the skill/agent watchers start, so
// the fresh directories are watched on the same launch. Idempotent,
// non-destructive, per-pack failures logged and never fatal; classification
// contract in core/research/skillpack.go and core/papers/skillpack.go.
// Serialized on researchSeedMu; skill/agent caches invalidated afterwards.
// See specs/domains/research.md.
func (f *FrontendAPI) seedGlobalPacks() {
	f.seedAcquire()
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

// applyResearchPins fills a ResearchStatusDTO's pin fields from the
// project's persisted pins, copied and normalized to non-nil so the wire
// shape is a stable empty slice/map instead of null.
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
