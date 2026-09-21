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
	"github.com/v0lka/c0wrk/core/papers"
)

// ---------------------------------------------------------------------------
// Paper-library RPCs
//
// The literature ("papers") library is a GLOBAL subdirectory of a project's
// research root: <research-root>/papers/<slug>/{paper.md, note.md,
// appraisal.md}. It lives independently of any R-NNN research project — no
// project may exist yet, and the library still works. The effective research
// root is the canonical <workspace>/.research (see effectiveResearchRoot).
//
// Papers link to research via the paper card's `research_ids` field (H-NNN
// hypothesis ids); the read RPCs resolve each id to the owning R-NNN project(s)
// when the research root is parseable. Pins reuse the ResearchPins container
// (ProjectInfo.ResearchPins.Papers), stored as research-root-relative
// forward-slash document paths (e.g. "papers/<slug>/paper.md") — the same path
// style the research pins use.
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

	// LinkedResearch resolves the paper's research_ids to the R-NNN project(s)
	// that own them. An id that resolves to no project yields one entry with an
	// empty ResearchID; when the research root is unavailable the list is empty
	// but the raw ResearchIDs are still returned. Always non-nil.
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

// GetPapers returns the active project's paper library. It requires a real
// (non-No-Project) project and enforces workspace containment on the research
// root and the library before any read. A missing or unreadable library is not
// an error: the response carries an empty, non-nil Papers slice so the Papers
// panel renders an empty state.
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

// SetPaperPinned pins (or unpins) a paper identified by its id or slug:
// pinning records the paper card's research-root-relative document path (e.g.
// "papers/<slug>/paper.md") in ProjectInfo.ResearchPins.Papers; unpinning
// removes it. Both directions are idempotent. Pinning additionally requires the
// card file to exist (no pins to phantom papers); unpinning deliberately works
// for a paper whose card has since been deleted — or whose whole directory was
// deleted or renamed (the stale pin is matched by its normalized path/key, not
// only the current on-disk card path) — so stale pins stay removable. No event
// is emitted — the caller's resolved promise is its refresh signal.
//
// The update serializes on the same per-effective-research-root mutex as the
// research pin RPCs (all writers of the projects row), and merges only the
// pins delta under that lock, so a concurrent row save cannot clobber it (or
// vice versa).
func (f *FrontendAPI) SetPaperPinned(projectID, paperID string, pinned bool) error {
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

	// Re-load the row under the lock and persist only the pins delta. The load
	// in papersReadContextFor ran before the mutex was acquired, so a full-row
	// save of that snapshot would clobber whatever was committed while this RPC
	// waited (a concurrent pin toggle, or a research-root change — guarded
	// below).
	fresh, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return err
	}
	if effectiveResearchRoot(fresh) != ctx.researchRoot {
		return errResearchRootChanged
	}

	lib, err := f.parsePaperLibrary(ctx.libraryRoot)
	if err != nil {
		return err
	}
	rec := lib.Get(paperID)

	if !pinned {
		// Unpin tolerates a paper that no longer resolves: its directory may
		// have been deleted (lib.Get == nil) or renamed while the card kept its
		// declared slug, so the stored pin still keys off the old path. Drop
		// every pin referring to this paper by the resolved record's card paths
		// and the normalized key instead of erroring on a nil record — no other
		// RPC removes a paper pin, so a failure here orphans it forever.
		next, changed := removePaperPin(fresh.ResearchPins.Papers, paperID, rec)
		if !changed {
			return nil // already in the requested state
		}
		fresh.ResearchPins.Papers = next
		if err := f.projStore.SaveProject(context.Background(), *fresh); err != nil {
			return fmt.Errorf("failed to persist paper pins: %w", err)
		}
		return nil
	}

	// Pinning fails closed: the paper must resolve AND its card file must exist
	// (no pins to phantom papers).
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
	if err := f.projStore.SaveProject(context.Background(), *fresh); err != nil {
		return fmt.Errorf("failed to persist paper pins: %w", err)
	}
	return nil
}

// removePaperPin removes every pin entry that refers to the paper identified by
// key (an id, a slug, or even a stored pin path). It matches on the resolved
// record's on-disk and declared card paths, on the slugified key, and on the
// raw key itself. Matching these normalized forms — rather than the exact
// on-disk card path alone — is what lets a stale pin still be removed after the
// paper's directory was deleted (rec == nil) or renamed while the card kept its
// declared slug. It returns the next list and whether anything changed.
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

// RecordFlashcardReview records a self-grade for one flashcard of a paper: it
// appends the grade to the paper's flashcards.md review log and updates the
// card's Stage, then returns. The paper is resolved inside the REQUESTING
// project's own containment-checked library (by id or slug) before any file is
// touched, and the whole resolve→read→mutate→write chain serializes on the same
// per-research-root mutex as the research pin RPCs, so a concurrent root
// change cannot land the review in the wrong library. The
// mutation itself is atomic (temp file + rename) and containment-checked in
// core/papers (a symlinked paper directory is rejected). An unknown paper, an
// unknown card id, or an unknown grade is rejected and leaves the deck
// byte-for-byte unchanged. No event is emitted synchronously: the write lands
// inside the watched library, so the file watcher emits `papers:changed` and the
// caller's resolved promise is its refresh signal.
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

	// Re-load the row under the lock and bail out when the research root moved
	// (a root change committed while this RPC waited), so the review never
	// lands in a stale library.
	fresh, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return err
	}
	if effectiveResearchRoot(fresh) != ctx.researchRoot {
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
// "papers" subdirectory of its effective research root. It returns "" for the
// No Project pseudo-project or a project without a workspace, where the
// research/papers subsystems are unavailable. Used by the project switch (to
// track the active library for the watcher) and by the watcher setup (to
// recursively watch the library for every real project).
func papersRootForProject(p *project.ProjectInfo) string {
	root := effectiveResearchRoot(p)
	if root == "" {
		return ""
	}
	return config.PaperLibraryPathIn(root)
}

// comparisonsRootForProject returns the multi-paper comparison root for a
// project: the "comparisons" subdirectory of its effective research root. Like
// the paper library it is a global subdirectory of the research root and is
// watched for every real project, so a comparison artifact written before any
// R-NNN exists still refreshes the Compare section. It returns "" for
// the No Project pseudo-project or a project without a workspace.
func comparisonsRootForProject(p *project.ProjectInfo) string {
	root := effectiveResearchRoot(p)
	if root == "" {
		return ""
	}
	return config.ComparisonsPathIn(root)
}

// effectiveResearchRoot returns the project's research root: the canonical
// <workspace>/.research (RESEARCH is always on for real projects). Returns ""
// for nil, No Project, or a workspace-less project.
func effectiveResearchRoot(p *project.ProjectInfo) string {
	if p == nil || p.IsNoProject || p.WorkspacePath == "" {
		return ""
	}
	return config.ProjectResearchPath(p.WorkspacePath)
}

// emitPapersChanged checks whether any of the changed paths fall inside the
// paper library or the multi-paper comparisons directory and, if so, emits a
// papers:changed event carrying the project ID and comma-separated paths. It
// returns true when at least one path was scoped to either root.
//
// BOTH roots are global subdirectories of the research root (papers/ and
// comparisons/) and BOTH are watched for every real project, so an artifact
// written before any R-NNN exists still refreshes the UI: the
// frontend's papers:changed handler refetches the library and bumps its sync
// key, which the Compare section subscribes to as a refresh key.
//
// Like emitResearchFileChanged, this is a workspace-watcher callback scoped to
// a subdirectory of the canonical research root of a real project: an edit to a
// paper card or a comparison artifact emits the event regardless of which
// research project (if any) is active.
//
// The caller must pass already-snapshotted papersRoot, comparisonsRoot, and
// projectID (read under activeProjectMu) to avoid the data race between the
// fsnotify callback goroutine and project switches on the main thread. An empty
// root is treated as "not applicable" and simply never matches.
func (f *FrontendAPI) emitPapersChanged(papersRoot, comparisonsRoot, projectID string, changedPaths []string) bool {
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
// requiring a real (non-No-Project) project, deriving the effective research
// root, and enforcing workspace containment on both the research root and the
// library before any read (SECURITY.md — defense in depth even though the root
// was validated at enable time). It errors for a missing/unknown project, the
// No Project pseudo-project, or a root that escapes the workspace.
func (f *FrontendAPI) papersReadContextFor(projectID string) (*papersReadContext, error) {
	if projectID == "" {
		return nil, errors.New("project_id is required")
	}
	if f.projectManager == nil {
		return nil, errors.New("project subsystem not initialized")
	}
	// Rejects the No Project pseudo-project (papers require a real project
	// with a workspace).
	proj, err := f.loadProjectForResearch(projectID)
	if err != nil {
		return nil, err
	}
	researchRoot := effectiveResearchRoot(proj)
	if researchRoot == "" {
		return nil, errors.New("project has no workspace to derive a paper library from")
	}
	contained, withinErr := config.IsWithinPath(proj.WorkspacePath, researchRoot)
	if withinErr != nil {
		return nil, fmt.Errorf("failed to validate research root containment: %w", withinErr)
	}
	if !contained {
		return nil, fmt.Errorf("research root %q must be inside the project workspace %q", researchRoot, proj.WorkspacePath)
	}
	libraryRoot := config.PaperLibraryPathIn(researchRoot)
	libContained, libErr := config.IsWithinPath(proj.WorkspacePath, libraryRoot)
	if libErr != nil {
		return nil, fmt.Errorf("failed to validate paper library containment: %w", libErr)
	}
	if !libContained {
		return nil, fmt.Errorf("paper library %q must be inside the project workspace %q", libraryRoot, proj.WorkspacePath)
	}
	return &papersReadContext{project: proj, researchRoot: researchRoot, libraryRoot: libraryRoot}, nil
}

// parsePaperLibrary parses a library root. A library that has not been created
// yet (the root does not exist) is the legitimate empty state and yields an
// empty library. Any OTHER error (permissions, I/O, a stale mount, or a path
// that is not a directory) is a genuine read failure and is returned rather
// than silently degraded to an empty library: collapsing the two made an
// unreadable library indistinguishable from "no papers studied yet", hiding
// the failure from the Papers panel. The failure is also logged at Warn so it
// is visible at the default log level.
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

// paperCardRelPath renders the pin-path form of a paper card: the paper
// directory's path relative to the research root (forward slashes) joined with
// the card file name, e.g. "papers/<slug>/paper.md". The slug is taken from the
// actual directory on disk (not the record's ResolvedSlug) so the pin path
// always matches the directory the writer and watcher use. It falls back to the
// directory's base name when the relative path cannot be computed (the
// containment check uses the centralized path API rather than the forbidden
// filepath.Rel + strings.HasPrefix idiom — always use the constants/helpers
// from config/pathutil).
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

// researchLinkIndex maps every hypothesis id (H-NNN) in the project's research
// root to the R-NNN project id(s) that own it. A hypothesis id can exist in
// more than one research project, so the value is a list. An empty, disabled,
// or unparseable root yields an empty map — the paper's raw research_ids are
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

// linksForResearchIDs resolves each paper research id (H-NNN) to the R-NNN
// project(s) that own it, producing one link per (H-NNN, R-NNN) pair. An
// unresolvable id yields a single link with an empty ResearchID so the frontend
// can still surface the dangling reference. The result is always non-nil.
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
