package backend

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/v0lka/sp4rk/pathutil"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/sdd"
	"github.com/v0lka/sp4rk/skills"
)

// The framework-aware SDD surface (Specs panel).
//
// Unlike the hand-written specs/ reader this replaces, the backend no longer
// knows a specification layout. It asks core/sdd three questions:
//
//  1. WHICH SDD frameworks are installed here? — detection, decided by the
//     agent skills each framework's `init` installs (core/sdd/detect.go).
//  2. WHICH one is active? — the operator's per-project persisted selection,
//     else detection's default (tree present > strongest skill tier >
//     declaration order).
//  3. WHAT does its tree look like? — one declarative parse through the
//     selected framework's definition (core/sdd/parse.go).
//
// Everything the panel renders (documents, nodes, views, findings, counts,
// actions) is DSL-driven: adding a framework is a YAML document, never a Go
// change. The backend contributes only what core/sdd deliberately does not
// know — the filesystem layout of skill discovery tiers, the two operator DSL
// files, the session-following workspace, and the sidebar gate.

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

// SpecsStatusDTO is the Specs-panel view model. Every collection and every
// map is non-nil on the wire (empty slices/maps, never null), so the frontend
// renders without null guards.
//
// `present` is the VISIBILITY answer — the sidebar gate is on AND at least one
// SDD framework is detected. It is not the tree state: a detected framework
// whose declared roots are absent still reports `tree_present=false` (the
// panel's empty state) while `present` stays true. No Project is the neutral
// `present=false` shape.
type SpecsStatusDTO struct {
	// Present reports whether the Specs surface should render:
	// gate && len(detected) > 0.
	Present bool `json:"present"`

	// TreePresent reports whether the active framework's declared roots exist
	// in the parsed workspace.
	TreePresent bool `json:"tree_present"`

	// Root is the absolute path of the active framework's first declared root
	// (the first PRESENT one when any root exists). "" when nothing is active.
	Root string `json:"root"`

	// Workspace is the absolute workspace the tree was parsed in — the active
	// session's tree when one resolves, the checkout otherwise.
	Workspace string `json:"workspace"`

	// Detected lists every recognised framework, in detection default order.
	// Empty means no framework is installed, which hides the surface.
	Detected []SDDFrameworkDTO `json:"detected"`

	// ActiveID is the active framework's id; "" when none is active.
	ActiveID string `json:"active_id,omitempty"`
	// Active is the active framework's detection record (the matching element
	// of Detected); nil when none is active.
	Active *SDDFrameworkDTO `json:"active,omitempty"`

	// SelectorNeeded reports that more than one framework is detected and the
	// operator has not chosen one — the surface shows a selector.
	SelectorNeeded bool `json:"selector_needed"`

	// Roots reports each declared root of the active framework.
	Roots []SDDRootDTO `json:"roots"`

	// Documents, Nodes, Views and Findings are the parsed model of the active
	// framework's tree. Always non-nil.
	Documents []SDDDocumentDTO `json:"documents"`
	Nodes     []SDDNodeDTO     `json:"nodes"`
	Views     []SDDViewDTO     `json:"views"`
	Findings  []SDDFindingDTO  `json:"findings"`

	// Actions are the active framework's declared lifecycle gestures, each
	// with a computed availability flag.
	Actions []SDDActionDTO `json:"actions"`

	// Counts aggregates the collections for badges and filters.
	Counts SDDCountsDTO `json:"counts"`

	// Warnings holds non-fatal diagnostics: operator DSL file faults and the
	// parser's read warnings. Never errors.
	Warnings []string `json:"warnings"`
}

// SDDFrameworkDTO is one detected SDD framework: the definition's identity
// plus the evidence that recognised it.
type SDDFrameworkDTO struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	DocsURL     string `json:"docs_url,omitempty"`
	// Origin is builtin | global | project — where the definition came from.
	Origin string `json:"origin"`
	// Scope is the strongest skill-discovery tier among the matched skills
	// (project | global | user).
	Scope string `json:"scope"`
	// TreePresent reports a declared root or marker existing in the workspace.
	TreePresent bool `json:"tree_present"`
	// RootPresent / MarkerPresent are the two tree signals.
	RootPresent   bool `json:"root_present"`
	MarkerPresent bool `json:"marker_present"`
	// Skills are the installed skills that satisfied the framework's declared
	// set, strongest tier first.
	Skills []SDDMatchedSkillDTO `json:"skills"`
}

// SDDMatchedSkillDTO is one installed skill that recognised a framework.
type SDDMatchedSkillDTO struct {
	Name  string `json:"name"`
	Dir   string `json:"dir"`
	Scope string `json:"scope"`
}

// SDDRootDTO is one declared framework root and its presence.
type SDDRootDTO struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Label    string `json:"label,omitempty"`
	Required bool   `json:"required"`
	Present  bool   `json:"present"`
}

// SDDDocumentDTO mirrors one parsed concept instance that is a file.
type SDDDocumentDTO struct {
	Path         string `json:"path"`
	Root         string `json:"root"`
	Concept      string `json:"concept"`
	Label        string `json:"label,omitempty"`
	Section      string `json:"section,omitempty"`
	Name         string `json:"name"`
	Number       string `json:"number,omitempty"`
	Slug         string `json:"slug"`
	Title        string `json:"title,omitempty"`
	DisplayTitle string `json:"display_title,omitempty"`
	// Date is the optional git-derived add date (YYYY-MM-DD) for a decision
	// document; omitted when unknown.
	Date         string            `json:"date,omitempty"`
	Headings     []string          `json:"headings"`
	KeyFiles     []string          `json:"key_files"`
	CrossRefs    []string          `json:"cross_refs"`
	Frontmatter  map[string]string `json:"frontmatter,omitempty"`
	Status       string            `json:"status,omitempty"`
	SupersededBy string            `json:"superseded_by,omitempty"`
	Purpose      string            `json:"purpose,omitempty"`
	Requirements []string          `json:"requirements"`
	Scenarios    []string          `json:"scenarios"`
	Deltas       []string          `json:"deltas"`
	Principles   []string          `json:"principles"`
	UserStories  []string          `json:"user_stories"`
	TaskItems    []SDDTaskItemDTO  `json:"task_items"`
	Tasks        []SDDIndexTaskDTO `json:"tasks"`
	Missing      []string          `json:"missing"`
}

// SDDTaskItemDTO is one checkbox item.
type SDDTaskItemDTO struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// SDDIndexTaskDTO is one navigation-table row.
type SDDIndexTaskDTO struct {
	Task  string   `json:"task"`
	Specs []string `json:"specs"`
}

// SDDNodeDTO is one rendered tree node (folder, document or directory).
type SDDNodeDTO struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Path     string   `json:"path"`
	Label    string   `json:"label"`
	Concept  string   `json:"concept,omitempty"`
	Name     string   `json:"name"`
	Parent   string   `json:"parent,omitempty"`
	Children []string `json:"children"`
	Missing  []string `json:"missing,omitempty"`
}

// SDDViewDTO is one declared view section.
type SDDViewDTO struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	Kind  string   `json:"kind"`
	Nodes []string `json:"nodes"`
	// Paths carries the section's full node membership (its concepts'
	// instances plus their ancestor folders), so the panel renders each
	// section's own subtree instead of the shared forest.
	Paths []string `json:"paths,omitempty"`
}

// SDDFindingDTO is one failed declared check.
type SDDFindingDTO struct {
	Kind     string `json:"kind"`
	File     string `json:"file"`
	Detail   string `json:"detail"`
	Message  string `json:"message,omitempty"`
	Severity string `json:"severity"`
}

// SDDActionDTO is one declared lifecycle gesture.
type SDDActionDTO struct {
	Kind     string   `json:"kind"`
	Label    string   `json:"label"`
	Skill    string   `json:"skill,omitempty"`
	Prompt   string   `json:"prompt,omitempty"`
	Concept  string   `json:"concept,omitempty"`
	Requires []string `json:"requires"`
	// Available reports every declared requirement holding for this tree AND the
	// declared skill resolving to an installed, callable one: it is the single
	// flag a surface dispatches on, so a gesture whose tree is complete but whose
	// skill is missing is not available either.
	Available bool `json:"available"`
	// SkillAvailable reports the declared `skill` resolving to an INSTALLED,
	// callable skill. It is a SEPARATE fact from Available because a framework's
	// declared skill set can be only partially installed (detect.skills.any_of):
	// the tree can satisfy every requirement while the declared skill is absent,
	// and the two causes need different operator actions — build the tree versus
	// install the skill — so a surface that folds them together names the wrong
	// fault. Always true for a skill-less action (nothing to resolve) and when
	// the installed catalog is undeterminable.
	SkillAvailable bool `json:"skill_available"`
	// UnavailableAt lists the node paths at which a per-node requirement
	// (companion:/absent:) does NOT hold. Available answers "could this gesture
	// apply anywhere in this tree?"; this answers "does it apply HERE?", so the
	// panel disables the gesture on exactly the nodes that already have their
	// artifact (or still lack their companion).
	UnavailableAt []string `json:"unavailable_at"`
}

// SDDCountsDTO aggregates the model for badges and filters.
type SDDCountsDTO struct {
	Documents      int            `json:"documents"`
	Findings       int            `json:"findings"`
	ByConcept      map[string]int `json:"by_concept"`
	FindingsByKind map[string]int `json:"findings_by_kind"`
}

// ---------------------------------------------------------------------------
// GetSpecsStatus
// ---------------------------------------------------------------------------

// GetSpecsStatus returns the framework-aware SDD status of a project for the
// Specs sidebar panel: which SDD frameworks are installed, which one is
// active, and the active framework's parsed tree plus its declared actions.
//
// The tree follows the ACTIVE SESSION's workspace (ADR-116, ADR-099's
// protocol), while the checkout-local operator DSL file is always read from
// the project checkout. A missing/non-directory root parses to an empty tree
// (`tree_present=false`), never an error; an unknown project or an
// uninitialized subsystem is a transport-level Go error. No Project returns
// the neutral `present=false` DTO (the defensive contract — the workspace tab
// strip, and with it the Specs panel, is not rendered in CHAT mode at all).
func (f *FrontendAPI) GetSpecsStatus(projectID string) (*SpecsStatusDTO, error) {
	f.seedAcquire()
	projectID = strings.TrimSpace(projectID)
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
		return neutralSpecsStatusDTO(""), nil
	}

	workspace := f.sddWorkspace(proj)
	if workspace == "" {
		// A workspace-less project (WorkspacePath == "", but not No Project —
		// that guard already ran) has no tree to parse and no evidence to
		// probe. The two paths that would otherwise resolve against the
		// process CWD are dead ends by construction — ProjectSDDPath("")
		// returns "" and core/sdd.ParseTree rejects an empty root — and the
		// neutral shape keeps the surface hidden exactly like the research
		// and papers RPCs exit on WorkspacePath == "".
		return neutralSpecsStatusDTO(""), nil
	}
	det, load := f.detectSDD(proj, workspace)

	persisted := f.loadActiveSDD(projectID)

	dto := neutralSpecsStatusDTO(workspace)
	dto.Warnings = append(dto.Warnings, load.Warnings...)
	dto.Detected = sddFrameworkDTOs(det.Frameworks)
	dto.SelectorNeeded = sddSelectorNeeded(det, persisted)
	dto.Present = f.specsGateEnabled() && len(det.Frameworks) > 0

	active, ok := pickActiveSDD(det, persisted)
	if persisted != "" {
		if _, found := detectionByID(det, persisted); !found {
			// The selection persists per project while detection runs per
			// workspace, so a worktree-only choice or a framework since
			// uninstalled can stop being detected here. The fallback to the
			// detection default self-heals (the row comes back when the
			// framework returns), but the fallback is never INVISIBLE: the
			// panel names the stale selection instead of quietly showing a
			// different framework.
			if ok {
				dto.Warnings = append(dto.Warnings, fmt.Sprintf(
					"sdd: the persisted selection %q is not detected in the current workspace; showing the detection default %q",
					persisted, active.ID))
			} else {
				dto.Warnings = append(dto.Warnings, fmt.Sprintf(
					"sdd: the persisted selection %q is not detected in the current workspace", persisted))
			}
		}
	}
	if !ok {
		return dto, nil
	}
	activeDTO := sddFrameworkDTO(active)
	dto.ActiveID = active.ID
	dto.Active = &activeDTO

	tree, err := sdd.ParseTree(workspace, active.Framework)
	if err != nil {
		// The parser only fails on an unresolvable workspace path; fail soft
		// to the detected-but-unparsed shape (the parseResearchRootBestEffort
		// pattern) — detection stays useful, the tree stays empty.
		f.log().Debug("GetSpecsStatus: SDD tree not parseable",
			"workspace", workspace, "framework", active.ID, "error", err)
		dto.Warnings = append(dto.Warnings, fmt.Sprintf("sdd: cannot parse the workspace: %v", err))
		return dto, nil
	}
	// The actions the DTO reports are gated on the workspace's CALLABLE
	// skill catalog: a declared-but-absent skill stays visible as an
	// inert action (available=false), and an alias spelling resolves to
	// the name actually installed (spec-check vs vibespec-check).
	applySDDTree(dto, active.Framework, tree, f.callableSkillNames(workspace, sddCheckout(proj)))
	// Environment enrichment, strictly after the pure parse: stamp decision
	// documents with their git add date. Fail-soft by contract.
	f.enrichSpecsDocumentDates(f.ctx(), dto.Documents, workspace)
	return dto, nil
}

// detectSDD assembles the merged framework catalog, collects the detection
// evidence in workspace and runs the pure detector. The catalog reads the two
// operator DSL files: the global one from the agent dir and the checkout-local
// one from the project checkout (never the session tree).
func (f *FrontendAPI) detectSDD(p *project.ProjectInfo, workspace string) (sdd.DetectResult, sdd.LoadResult) {
	load := sdd.Load(config.SDDPath(f.agentDir), config.ProjectSDDPath(sddCheckout(p)))
	catalog := frameworksOf(load.Frameworks)
	det := sdd.DetectInstalled(load.Frameworks, f.sddEvidence(workspace, catalog))
	return det, load
}

// frameworksOf projects the provenance-carrying definitions onto the plain
// framework catalog the pure detector and parser take.
func frameworksOf(defs []sdd.Definition) []sdd.Framework {
	out := make([]sdd.Framework, len(defs))
	for i, def := range defs {
		out[i] = def.Framework
	}
	return out
}

// ---------------------------------------------------------------------------
// SetActiveSDD
// ---------------------------------------------------------------------------

// SetActiveSDD persists the operator's SDD framework selection for a project.
// A non-empty id must be DETECTED for that project (the surface only offers
// detected frameworks), otherwise the call fails; an empty id clears the
// selection, restoring the detection default. The selection is per project and
// survives app restart; it never affects another project.
func (f *FrontendAPI) SetActiveSDD(projectID, sddID string) error {
	f.seedAcquire()
	projectID = strings.TrimSpace(projectID)
	sddID = strings.TrimSpace(sddID)
	if projectID == "" {
		return errors.New("project_id is required")
	}
	if f.projectManager == nil || f.projStore == nil {
		return errors.New("project subsystem not initialized")
	}

	proj, err := f.projectManager.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("failed to load project %q: %w", projectID, err)
	}
	if proj == nil {
		return fmt.Errorf("project %q not found", projectID)
	}
	if proj.IsNoProject {
		return errors.New("the No Project pseudo-project has no SDD tree")
	}

	if sddID != "" {
		// The selector offers what GetSpecsStatus detects in the session
		// workspace (the tree it renders), while the selection persists per
		// project and must survive session switches. A choice is legitimate
		// when EITHER root detects it: the project checkout (the persistence
		// scope) or the active session workspace (the offer set) — a
		// framework whose init ran inside the session worktree must not be
		// offered and then refused. A worktree-only selection self-heals
		// later: pickActiveSDD falls back to the detection default when the
		// persisted id stops being detected anywhere.
		det, _ := f.detectSDD(proj, sddCheckout(proj))
		if _, ok := detectionByID(det, sddID); !ok {
			if ws := f.sddWorkspace(proj); ws != "" && ws != sddCheckout(proj) {
				det, _ = f.detectSDD(proj, ws)
			}
			if _, ok := detectionByID(det, sddID); !ok {
				return fmt.Errorf("SDD framework %q is not detected for project %q", sddID, projectID)
			}
		}
	}

	ctx, cancel := f.storeOpCtx()
	defer cancel()
	if err := f.projStore.SaveActiveSDD(ctx, projectID, sddID); err != nil {
		return fmt.Errorf("failed to save active SDD: %w", err)
	}
	return nil
}

// detectionByID finds one recognised framework by id.
func detectionByID(det sdd.DetectResult, id string) (sdd.Detection, bool) {
	for _, d := range det.Frameworks {
		if d.ID == id {
			return d, true
		}
	}
	return sdd.Detection{}, false
}

// pickActiveSDD resolves the active framework: the persisted selection when it
// is still detected, else detection's default. ok=false when nothing is
// detected (the surface is hidden).
func pickActiveSDD(det sdd.DetectResult, persisted string) (sdd.Detection, bool) {
	if persisted != "" {
		if d, ok := detectionByID(det, persisted); ok {
			return d, true
		}
	}
	if det.Recognized {
		return det.Active, true
	}
	return sdd.Detection{}, false
}

// sddSelectorNeeded reports that the operator still has to choose: more than
// one framework is detected and the persisted selection does not name one of
// them.
func sddSelectorNeeded(det sdd.DetectResult, persisted string) bool {
	if len(det.Frameworks) <= 1 {
		return false
	}
	_, ok := detectionByID(det, persisted)
	return !ok
}

// loadActiveSDD reads the persisted selection, fail-soft: a store error leaves
// the detection default in charge and is debug-logged only (the selection is a
// UI preference, not an authority).
func (f *FrontendAPI) loadActiveSDD(projectID string) string {
	if f.projStore == nil {
		return ""
	}
	ctx, cancel := f.storeOpCtx()
	defer cancel()
	id, err := f.projStore.LoadActiveSDD(ctx, projectID)
	if err != nil {
		f.log().Debug("specs panel: active SDD selection unavailable", "error", err)
		return ""
	}
	return id
}

// specsGateEnabled resolves the sidebar gate (specs.enabled; unset means on).
func (f *FrontendAPI) specsGateEnabled() bool {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil {
		return true
	}
	return f.config.Specs.PackEnabled()
}

// ---------------------------------------------------------------------------
// detection evidence
// ---------------------------------------------------------------------------

// sddEvidence collects the facts the pure detector runs on: the installed
// skills (with their discovery tier), the declared markers that exist, and the
// declared roots that exist. Every declared path is workspace-relative and is
// resolved and containment-checked here — core/sdd never reads a path.
func (f *FrontendAPI) sddEvidence(workspace string, catalog []sdd.Framework) sdd.Evidence {
	markers := make([]string, 0)
	roots := make([]string, 0)
	for _, fw := range catalog {
		markers = append(markers, fw.Detect.Markers...)
		for _, root := range fw.Roots {
			roots = append(roots, root.Path)
		}
	}
	return sdd.Evidence{
		Skills:  f.installedSDDSkills(workspace, catalog),
		Markers: presentDeclaredPaths(workspace, markers, false),
		Roots:   presentDeclaredPaths(workspace, roots, true),
	}
}

// sddSkillDir is one discovery directory with the tier it represents.
type sddSkillDir struct {
	path  string
	scope sdd.SkillScope
}

// sddSkillDirs assembles the discovery set with tiers, highest first: the
// workspace's own .agents/skills and every framework-declared project
// directory, then the shared base directories from config. A declared path
// that resolves outside the workspace is classified by where it lands
// (the c0wrk global dir is global, anything else is user).
func (f *FrontendAPI) sddSkillDirs(workspace string, catalog []sdd.Framework) []sddSkillDir {
	globalDir := ""
	if f.agentDir != "" {
		globalDir = config.SkillsDir(f.agentDir)
	}

	candidates := make([]string, 0, 4+len(catalog))
	if workspace != "" {
		candidates = append(candidates, filepath.Join(workspace, core.SkillsRelativePath))
		for _, fw := range catalog {
			for _, declared := range fw.Detect.Skills.Dirs {
				if abs, ok := resolveDeclaredPath(workspace, declared); ok {
					candidates = append(candidates, abs)
				}
			}
		}
	}
	candidates = append(candidates, f.baseSkillDirs()...)

	dirs := make([]sddSkillDir, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		clean := filepath.Clean(candidate)
		if clean == "" || seen[clean] {
			continue
		}
		seen[clean] = true
		dirs = append(dirs, sddSkillDir{
			path:  clean,
			scope: classifySkillDir(workspace, clean, globalDir),
		})
	}
	return dirs
}

// baseSkillDirs resolves the configured shared discovery directories. A
// configured slice is the operator's decision — INCLUDING an explicitly empty
// one ("no shared dirs"), which is kept empty: silently re-expanding it to the
// c0wrk global dir would make detection see skills that activation (the same
// empty set handed to the builder) cannot call. Only the unconfigured state —
// no config at all, as in a bare test FrontendAPI — falls back to the global
// skills dir.
func (f *FrontendAPI) baseSkillDirs() []string {
	f.configMu.RLock()
	var configured []string
	configuredSet := false
	if f.config != nil {
		configured = f.config.Skills.Dirs
		configuredSet = f.config.Skills.Dirs != nil
	}
	f.configMu.RUnlock()
	if configuredSet {
		return resolveCatalogDirs(configured, f.agentDir, config.ExpandEnvVars, f.log())
	}
	if f.agentDir == "" {
		return nil
	}
	return []string{config.SkillsDir(f.agentDir)}
}

// classifySkillDir assigns a discovery tier: inside the workspace is project,
// the c0wrk global dir is global, everything else is user.
func classifySkillDir(workspace, dir, globalDir string) sdd.SkillScope {
	if workspace != "" {
		if ok, err := pathutil.IsWithinPath(workspace, dir); err == nil && ok {
			return sdd.ScopeProject
		}
	}
	if globalDir != "" && samePath(globalDir, dir) {
		return sdd.ScopeGlobal
	}
	return sdd.ScopeUser
}

// samePath reports whether two paths denote the same location, resolving
// symlinks through their existing prefixes (macOS /var → /private/var).
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ab, err1 := pathutil.IsWithinPath(a, b)
	ba, err2 := pathutil.IsWithinPath(b, a)
	return err1 == nil && err2 == nil && ab && ba
}

// installedSDDSkills enumerates the skills installed in the discovery set: one
// entry per skill the SDK's own parser accepts (the same ParseSkill the
// callable SkillManager runs — a directory carrying a malformed, description-less
// or name-mismatched SKILL.md is NOT an installed skill, or detection would
// recognize a framework whose skills can never activate), tagged with the tier
// of the directory it was found in. A validated higher-tier copy wins over an
// invalid one by construction: the invalid document never yields evidence.
func (f *FrontendAPI) installedSDDSkills(workspace string, catalog []sdd.Framework) []sdd.InstalledSkill {
	dirs := f.sddSkillDirs(workspace, catalog)
	var installed []sdd.InstalledSkill
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir.path)
		if err != nil {
			continue // absent/unreadable directory: no evidence, not an error
		}
		for _, entry := range entries {
			if !isDirectoryEntry(dir.path, entry) {
				continue
			}
			skillDir := filepath.Join(dir.path, entry.Name())
			skillMD := filepath.Join(skillDir, "SKILL.md")
			if info, err := os.Stat(skillMD); err != nil || info.IsDir() {
				continue
			}
			parsed, err := skills.ParseSkill(skillMD, skillDir)
			if err != nil {
				f.log().Debug("installedSDDSkills: skipping invalid skill document",
					"dir", skillDir, "error", err)
				continue
			}
			installed = append(installed, sdd.InstalledSkill{
				Name:  parsed.Metadata.Name,
				Dir:   skillDir,
				Scope: dir.scope,
			})
		}
	}
	return installed
}

// isDirectoryEntry reports whether an entry is a directory, following a
// symlink (a skill directory is often linked into place).
func isDirectoryEntry(dir string, entry fs.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&fs.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, entry.Name()))
	return err == nil && info.IsDir()
}

// presentDeclaredPaths returns the declared paths that exist in workspace, in
// the DECLARED spelling (the form core/sdd matches against). dirOnly restricts
// the probe to directories (roots); markers accept a file or a directory.
func presentDeclaredPaths(workspace string, declared []string, dirOnly bool) []string {
	present := make([]string, 0, len(declared))
	seen := make(map[string]bool, len(declared))
	for _, entry := range declared {
		if seen[entry] {
			continue
		}
		seen[entry] = true
		abs, ok := resolveDeclaredPath(workspace, entry)
		if !ok {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if dirOnly && !info.IsDir() {
			continue
		}
		present = append(present, entry)
	}
	return present
}

// resolveDeclaredPath resolves one DSL-declared path against the workspace.
// Only clean workspace-relative spellings resolve — the same rule the DSL
// validator enforces at load (`~`-relative, absolute and `..`-carrying
// declarations are rejected there), so this probe repeats the check fail-closed
// against any future call site. Rootedness is lexical across every dialect
// (core.IsAbsolutePath): filepath.IsAbs is false for "/etc/specs" on Windows,
// which would let a rooted declaration resolve INSIDE the workspace there. A
// framework document never widens a probe outside the directories it names.
func resolveDeclaredPath(workspace, declared string) (string, bool) {
	entry := strings.TrimSpace(strings.ReplaceAll(declared, `\`, "/"))
	if entry == "" || entry == "~" || strings.HasPrefix(entry, "~/") || core.IsAbsolutePath(entry) {
		return "", false
	}
	for _, segment := range strings.Split(entry, "/") {
		if segment == ".." {
			return "", false
		}
	}
	if workspace == "" {
		return "", false
	}
	abs := filepath.Join(workspace, filepath.FromSlash(entry))
	if ok, err := pathutil.IsWithinPath(workspace, abs); err != nil || !ok {
		return "", false
	}
	return abs, true
}

// ---------------------------------------------------------------------------
// model → DTO mapping
// ---------------------------------------------------------------------------

// neutralSpecsStatusDTO returns the fully allocated empty shape.
func neutralSpecsStatusDTO(workspace string) *SpecsStatusDTO {
	return &SpecsStatusDTO{
		Workspace: workspace,
		Detected:  []SDDFrameworkDTO{},
		Roots:     []SDDRootDTO{},
		Documents: []SDDDocumentDTO{},
		Nodes:     []SDDNodeDTO{},
		Views:     []SDDViewDTO{},
		Findings:  []SDDFindingDTO{},
		Actions:   []SDDActionDTO{},
		Warnings:  []string{},
		Counts: SDDCountsDTO{
			ByConcept:      map[string]int{},
			FindingsByKind: map[string]int{},
		},
	}
}

// sddFrameworkDTOs maps a detection result's frameworks.
func sddFrameworkDTOs(detections []sdd.Detection) []SDDFrameworkDTO {
	out := make([]SDDFrameworkDTO, 0, len(detections))
	for _, d := range detections {
		out = append(out, sddFrameworkDTO(d))
	}
	return out
}

// sddFrameworkDTO maps one detection record.
func sddFrameworkDTO(d sdd.Detection) SDDFrameworkDTO {
	matched := make([]SDDMatchedSkillDTO, 0, len(d.Skills))
	for _, skill := range d.Skills {
		matched = append(matched, SDDMatchedSkillDTO{
			Name:  skill.Name,
			Dir:   skill.Dir,
			Scope: string(skill.Scope),
		})
	}
	return SDDFrameworkDTO{
		ID:            d.ID,
		Label:         d.Label,
		Description:   d.Description,
		DocsURL:       d.DocsURL,
		Origin:        string(d.Origin),
		Scope:         string(d.Scope),
		TreePresent:   d.TreePresent(),
		RootPresent:   d.RootPresent,
		MarkerPresent: d.MarkerPresent,
		Skills:        matched,
	}
}

// applySDDTree fills the tree-driven fields of dto from a parsed tree.
// callableSkillNames collects the skill names callable in one workspace
// (the same ordered discovery set activation uses), under their INSTALLED
// spellings; the discovery catalog is loaded for the project checkout
// (sddCheckout) exactly like activation's. Declared alias names are mapped
// onto these raw spellings by resolveActionSkill, which needs the installed
// name a dispatch can actually invoke. nil means the catalog could not be
// determined (no builder — bare tests), in which case skill availability is
// not gated.
func (f *FrontendAPI) callableSkillNames(workspace, sddCheckoutPath string) map[string]bool {
	b := f.builder()
	if b == nil {
		return nil
	}
	dirs := b.SkillDirsForWorkspace(workspace, sddCheckoutPath)
	if len(dirs) == 0 {
		return map[string]bool{}
	}
	out := make(map[string]bool)
	for _, d := range b.GetSkillDescriptors(dirs) {
		out[d.Name] = true
	}
	return out
}

func applySDDTree(dto *SpecsStatusDTO, fw sdd.Framework, tree *sdd.Tree, callable map[string]bool) {
	if tree == nil {
		return
	}
	dto.TreePresent = tree.Present
	dto.Root = activeRootPath(dto.Workspace, fw, tree)

	dto.Roots = make([]SDDRootDTO, 0, len(tree.Roots))
	for _, root := range tree.Roots {
		dto.Roots = append(dto.Roots, SDDRootDTO{
			ID:       root.ID,
			Path:     root.Path,
			Label:    root.Label,
			Required: root.Required,
			Present:  root.Present,
		})
	}

	dto.Documents = make([]SDDDocumentDTO, 0, len(tree.Documents))
	for _, doc := range tree.Documents {
		dto.Documents = append(dto.Documents, sddDocumentDTO(doc))
	}

	dto.Nodes = make([]SDDNodeDTO, 0, len(tree.Nodes))
	for _, node := range tree.Nodes {
		dto.Nodes = append(dto.Nodes, SDDNodeDTO{
			ID:       node.ID,
			Kind:     node.Kind,
			Path:     node.Path,
			Label:    node.Label,
			Concept:  node.Concept,
			Name:     node.Name,
			Parent:   node.Parent,
			Children: nonNilStrings(node.Children),
			Missing:  nonNilStrings(node.Missing),
		})
	}

	dto.Views = make([]SDDViewDTO, 0, len(tree.Views))
	for _, view := range tree.Views {
		dto.Views = append(dto.Views, SDDViewDTO{
			ID:    view.ID,
			Label: view.Label,
			Kind:  view.Kind,
			Nodes: nonNilStrings(view.Nodes),
			Paths: nonNilStrings(view.Members),
		})
	}

	dto.Findings = make([]SDDFindingDTO, 0, len(tree.Findings))
	for _, finding := range tree.Findings {
		dto.Findings = append(dto.Findings, SDDFindingDTO{
			Kind:     finding.Kind,
			File:     finding.File,
			Detail:   finding.Detail,
			Message:  finding.Message,
			Severity: finding.Severity,
		})
	}

	dto.Actions = make([]SDDActionDTO, 0, len(fw.Actions))
	for _, action := range fw.Actions {
		resolved, callableOK := resolveActionSkill(action.Skill, callable)
		dto.Actions = append(dto.Actions, SDDActionDTO{
			Kind:           action.Kind,
			Label:          action.Label,
			Skill:          resolved,
			Prompt:         action.Prompt,
			Concept:        action.Concept,
			Requires:       nonNilStrings(action.Requires),
			Available:      actionAvailable(action, tree) && callableOK,
			SkillAvailable: callableOK,
			UnavailableAt:  nonNilStrings(actionBlockedNodes(action, tree)),
		})
	}

	dto.Counts = SDDCountsDTO{
		Documents:      len(dto.Documents),
		Findings:       len(dto.Findings),
		ByConcept:      map[string]int{},
		FindingsByKind: map[string]int{},
	}
	for concept, count := range tree.Counts.ByConcept {
		dto.Counts.ByConcept[concept] = count
	}
	for kind, count := range tree.Counts.FindingsByKind {
		dto.Counts.FindingsByKind[kind] = count
	}

	dto.Warnings = append(dto.Warnings, tree.Warnings...)
}

// activeRootPath returns the absolute path of the active framework's root to
// report: the first PRESENT declared root, else the first declared one.
func activeRootPath(workspace string, fw sdd.Framework, tree *sdd.Tree) string {
	if workspace == "" || len(fw.Roots) == 0 {
		return ""
	}
	for _, root := range tree.Roots {
		if root.Present {
			return sddRootPath(workspace, root.Path)
		}
	}
	return sddRootPath(workspace, fw.Roots[0].Path)
}

// actionAvailable reports whether every declared action requirement holds for
// the tree: "root", "findings", "concept:<kind>", "companion:<kind>" or
// "absent:<kind>".
func actionAvailable(action sdd.Action, tree *sdd.Tree) bool {
	for _, requirement := range action.Requires {
		if !requirementMet(action, requirement, tree) {
			return false
		}
	}
	return true
}

// requirementMet evaluates one action requirement token. The concept-shaped
// tokens are TREE-level gates (could this gesture apply anywhere in this tree?);
// the per-instance half — which feature directory already has its plan.md — is
// answered by actionBlockedNodes below, which reports the nodes where it fails.
func requirementMet(action sdd.Action, requirement string, tree *sdd.Tree) bool {
	switch {
	case requirement == "root":
		return tree.Present
	case requirement == "findings":
		return len(tree.Findings) > 0
	case strings.HasPrefix(requirement, "concept:"):
		kind := strings.TrimPrefix(requirement, "concept:")
		// Counts.ByConcept counts every concept instance, directory concepts
		// (an OpenSpec change folder, a Spec Kit feature) included — a
		// document-only lookup would report such an action unavailable on a
		// perfectly populated tree.
		return tree.Counts.ByConcept[kind] > 0
	case strings.HasPrefix(requirement, "companion:"):
		// At least one instance of the kind exists somewhere in the tree —
		// the companion relation the action builds on is present.
		return tree.Counts.ByConcept[strings.TrimPrefix(requirement, "companion:")] > 0
	case strings.HasPrefix(requirement, "absent:"):
		// The output artifact the action creates is missing from at least one
		// holder: fewer <kind> instances than the action's holder concept has
		// means some directory still needs its artifact. Without a holder
		// concept the gate is meaningless and fails closed.
		kind := strings.TrimPrefix(requirement, "absent:")
		if action.Concept == "" {
			return false
		}
		return tree.Counts.ByConcept[kind] < tree.Counts.ByConcept[action.Concept]
	default:
		// Unknown requirement tokens fail closed: a declared gate the backend
		// cannot evaluate must not be reported as satisfied.
		return false
	}
}

// nodeGate is one per-node requirement token, decomposed.
type nodeGate struct {
	kind   string
	absent bool // absent:K must NOT find an artifact; companion:K must find one
}

// actionBlockedNodes reports the node paths at which a declared per-node gate
// fails, so the holder concept's own nodes can render the gesture disabled one
// by one: a Plan button disappears on the feature that already has plan.md
// while staying live on the feature that does not. Nodes of other concepts and
// actions without such a gate report nothing.
func actionBlockedNodes(action sdd.Action, tree *sdd.Tree) []string {
	if tree == nil || action.Concept == "" {
		return nil
	}
	gates := make([]nodeGate, 0, len(action.Requires))
	for _, requirement := range action.Requires {
		switch {
		case strings.HasPrefix(requirement, "companion:"):
			gates = append(gates, nodeGate{kind: strings.TrimPrefix(requirement, "companion:")})
		case strings.HasPrefix(requirement, "absent:"):
			gates = append(gates, nodeGate{kind: strings.TrimPrefix(requirement, "absent:"), absent: true})
		}
	}
	if len(gates) == 0 {
		return nil
	}
	instances := conceptInstancePaths(tree)
	var blocked []string
	for _, node := range tree.Nodes {
		if node.Concept != action.Concept {
			continue
		}
		for _, gate := range gates {
			// An absent-gate fails on a node that HAS the artifact; a
			// companion-gate fails on a node that lacks it.
			if artifactAttached(instances[gate.kind], node.Path) == gate.absent {
				blocked = append(blocked, node.Path)
				break
			}
		}
	}
	return blocked
}

// conceptInstancePaths groups every instance path by concept kind, directory
// instances included — the same population Counts.ByConcept counts.
func conceptInstancePaths(tree *sdd.Tree) map[string][]string {
	byConcept := make(map[string][]string)
	for _, node := range tree.Nodes {
		if node.Concept == "" {
			continue
		}
		byConcept[node.Concept] = append(byConcept[node.Concept], node.Path)
	}
	return byConcept
}

// artifactAttached reports whether any instance of the kind belongs to the
// holder node: beside it in the same directory (a `*/plan.md` sibling of
// `*/spec.md`) or below it (a `changes/x/proposal.md` child of the `changes/x`
// change folder). Both are the shapes the DSL's concepts match.
func artifactAttached(paths []string, holder string) bool {
	holderDir := path.Dir(holder)
	prefix := holder + "/"
	for _, p := range paths {
		if p == holder || strings.HasPrefix(p, prefix) || path.Dir(p) == holderDir {
			return true
		}
	}
	return false
}

// sddDocumentDTO maps one parsed document.
func sddDocumentDTO(doc sdd.Document) SDDDocumentDTO {
	taskItems := make([]SDDTaskItemDTO, 0, len(doc.TaskItems))
	for _, item := range doc.TaskItems {
		taskItems = append(taskItems, SDDTaskItemDTO{Text: item.Text, Done: item.Done})
	}
	tasks := make([]SDDIndexTaskDTO, 0, len(doc.Tasks))
	for _, task := range doc.Tasks {
		tasks = append(tasks, SDDIndexTaskDTO{Task: task.Task, Specs: nonNilStrings(task.Specs)})
	}
	return SDDDocumentDTO{
		Path:         doc.Path,
		Root:         doc.Root,
		Concept:      doc.Concept,
		Label:        doc.Label,
		Section:      doc.Section,
		Name:         doc.Name,
		Number:       doc.Number,
		Slug:         doc.Slug,
		Title:        doc.Title,
		DisplayTitle: doc.DisplayTitle,
		Headings:     nonNilStrings(doc.Headings),
		KeyFiles:     nonNilStrings(doc.KeyFiles),
		CrossRefs:    nonNilStrings(doc.CrossRefs),
		Frontmatter:  doc.Frontmatter,
		Status:       doc.Status,
		SupersededBy: doc.SupersededBy,
		Purpose:      doc.Purpose,
		Requirements: nonNilStrings(doc.Requirements),
		Scenarios:    nonNilStrings(doc.Scenarios),
		Deltas:       nonNilStrings(doc.Deltas),
		Principles:   nonNilStrings(doc.Principles),
		UserStories:  nonNilStrings(doc.UserStories),
		TaskItems:    taskItems,
		Tasks:        tasks,
		Missing:      nonNilStrings(doc.Missing),
	}
}

// nonNilStrings returns a non-nil copy so the wire is always an array.
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// resolveActionSkill maps an action's declared skill onto the name actually
// callable in the workspace: the exact name when installed, the alias spelling
// when only the upstream copy exists (vibespec-check for a declared
// spec-check), or the declared name flagged not-callable so the action stays
// visible but inert. A skill-less action is not gated (its inertness is the
// dispatch layer's decision), and an undeterminable catalog (nil) does not
// gate either.
func resolveActionSkill(skill string, callable map[string]bool) (string, bool) {
	if skill == "" || callable == nil {
		return skill, true
	}
	if callable[skill] {
		return skill, true
	}
	// Sorted iteration keeps the resolved spelling stable when several
	// installed spellings fold to one declared skill's canonical name.
	for _, name := range slices.Sorted(maps.Keys(callable)) {
		if sdd.SkillNameMatches(skill, name) {
			return name, true
		}
	}
	return skill, false
}
