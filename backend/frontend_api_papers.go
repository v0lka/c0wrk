package backend

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/papers"
)

// ---------------------------------------------------------------------------
// Paper-library RPCs
//
// The literature ("papers") library is a GLOBAL subdirectory of a project's
// research root — <research-root>/papers/<slug>/ — independent of any R-NNN
// research project. Papers link to research via the card's research_ids
// (H-NNN), resolved by the read RPCs when the root is parseable; pins reuse
// the ResearchPins container as research-root-relative forward-slash paths.
// See specs/domains/papers.md.
// ---------------------------------------------------------------------------

// PapersDTO is the response for GetPapers: the project's paper library, fully
// normalized for the frontend. Every collection is non-nil (empty slice, never
// null) so consumers need no nil checks at the boundary.
type PapersDTO struct {
	// ProjectID is the project these results pertain to.
	ProjectID string `json:"project_id"`

	// ResearchRoot is the project's effective research root (the canonical
	// <workspace>/.research). The library is a subdirectory of it.
	ResearchRoot string `json:"research_root"`

	// Root is the absolute path of the paper library
	// (<research_root>/papers).
	Root string `json:"root"`

	// Papers lists every parsed paper, ordered by ID then slug. Always
	// non-nil (empty slice when the library is empty/absent).
	Papers []PaperDTO `json:"papers"`

	// Pinned lists the pinned paper-card document paths (research-root-relative,
	// forward slashes, e.g. "papers/<slug>/paper.md"). Always non-nil.
	Pinned []string `json:"pinned"`
}

// PaperDTO is the normalized wire shape of a single paper. Collections are
// non-nil (empty rather than null) so the frontend's boundary guard is a pure
// pass-through.
type PaperDTO struct {
	ID          string              `json:"id"`
	Slug        string              `json:"slug"`
	Title       string              `json:"title"`
	Authors     []string            `json:"authors"`
	Year        int                 `json:"year"`
	Venue       string              `json:"venue"`
	Identifiers []papers.Identifier `json:"identifiers"`
	Mode        string              `json:"mode"`
	Reading     string              `json:"reading"`
	Verdict     string              `json:"verdict"`
	Confidence  string              `json:"confidence"`
	// ResearchIDs lists the research-hypothesis ids (H-NNN) this paper
	// informs, verbatim from paper.md. Always non-nil.
	ResearchIDs []string        `json:"research_ids"`
	Anchors     []papers.Anchor `json:"anchors"`

	// Claims, RedFlags, and Uncertainties are parsed from note.md.
	Claims        []papers.Claim       `json:"claims"`
	RedFlags      []papers.RedFlag     `json:"red_flags"`
	Uncertainties []papers.Uncertainty `json:"uncertainties"`

	// Dir is the absolute paper directory the record was parsed from (for the
	// file viewer).
	Dir string `json:"dir"`

	// CardPath is the paper card's document path relative to the research root
	// (forward slashes) — the pin-path form, e.g. "papers/<slug>/paper.md".
	CardPath string `json:"card_path"`

	// Pinned reports whether this paper's card is currently pinned.
	Pinned bool `json:"pinned"`

	// LinkedResearch resolves the paper's research_ids to the R-NNN
	// project(s) that own them. An id that resolves to no project yields one
	// entry with an empty ResearchID; when the root is unavailable the list
	// is empty but the raw ResearchIDs are still returned. Always non-nil.
	LinkedResearch []PaperResearchLinkDTO `json:"linked_research"`
}

// PaperResearchLinkDTO is one resolved paper→research link: a hypothesis id
// (H-NNN) referenced by a paper and the R-NNN project that owns it (empty when
// the hypothesis exists in no parsed research project).
type PaperResearchLinkDTO struct {
	HypothesisID string `json:"hypothesis_id"`
	ResearchID   string `json:"research_id"`
}

// papersReadContext is the resolved, containment-checked view of a project's
// paper library used by the read RPCs.
type papersReadContext struct {
	project      *project.ProjectInfo
	researchRoot string
	libraryRoot  string
}

// GetPapers returns the project's paper library, fully normalized (non-nil
// collections). Requires a real project and enforces workspace containment
// before any read; a missing/unreadable library is an empty state, not an
// error. See specs/domains/papers.md.
func (f *FrontendAPI) GetPapers(projectID string) (*PapersDTO, error) {
	ctx, err := f.papersReadContextFor(projectID)
	if err != nil {
		return nil, err
	}
	lib, err := f.parsePaperLibrary(ctx.libraryRoot)
	if err != nil {
		return nil, err
	}
	links := f.researchLinkIndex(ctx.researchRoot)
	pinned := paperPinSet(ctx.project.ResearchPins.Papers)

	dto := &PapersDTO{
		ProjectID:    projectID,
		ResearchRoot: ctx.researchRoot,
		Root:         ctx.libraryRoot,
		Papers:       make([]PaperDTO, 0, len(lib.Papers)),
		Pinned:       normalizedCopy(ctx.project.ResearchPins.Papers),
	}
	for _, rec := range lib.Papers {
		dto.Papers = append(dto.Papers, f.toPaperDTO(rec, ctx.researchRoot, pinned, links))
	}
	return dto, nil
}

// GetPaper returns a single paper by its id (P-NNN) or slug. It uses the same
// project/containment requirements as GetPapers and returns an error when the
// paper is not found in the library.
func (f *FrontendAPI) GetPaper(projectID, paperID string) (*PaperDTO, error) {
	if strings.TrimSpace(paperID) == "" {
		return nil, errors.New("paper id or slug is required")
	}
	ctx, err := f.papersReadContextFor(projectID)
	if err != nil {
		return nil, err
	}
	lib, err := f.parsePaperLibrary(ctx.libraryRoot)
	if err != nil {
		return nil, err
	}
	rec := lib.Get(paperID)
	if rec == nil {
		return nil, fmt.Errorf("paper %q not found in the library", paperID)
	}
	links := f.researchLinkIndex(ctx.researchRoot)
	pinned := paperPinSet(ctx.project.ResearchPins.Papers)
	dto := f.toPaperDTO(rec, ctx.researchRoot, pinned, links)
	return &dto, nil
}

// SetPaperPinned pins (or unpins) a paper identified by its id or slug,
// recording the card's research-root-relative path in
// ProjectInfo.ResearchPins.Papers. Idempotent both ways; pinning requires the
// card file to exist, unpinning deliberately works for a since-deleted or
// renamed paper (stale pins matched by normalized path/key stay removable).
// No event emitted. Serializes on the per-effective-research-root mutex,
// merging only the pins delta under the lock. See specs/domains/papers.md.
func (f *FrontendAPI) SetPaperPinned(projectID, paperID string, pinned bool) error {
	f.seedAcquire()
	if f.projStore == nil {
		return errors.New("project subsystem not initialized")
	}
	if strings.TrimSpace(paperID) == "" {
		return errors.New("paper id or slug is required")
	}
	ctx, err := f.papersReadContextFor(projectID)
	if err != nil {
		return err
	}

	mu := f.researchMutationMu(ctx.researchRoot)
	mu.Lock()
	defer mu.Unlock()

	// Re-load the row under the lock and persist only the pins delta — the
	// load in papersReadContextFor ran before the mutex (see SetResearchPinned
	// for the lost-update rationale).
	fresh, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return err
	}
	if f.effectiveResearchRoot(fresh) != ctx.researchRoot {
		return errResearchRootChanged
	}

	lib, err := f.parsePaperLibrary(ctx.libraryRoot)
	if err != nil {
		return err
	}
	rec := lib.Get(paperID)

	if !pinned {
		// Unpin tolerates a paper that no longer resolves (directory deleted
		// or renamed while the card kept its declared slug): drop every pin
		// referring to it by the resolved card paths and the normalized key
		// instead of erroring on a nil record — no other RPC removes a paper
		// pin, so a failure here orphans it forever.
		next, changed := removePaperPin(fresh.ResearchPins.Papers, paperID, rec)
		if !changed {
			return nil // already in the requested state
		}
		fresh.ResearchPins.Papers = next
		// Bounded by storeOpCtx (per-root mutation mutex — head-of-line
		// blocking, see SetResearchPinned).
		pctx, pcancel := f.storeOpCtx()
		if err := f.projStore.SaveProject(pctx, *fresh); err != nil {
			pcancel()
			return fmt.Errorf("failed to persist paper pins: %w", err)
		}
		pcancel()
		return nil
	}

	// Pinning fails closed: the paper must resolve AND its card file must
	// exist (no pins to phantom papers).
	if rec == nil {
		return fmt.Errorf("paper %q not found in the library", paperID)
	}
	cardPath := paperCardRelPath(ctx.researchRoot, rec)
	if _, statErr := os.Stat(filepath.Join(rec.Dir, papers.PaperFileName)); statErr != nil {
		return fmt.Errorf("paper card for %q not found: %w", paperID, statErr)
	}

	next, changed := togglePinnedPath(fresh.ResearchPins.Papers, cardPath, true)
	if !changed {
		return nil // already in the requested state
	}
	fresh.ResearchPins.Papers = next
	pctx, pcancel := f.storeOpCtx()
	if err := f.projStore.SaveProject(pctx, *fresh); err != nil {
		pcancel()
		return fmt.Errorf("failed to persist paper pins: %w", err)
	}
	pcancel()
	return nil
}

// removePaperPin removes every pin entry referring to the paper identified by
// key (id, slug, or a stored pin path): it matches the resolved record's
// on-disk, declared, and resolved card paths, the slugified key, and the raw
// key — matching these normalized forms is what lets a stale pin still be
// removed after the paper's directory was deleted (rec == nil) or renamed.
func removePaperPin(pins []string, key string, rec *papers.PaperRecord) ([]string, bool) {
	candidates := map[string]bool{}
	add := func(rel string) {
		if rel = path.Clean(strings.TrimSpace(rel)); rel != "" && rel != "." {
			candidates[rel] = true
		}
	}
	add(key)
	if slug := papers.Slugify(key); slug != "" {
		add(path.Join("papers", slug, papers.PaperFileName))
	}
	if rec != nil {
		add(path.Join("papers", filepath.Base(rec.Dir), papers.PaperFileName))
		add(path.Join("papers", rec.Slug, papers.PaperFileName))
		add(path.Join("papers", rec.ResolvedSlug(), papers.PaperFileName))
	}
	next := make([]string, 0, len(pins))
	changed := false
	for _, p := range pins {
		if candidates[path.Clean(filepath.ToSlash(p))] {
			changed = true
			continue
		}
		next = append(next, p)
	}
	return next, changed
}

// RecordFlashcardReview records a self-grade for one flashcard of a paper:
// appends the grade to the paper's flashcards.md review log and advances the
// card's Stage. The paper resolves inside the REQUESTING project's
// containment-checked library; the chain serializes on the per-research-root
// mutex, the mutation is atomic and containment-checked in core/papers, and
// an unknown paper/card/grade leaves the deck byte-for-byte unchanged. No
// synchronous event — the watcher emits papers:changed. See
// specs/domains/papers.md.
func (f *FrontendAPI) RecordFlashcardReview(projectID, paperID, cardID, grade string) error {
	if strings.TrimSpace(paperID) == "" {
		return errors.New("paper id or slug is required")
	}
	if strings.TrimSpace(cardID) == "" {
		return errors.New("card id is required")
	}
	g := papers.NormalizeGrade(grade)
	if g == "" {
		return fmt.Errorf("unknown flashcard grade %q", grade)
	}
	ctx, err := f.papersReadContextFor(projectID)
	if err != nil {
		return err
	}

	mu := f.researchMutationMu(ctx.researchRoot)
	mu.Lock()
	defer mu.Unlock()

	// Re-load the row under the lock and bail out when the research root
	// moved, so the review never lands in a stale library.
	fresh, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return err
	}
	if f.effectiveResearchRoot(fresh) != ctx.researchRoot {
		return errResearchRootChanged
	}

	lib, err := f.parsePaperLibrary(ctx.libraryRoot)
	if err != nil {
		return err
	}
	rec := lib.Get(paperID)
	if rec == nil {
		return fmt.Errorf("paper %q not found in the library", paperID)
	}

	slug := filepath.Base(rec.Dir)
	date := time.Now().Format("2006-01-02")
	if err := papers.RecordCardReview(ctx.libraryRoot, slug, cardID, g, date); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// watcher helpers
// ---------------------------------------------------------------------------

// papersRootForProject returns the paper-library root for a project: the
// "papers" subdirectory of its effective research root ("" for No Project or
// a workspace-less project). Used by the project switch and watcher setup.
func (f *FrontendAPI) papersRootForProject(p *project.ProjectInfo) string {
	root := f.effectiveResearchRoot(p)
	if root == "" {
		return ""
	}
	return config.PaperLibraryPathIn(root)
}

// comparisonsRootForProject returns the multi-paper comparison root for a
// project: the "comparisons" subdirectory of its effective research root —
// watched for every real project, so an artifact written before any R-NNN
// exists still refreshes the Compare section.
func (f *FrontendAPI) comparisonsRootForProject(p *project.ProjectInfo) string {
	root := f.effectiveResearchRoot(p)
	if root == "" {
		return ""
	}
	return config.ComparisonsPathIn(root)
}

// effectiveResearchRoot returns the project's effective research root,
// following the ACTIVE SESSION (ADR-099): <workspace-of-the-active-session>/.research,
// where the active session is the user-selected session when a usable
// selection is persisted (project_ui_state.saved_session_id), falling back to
// the most recently active non-archived session — resolved through the
// READ-ONLY Manager.WorkspacePathFor with GetSessionWorkspace's project-ID
// membership, else the project checkout (a local session's workspace IS the
// checkout, so the two agree by construction; a managed tree resolving to its
// own root is what makes worktree research visible). "" for nil, No Project,
// or a workspace-less project. Containment must validate against the
// workspace the root was derived from — use researchRootWithBoundary.
// See specs/decisions/099-research-root-follows-active-session.md.
func (f *FrontendAPI) effectiveResearchRoot(p *project.ProjectInfo) string {
	root, _ := f.researchRootWithBoundary(p)
	return root
}

// researchRootWithBoundary returns the project's effective research root
// (see effectiveResearchRoot) with the workspace its containment must be
// validated against — the session workspace when the root followed a session
// (a managed tree lives OUTSIDE the checkout), the checkout on the fallback.
func (f *FrontendAPI) researchRootWithBoundary(p *project.ProjectInfo) (root, boundary string) {
	if p == nil || p.IsNoProject || p.WorkspacePath == "" {
		return "", ""
	}
	if ws, ok := f.activeSessionWorkspace(p.ID); ok {
		return config.ProjectResearchPath(ws), ws
	}
	return config.ProjectResearchPath(p.WorkspacePath), p.WorkspacePath
}

// activeSessionWorkspace resolves the workspace of the project's active
// session: the USER-SELECTED session when a usable selection is persisted,
// falling back to the most recently active non-archived session — through the
// READ-ONLY Manager.WorkspacePathFor, never GetSessionWorkspacePath (whose
// lazy restore would rebuild a full orchestrator on a path lookup).
// Membership is by project ID (store row, resident in-memory fallback); both
// reads share one terminalPathLookupTimeout-bounded context. ok=false when
// the manager is unavailable or nothing resolves — a foreign session must
// never steer the root. See
// specs/decisions/099-research-root-follows-active-session.md.
func (f *FrontendAPI) activeSessionWorkspace(projectID string) (string, bool) {
	if f.appCell() == nil {
		return "", false
	}
	mgr := f.app.Manager()
	if mgr == nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(f.ctx(), terminalPathLookupTimeout)
	defer cancel()

	sessionID, err := f.activeSessionID(ctx, mgr, projectID)
	if err != nil || sessionID == "" {
		return "", false
	}
	ws, hasWS := mgr.WorkspacePathFor(ctx, sessionID)
	if !hasWS || ws == "" {
		return "", false
	}
	if f.store != nil {
		if info, rowErr := f.store.LoadSession(ctx, sessionID); rowErr == nil && info != nil {
			if info.ProjectID == projectID {
				return ws, true
			}
			return "", false
		}
	}
	if pid, live := mgr.SessionProjectID(sessionID); live {
		return ws, pid == projectID
	}
	// No authority answered (transient store failure, session neither stored
	// nor resident): treat as no session so the caller takes its fallback.
	return "", false
}

// activeSessionID resolves which of projectID's sessions is "active": the
// persisted user selection first (validated against the same non-archived
// membership list the fallback orders — one ListSessionsByProjectCtx
// round-trip serves both), else the activity-latest session — ONE definition
// for every backend consumer, so a backend root can no longer disagree with
// the session the UI shows. A selection-read failure is fail-soft (nothing is
// written on this path). See
// specs/decisions/099-research-root-follows-active-session.md.
func (f *FrontendAPI) activeSessionID(ctx context.Context, mgr *session.Manager, projectID string) (string, error) {
	sessions, err := mgr.ListSessionsByProjectCtx(ctx, projectID)
	if err != nil {
		return "", err
	}
	if saved := f.savedSessionIDForProject(ctx, projectID); saved != "" {
		for _, s := range sessions {
			if s.ID == saved && !s.Archived {
				return saved, nil
			}
		}
	}
	return latestOfSessions(sessions), nil
}

// savedSessionIDForProject reads project_ui_state.saved_session_id — the
// frontend's persisted selection (SaveProjectActiveSession). A missing
// subsystem or store error yields "" (fail-soft, Debug-logged): the caller
// falls back to the activity-latest session.
func (f *FrontendAPI) savedSessionIDForProject(ctx context.Context, projectID string) string {
	if f.projStore == nil {
		return ""
	}
	state, err := f.projStore.LoadUIState(ctx, projectID)
	if err != nil {
		f.log().Debug("failed to load saved session selection", "project", projectID, "error", err)
		return ""
	}
	if state == nil {
		return ""
	}
	return strings.TrimSpace(state.SavedSessionID)
}

// latestSessionID returns the id of projectID's most recently active
// non-archived session (the activity-latest fallback of activeSessionID),
// through one ListSessionsByProjectCtx call.
func (f *FrontendAPI) latestSessionID(ctx context.Context, mgr *session.Manager, projectID string) (string, error) {
	sessions, err := mgr.ListSessionsByProjectCtx(ctx, projectID)
	if err != nil {
		return "", err
	}
	return latestOfSessions(sessions), nil
}

// latestOfSessions returns the most recently active non-archived session of
// the list (the SessionInfo.LastActiveAt ordering ListSessionsByProjectCtx
// exposes, falling back to created_at). Archived sessions are skipped — they
// are read-only and hidden from the picker, so they must never be selected.
func latestOfSessions(sessions []session.SessionInfo) string {
	latestID := ""
	var latestTS time.Time
	for _, candidate := range sessions {
		if candidate.Archived {
			continue
		}
		ts := parseSessionActivityTimestamp(candidate)
		if latestID == "" || ts.After(latestTS) {
			latestID = candidate.ID
			latestTS = ts
		}
	}
	return latestID
}

// emitPapersChanged checks whether any changed path falls inside the paper
// library or comparisons directory and emits papers:changed (project ID +
// comma-separated paths); true when at least one path matched. BOTH roots are
// global subdirectories of the research root, watched for every real project,
// so an artifact written before any R-NNN exists still refreshes the UI.
//
// The caller must pass already-snapshotted papersRoot, comparisonsRoot, and
// projectID (read under activeProjectMu) — the fsnotify callback goroutine
// races project switches otherwise. An empty root never matches.
func (f *FrontendAPI) emitPapersChanged(papersRoot, comparisonsRoot, projectID string, changedPaths []string) bool {
	f.seedAcquire()
	if papersRoot == "" && comparisonsRoot == "" {
		return false
	}
	var paperPaths []string
	for _, p := range changedPaths {
		if pathWithinRoot(papersRoot, p) || pathWithinRoot(comparisonsRoot, p) {
			paperPaths = append(paperPaths, p)
		}
	}
	if len(paperPaths) == 0 {
		return false
	}
	f.log().Debug("papers: file changed in the paper library or comparisons dir, emitting event",
		"project_id", projectID,
		"papers_root", papersRoot,
		"comparisons_root", comparisonsRoot,
		"paths", paperPaths,
	)
	f.emitEvent(EventPapersChanged, map[string]string{
		"project_id": projectID,
		"paths":      strings.Join(paperPaths, ","),
	})
	return true
}

// pathWithinRoot reports whether p lies inside root, tolerating an empty root
// (treated as "not applicable") and a containment error (treated as "outside").
func pathWithinRoot(root, p string) bool {
	if root == "" {
		return false
	}
	within, err := config.IsWithinPath(root, p)
	return err == nil && within
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// papersReadContextFor resolves the paper-library context for a project:
// a real (non-No-Project) project, the effective research root, and workspace
// containment on both root and library before any read (SECURITY.md —
// defense in depth). Errors for a missing project, No Project, or an
// escaping root. See specs/domains/papers.md.
func (f *FrontendAPI) papersReadContextFor(projectID string) (*papersReadContext, error) {
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
	// The root follows the active session (see researchRootWithBoundary).
	researchRoot, boundary := f.researchRootWithBoundary(proj)
	if researchRoot == "" {
		return nil, errors.New("project has no workspace to derive a paper library from")
	}
	contained, withinErr := config.IsWithinPath(boundary, researchRoot)
	if withinErr != nil {
		return nil, fmt.Errorf("failed to validate research root containment: %w", withinErr)
	}
	if !contained {
		return nil, fmt.Errorf("research root %q must be inside the workspace %q", researchRoot, boundary)
	}
	libraryRoot := config.PaperLibraryPathIn(researchRoot)
	libContained, libErr := config.IsWithinPath(boundary, libraryRoot)
	if libErr != nil {
		return nil, fmt.Errorf("failed to validate paper library containment: %w", libErr)
	}
	if !libContained {
		return nil, fmt.Errorf("paper library %q must be inside the workspace %q", libraryRoot, boundary)
	}
	return &papersReadContext{project: proj, researchRoot: researchRoot, libraryRoot: libraryRoot}, nil
}

// parsePaperLibrary parses a library root. A not-yet-created library is the
// legitimate empty state; any OTHER error is a genuine read failure, returned
// rather than silently degraded — collapsing the two made an unreadable
// library indistinguishable from "no papers studied yet". Also Warn-logged.
func (f *FrontendAPI) parsePaperLibrary(libraryRoot string) (*papers.PaperLibrary, error) {
	lib, err := papers.ParseLibraryDir(libraryRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &papers.PaperLibrary{Root: libraryRoot}, nil
		}
		f.log().Warn("paper library is unreadable",
			"root", libraryRoot, "error", err)
		return nil, fmt.Errorf("failed to read paper library %q: %w", libraryRoot, err)
	}
	return lib, nil
}

// toPaperDTO normalizes a parsed record into the wire shape: string enums,
// non-nil collections, the pin-path form of its card, and the resolved R-NNN
// links for its research_ids.
func (f *FrontendAPI) toPaperDTO(rec *papers.PaperRecord, researchRoot string, pinned map[string]bool, links map[string][]string) PaperDTO {
	cardPath := paperCardRelPath(researchRoot, rec)
	return PaperDTO{
		ID:             rec.ID,
		Slug:           rec.Slug,
		Title:          rec.Title,
		Authors:        nonNilSlice(rec.Authors),
		Year:           rec.Year,
		Venue:          rec.Venue,
		Identifiers:    nonNilSlice(rec.Identifiers),
		Mode:           string(rec.Mode),
		Reading:        string(rec.Reading),
		Verdict:        string(rec.Verdict),
		Confidence:     string(rec.Confidence),
		ResearchIDs:    nonNilSlice(rec.ResearchIDs),
		Anchors:        nonNilSlice(rec.Anchors),
		Claims:         nonNilSlice(rec.Claims),
		RedFlags:       nonNilSlice(rec.RedFlags),
		Uncertainties:  nonNilSlice(rec.Uncertainties),
		Dir:            rec.Dir,
		CardPath:       cardPath,
		Pinned:         pinned[cardPath],
		LinkedResearch: linksForResearchIDs(rec.ResearchIDs, links),
	}
}

// paperCardRelPath renders the pin-path form of a paper card: the directory's
// path relative to the research root (forward slashes) joined with the card
// file name. The slug comes from the actual on-disk directory (not
// ResolvedSlug) so the pin path always matches the directory the writer and
// watcher use; falls back to the base name when the relative path fails.
func paperCardRelPath(researchRoot string, rec *papers.PaperRecord) string {
	dir := rec.Dir
	rel := ""
	if researchRoot != "" {
		if contained, err := config.IsWithinPath(researchRoot, dir); err == nil && contained {
			if r, relErr := filepath.Rel(researchRoot, dir); relErr == nil && r != "" && r != "." {
				rel = filepath.ToSlash(r)
			}
		}
	}
	if rel == "" {
		rel = filepath.Base(dir)
	}
	return path.Join(rel, papers.PaperFileName)
}

// researchLinkIndex maps every hypothesis id (H-NNN) in the research root to
// the R-NNN project id(s) that own it (a list — one id can exist in several
// projects). An unparseable root yields an empty map: raw research_ids are
// still returned, they simply cannot be resolved to an R-NNN.
func (f *FrontendAPI) researchLinkIndex(researchRoot string) map[string][]string {
	idx := make(map[string][]string)
	root := f.parseResearchRootBestEffort(researchRoot)
	if root == nil {
		return idx
	}
	for _, p := range root.Projects {
		if p == nil {
			continue
		}
		for _, n := range p.Graph.Nodes {
			if n == nil || n.ID == "" {
				continue
			}
			if !containsString(idx[n.ID], p.ID) {
				idx[n.ID] = append(idx[n.ID], p.ID)
			}
		}
	}
	return idx
}

// linksForResearchIDs resolves each research id (H-NNN) to the R-NNN
// project(s) that own it — one link per (H-NNN, R-NNN) pair; an unresolvable
// id yields a link with an empty ResearchID so the frontend surfaces the
// dangling reference. Always non-nil.
func linksForResearchIDs(researchIDs []string, links map[string][]string) []PaperResearchLinkDTO {
	out := make([]PaperResearchLinkDTO, 0, len(researchIDs))
	for _, hid := range researchIDs {
		rids := links[hid]
		if len(rids) == 0 {
			out = append(out, PaperResearchLinkDTO{HypothesisID: hid})
			continue
		}
		for _, rid := range rids {
			out = append(out, PaperResearchLinkDTO{HypothesisID: hid, ResearchID: rid})
		}
	}
	return out
}

// paperPinSet builds a lookup set from a persisted pin list.
func paperPinSet(pins []string) map[string]bool {
	set := make(map[string]bool, len(pins))
	for _, p := range pins {
		set[p] = true
	}
	return set
}

// normalizedCopy returns a non-nil copy of a pin list (never null on the wire).
func normalizedCopy(pins []string) []string {
	out := make([]string, len(pins))
	copy(out, pins)
	return out
}

// nonNilSlice returns in when it is non-nil, else an empty non-nil slice of the
// same type, so DTO collections serialize as [] rather than null.
func nonNilSlice[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

// containsString reports whether list contains v.
func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
