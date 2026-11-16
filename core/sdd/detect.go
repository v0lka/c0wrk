package sdd

import (
	"path"
	"sort"
	"strings"
)

// Detection — deciding which SDD framework a workspace has installed.
//
// Detection is deliberately I/O-free: the caller has already discovered the
// installed skills (one entry per skill directory, tagged with the discovery
// tier it came from), probed the project's marker files and checked which
// framework roots exist. This file turns those facts into two answers — which
// frameworks the project has installed, and which one is the default.
//
// Visibility is decided by the skill evidence alone (D8): a framework appears
// when its declared skill set is satisfied. Markers and roots never make a
// framework visible; they are the tree signal, read only by the default
// selection and by a surface's empty state.

// SkillScope is the discovery tier an installed skill came from, ordered by
// precedence: project > c0wrk global > user. The caller classifies a skill
// directory — core/sdd knows no filesystem layout.
type SkillScope string

const (
	// ScopeProject is a skill directory inside the checkout: the project's own
	// .agents/skills, or an extra directory an SDD framework declares there
	// (e.g. .claude/skills).
	ScopeProject SkillScope = "project"
	// ScopeGlobal is c0wrk's own global skill directory.
	ScopeGlobal SkillScope = "global"
	// ScopeUser is the user's shared agent skill directory.
	ScopeUser SkillScope = "user"
	// ScopeUnknown is a skill the caller could not attribute to a tier. It ranks
	// last so an unattributed copy never outranks a classified one.
	ScopeUnknown SkillScope = ""
)

// rank orders scopes for default selection: a lower rank is stronger. An
// unrecognised scope value is treated as ScopeUnknown.
func (s SkillScope) rank() int {
	switch s {
	case ScopeProject:
		return 0
	case ScopeGlobal:
		return 1
	case ScopeUser:
		return 2
	default:
		return 3
	}
}

// InstalledSkill is one skill the caller discovered: the skill directory name,
// the directory itself (reported back on a match, never read here) and the tier
// that directory belongs to.
type InstalledSkill struct {
	Name  string     // skill directory name, e.g. "openspec-propose"
	Dir   string     // skill directory; informational
	Scope SkillScope // discovery tier the directory belongs to
}

// MatchedSkill is one installed skill that satisfied a framework's declared
// skill set, with the provenance a surface needs to show where it was found.
type MatchedSkill struct {
	Name  string
	Dir   string
	Scope SkillScope
}

// Evidence is the project facts detection runs on. Every path is in the form
// the framework DSL declares it: workspace-relative and slash separated.
type Evidence struct {
	// Skills is the installed skill catalog — one entry per discovered skill
	// directory, in any tier. The same name may appear more than once; the
	// strongest tier wins.
	Skills []InstalledSkill
	// Markers lists the framework marker paths that exist in the project.
	Markers []string
	// Roots lists the framework root paths that exist in the project.
	Roots []string
}

// Detection is one recognised framework with the evidence that recognised it.
type Detection struct {
	Definition
	// Skills are the installed skills that satisfied the declared set, strongest
	// tier first. Exactly one entry per skill name.
	Skills []MatchedSkill
	// Scope is the strongest tier among Skills.
	Scope SkillScope
	// RootPresent reports that at least one declared root exists in the project.
	RootPresent bool
	// MarkerPresent reports that at least one declared marker exists in the
	// project.
	MarkerPresent bool
}

// TreePresent reports whether the framework looks initialised in the project: a
// declared root or marker exists. Only the default-selection tie-break and a
// surface's empty state read this signal — never visibility (D8).
func (d Detection) TreePresent() bool {
	return d.RootPresent || d.MarkerPresent
}

// DetectResult is the outcome of DetectInstalled.
type DetectResult struct {
	// Frameworks are the recognised frameworks in default-selection order, best
	// default first. An empty slice means no framework is installed, which hides
	// the SDD surface entirely (D8).
	Frameworks []Detection
	// Active is the default selection; it equals Frameworks[0] whenever
	// Recognized is true and is the zero value otherwise.
	Active Detection
	// Recognized reports whether any framework was recognised at all.
	Recognized bool
}

// DetectInstalled recognises the frameworks in catalog that the project has
// installed and picks the default one. catalog is the merged framework catalog
// in declaration order — what Merge and Builtins deliver (Priority, then ID).
//
// Recognition is decided by the framework's declared skill evidence alone:
// every all_of name must be installed and, when any_of is present, at least one
// of its names must be too. A framework declaring neither list never reaches
// this matcher — the loader rejects it, because it could never be recognized
// even when its markers exist (visibility must not depend on the tree, D8).
// Installed names are compared by exact, case-sensitive name after trimming
// surrounding whitespace: a framework is recognised by the spelling its own
// installer writes, and no prefix folding applies.
//
// The default is the first framework in this order (D9): tree present first,
// then the strongest skill scope (project > c0wrk global > user), then the
// catalog's own declaration order. Detection is pure: catalog and evidence are
// the caller's already-collected facts, and no path is read — the DSL's declared
// skill directories are not consulted either, because the caller expands them
// into the catalog before calling.
func DetectInstalled(catalog []Definition, evidence Evidence) DetectResult {
	installed := newInstalledIndex(evidence.Skills)
	roots := pathSet(evidence.Roots)
	markers := pathSet(evidence.Markers)

	recognized := make([]Detection, 0, len(catalog))
	for _, def := range catalog {
		skills, scope, ok := installed.match(def.Detect.Skills)
		if !ok {
			continue
		}
		recognized = append(recognized, Detection{
			Definition:    def,
			Skills:        skills,
			Scope:         scope,
			RootPresent:   def.hasAnyRoot(roots),
			MarkerPresent: def.hasAnyMarker(markers),
		})
	}
	// A stable sort keeps the catalog's declaration order for frameworks that tie
	// on both the tree signal and their skill scope.
	sort.SliceStable(recognized, func(i, j int) bool {
		if a, b := recognized[i].TreePresent(), recognized[j].TreePresent(); a != b {
			return a
		}
		return recognized[i].Scope.rank() < recognized[j].Scope.rank()
	})

	result := DetectResult{Frameworks: recognized, Recognized: len(recognized) > 0}
	if result.Recognized {
		result.Active = recognized[0]
	}
	return result
}

// installedIndex resolves an installed skill name to the strongest copy when the
// same skill exists in several tiers. Keys are the exact skill names, so two
// spellings of a similar name are two distinct skills.
type installedIndex map[string]InstalledSkill

// newInstalledIndex indexes the catalog by exact skill name, keeping the
// strongest tier on a collision (the first entry wins a tie, so the caller's
// order stays meaningful).
func newInstalledIndex(skills []InstalledSkill) installedIndex {
	index := make(installedIndex, len(skills))
	for _, skill := range skills {
		skill.Name = strings.TrimSpace(skill.Name)
		if skill.Name == "" {
			continue
		}
		if existing, ok := index[skill.Name]; ok && existing.Scope.rank() <= skill.Scope.rank() {
			continue
		}
		index[skill.Name] = skill
	}
	return index
}

// match evaluates a framework's declared skill set: every all_of name must be
// installed and, when any_of is present, at least one of its names must be too.
// A framework that declares neither list has no skill evidence and never
// matches. On success the returned skills are the installed skills that
// satisfied the set, strongest tier first, and the returned scope is the
// strongest tier among them.
func (index installedIndex) match(skills Skills) ([]MatchedSkill, SkillScope, bool) {
	if len(skills.AllOf) == 0 && len(skills.AnyOf) == 0 {
		return nil, ScopeUnknown, false
	}

	matched := make([]MatchedSkill, 0, len(skills.AllOf)+len(skills.AnyOf))
	seen := make(map[string]bool, cap(matched))
	best := ScopeUnknown
	collect := func(required string) bool {
		skill, ok := index[strings.TrimSpace(required)]
		if !ok {
			return false
		}
		if !seen[skill.Name] {
			seen[skill.Name] = true
			matched = append(matched, MatchedSkill(skill))
		}
		if best == ScopeUnknown || skill.Scope.rank() < best.rank() {
			best = skill.Scope
		}
		return true
	}

	for _, required := range skills.AllOf {
		if !collect(required) {
			return nil, ScopeUnknown, false
		}
	}
	if len(skills.AnyOf) > 0 {
		found := false
		for _, required := range skills.AnyOf {
			if collect(required) {
				found = true
			}
		}
		if !found {
			return nil, ScopeUnknown, false
		}
	}
	if len(matched) == 0 {
		return nil, ScopeUnknown, false
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].Scope.rank() < matched[j].Scope.rank()
	})
	return matched, best, true
}

// hasAnyRoot reports whether one of the framework's declared roots is present.
func (f Framework) hasAnyRoot(present map[string]bool) bool {
	return anyPathPresent(present, f.rootPaths())
}

// hasAnyMarker reports whether one of the framework's declared markers is
// present.
func (f Framework) hasAnyMarker(present map[string]bool) bool {
	return anyPathPresent(present, f.Detect.Markers)
}

// rootPaths returns the framework's declared root paths.
func (f Framework) rootPaths() []string {
	paths := make([]string, 0, len(f.Roots))
	for _, root := range f.Roots {
		paths = append(paths, root.Path)
	}
	return paths
}

// anyPathPresent reports whether any of paths is in the normalised present set.
func anyPathPresent(present map[string]bool, paths []string) bool {
	if len(present) == 0 {
		return false
	}
	for _, candidate := range paths {
		key, ok := normalizeRelativePath(candidate)
		if ok && present[key] {
			return true
		}
	}
	return false
}

// pathSet normalises a list of declared paths into a set; entries that are empty
// or not clean workspace-relative paths are dropped.
func pathSet(paths []string) map[string]bool {
	if len(paths) == 0 {
		return nil
	}
	set := make(map[string]bool, len(paths))
	for _, candidate := range paths {
		if key, ok := normalizeRelativePath(candidate); ok {
			set[key] = true
		}
	}
	return set
}

// normalizeRelativePath puts a declared path into the canonical form the sets
// share: backslashes become slashes and the result is cleaned. Empty, absolute
// and parent-escaping paths are rejected, so a malformed probe path never
// matches a framework declaration by accident. The workspace root itself (".")
// is KEPT: the loader accepts it as a clean relative root and the reader parses
// it as a present subtree, so rejecting it here would make the same declaration
// present to the parser and absent to the ranking.
func normalizeRelativePath(candidate string) (string, bool) {
	candidate = strings.TrimSpace(strings.ReplaceAll(candidate, `\`, "/"))
	if candidate == "" {
		return "", false
	}
	cleaned := path.Clean(candidate)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	return cleaned, true
}
