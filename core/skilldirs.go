package core

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/v0lka/c0wrk/core/sdd"
	"github.com/v0lka/sp4rk/pathutil"
)

// Skill discovery is a SET of directories, not one:
//
//	<ws>/.agents/skills            project-local, user-owned (highest priority)
//	<ws>/<framework skill dirs>    declared by the detected SDD framework, e.g.
//	                               .claude/skills — installed by the framework
//	                               and shared with other agent tools
//	<base dirs>                    SetSkillDirs: ~/.c0wrk/.agents/skills,
//	                               ~/.agents/skills (lowest priority)
//
// The framework-declared tier is what makes a skill installed OUTSIDE
// .agents/skills both visible (ListSkills / GetSkillDescriptors) and callable
// (the per-session SkillManager behind an activeSkills/`/skill` mention). The
// directories are per-workspace, so the whole set is recomputed per session and
// per active project rather than registered once at startup.

// sddFrameworkCatalog caches the parsed built-in SDD framework documents. The
// documents are embedded and parsing is pure, so one process-wide parse
// suffices; a failure is a build defect (pinned by core/sdd's own tests) and is
// reported through the caller's logger on every call instead of being cached as
// a silently empty discovery tier.
var sddFrameworkCatalog struct {
	once       sync.Once
	frameworks []sdd.Framework
	err        error
}

// builtinSDDFrameworks returns the embedded frameworks ordered by priority
// (first = the framework a workspace is attributed to when several match).
func builtinSDDFrameworks() ([]sdd.Framework, error) {
	sddFrameworkCatalog.once.Do(func() {
		sddFrameworkCatalog.frameworks, sddFrameworkCatalog.err = sdd.Builtins()
	})
	return sddFrameworkCatalog.frameworks, sddFrameworkCatalog.err
}

// SetSDDCatalog wires the framework-catalog provider skill discovery uses:
// built-ins merged with the operator's global + checkout-local DSL
// definitions, per project checkout. The argument is the checkout root whose
// specs.yaml merges in — repository data, deliberately never the session's
// execution workspace (a managed worktree). Nil provider (the zero value)
// falls back to the built-in catalog.
func (b *OrchestratorBuilder) SetSDDCatalog(provider func(checkoutPath string) []sdd.Framework) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sddCatalog = provider
}

// sddCatalogFor returns the catalog for one project checkout: the wired
// provider, or the built-in catalog alone.
func (b *OrchestratorBuilder) sddCatalogFor(checkoutPath string, logger *slog.Logger) []sdd.Framework {
	b.mu.RLock()
	provider := b.sddCatalog
	b.mu.RUnlock()
	if provider != nil {
		return provider(checkoutPath)
	}
	frameworks, err := builtinSDDFrameworks()
	if err != nil {
		if logger != nil {
			logger.Warn("SDD framework catalog unavailable; framework skill directories are not scanned",
				"error", err)
		}
		return nil
	}
	return frameworks
}

// SkillDirsForWorkspace returns the complete ordered skill discovery set for one
// session workspace, highest priority first: the workspace's own
// .agents/skills, the extra project-local directories the detected SDD
// frameworks declare, and finally the shared base directories registered with
// SetSkillDirs. An empty workspace (CHAT mode) yields the base dirs alone.
// sddCheckoutPath is the project's repository checkout whose checkout-local
// SDD operator DSL merges into the detection catalog; an empty value falls
// back to workspacePath.
//
// Every consumer of skill discovery goes through this set — GetSkillDescriptors
// (the ListSkills RPC) and buildSessionSkillManager (activation) — so a skill
// cannot be visible without being callable, or vice versa.
func (b *OrchestratorBuilder) SkillDirsForWorkspace(workspacePath, sddCheckoutPath string) []string {
	return b.skillDirsForWorkspace(workspacePath, sddCheckoutPath, b.log())
}

// skillDirsForWorkspace is SkillDirsForWorkspace with an explicit logger: Build
// passes the session's logger so a framework-detection warning lands on the
// session that triggered the scan.
func (b *OrchestratorBuilder) skillDirsForWorkspace(workspacePath, sddCheckoutPath string, logger *slog.Logger) []string {
	b.mu.RLock()
	baseDirs := append([]string(nil), b.baseSkillDirs...)
	b.mu.RUnlock()

	// Detection evidence is probed in the session's execution workspace; the
	// operator DSL that builds the detection catalog is read from the project
	// checkout — repository data that must not follow a managed worktree.
	catalogRoot := sddCheckoutPath
	if catalogRoot == "" {
		catalogRoot = workspacePath
	}
	dirs := make([]string, 0, len(baseDirs)+4)
	if workspacePath != "" {
		dirs = append(dirs, filepath.Join(workspacePath, SkillsRelativePath))
		dirs = append(dirs, ProjectSkillDirsFrom(workspacePath, b.sddCatalogFor(catalogRoot, logger))...)
	}
	dirs = append(dirs, baseDirs...)
	return dedupeSkillDirs(dirs)
}

// ProjectSkillDirs returns the skill discovery directories the detected SDD
// frameworks declare for workspacePath — absolute, deduplicated, ordered by
// framework priority (lower Priority first). Entries are workspace-relative
// paths (".claude/skills"): the DSL validator rejects "~"-relative and
// absolute entries at load, so a framework document cannot point discovery
// outside its workspace.
//
// A framework contributes when it is PRESENT in the workspace: one of its
// markers exists, its declared skill set is satisfied, or one of its declared
// skill directories holds at least one installed skill. The third signal is
// what surfaces a framework's skills in a project whose marker files have not
// been written yet — the skills are the framework's own install, and the
// directory stays unscanned until the DSL's exact skill names happen to match
// otherwise.
//
// The catalog is the built-in one; ProjectSkillDirsFrom is the same detection
// over an explicit catalog (built-ins merged with operator definitions), so a
// caller holding the merged catalog never runs a second, divergent detection.
func ProjectSkillDirs(workspacePath string, logger *slog.Logger) []string {
	frameworks, err := builtinSDDFrameworks()
	if err != nil {
		if logger != nil {
			logger.Warn("SDD framework catalog unavailable; framework skill directories are not scanned",
				"error", err)
		}
		return nil
	}
	return ProjectSkillDirsFrom(workspacePath, frameworks)
}

// ProjectSkillDirsFrom is ProjectSkillDirs over an explicit framework catalog.
// The caller owns ordering (priority first) and any operator-defined framework
// merge.
func ProjectSkillDirsFrom(workspacePath string, frameworks []sdd.Framework) []string {
	if workspacePath == "" {
		return nil
	}

	probe := workspaceProbe{workspace: workspacePath}
	var dirs []string
	for _, fw := range frameworks {
		if len(fw.Detect.Skills.Dirs) == 0 {
			continue
		}
		if !probe.frameworkPresent(fw) {
			continue
		}
		dirs = append(dirs, probe.skillDirs(fw.Detect.Skills)...)
	}
	return dedupeSkillDirs(dirs)
}

// workspaceProbe resolves framework-declared paths against one workspace.
type workspaceProbe struct {
	workspace string
}

// path resolves one declared entry; false means it cannot be used (empty,
// outside the workspace, or a declaration shape the loader rejects).
func (p *workspaceProbe) path(entry string) (string, bool) {
	return resolveSkillDirEntry(entry, p.workspace)
}

// skillDirs resolves a framework's declared skill directories.
func (p *workspaceProbe) skillDirs(skills sdd.Skills) []string {
	dirs := make([]string, 0, len(skills.Dirs))
	for _, entry := range skills.Dirs {
		if dir, ok := p.path(entry); ok {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// frameworkPresent reports whether fw applies to the probed workspace — any
// marker exists, the declared skill set is satisfied, or a declared skill
// directory holds at least one installed skill.
func (p *workspaceProbe) frameworkPresent(fw sdd.Framework) bool {
	for _, marker := range fw.Detect.Markers {
		dir, ok := p.path(marker)
		if !ok {
			continue
		}
		if _, err := os.Stat(dir); err == nil {
			return true
		}
	}
	if p.skillSetSatisfied(fw.Detect.Skills) {
		return true
	}
	for _, dir := range p.skillDirs(fw.Detect.Skills) {
		if holdsSkill(dir) {
			return true
		}
	}
	return false
}

// skillSetSatisfied evaluates Detect.Skills.AllOf / AnyOf against the declared
// directories. Both lists must hold when present: AllOf requires every named
// skill, AnyOf at least one.
func (p *workspaceProbe) skillSetSatisfied(probe sdd.Skills) bool {
	if len(probe.AllOf) == 0 && len(probe.AnyOf) == 0 {
		return false
	}
	dirs := p.skillDirs(probe)
	installed := func(name string) bool {
		for _, dir := range dirs {
			if info, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err == nil && !info.IsDir() {
				return true
			}
		}
		return false
	}
	for _, name := range probe.AllOf {
		if !installed(name) {
			return false
		}
	}
	if len(probe.AnyOf) > 0 {
		for _, name := range probe.AnyOf {
			if installed(name) {
				return true
			}
		}
		return false
	}
	return true
}

// holdsSkill reports whether dir contains at least one skill (a subdirectory
// with a SKILL.md).
func holdsSkill(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() {
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			if info, err := os.Stat(filepath.Join(dir, name)); err != nil || !info.IsDir() {
				continue
			}
		}
		if info, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// resolveSkillDirEntry turns one framework-declared directory into an absolute
// path inside the workspace. The DSL validator (cleanRelative) accepts clean
// workspace-relative entries only and rejects "~"-relative, absolute and
// backslash shapes at load, so this runtime resolver fails closed on them too:
// an entry that cannot legally name a workspace path must not widen discovery
// to one anyway. Containment goes through pathutil.IsWithinPath — the
// centralized path-locality primitive — which also catches symlink-resolved
// escapes for directories that already exist.
func resolveSkillDirEntry(entry, workspacePath string) (string, bool) {
	entry = strings.TrimSpace(entry)
	if entry == "" || workspacePath == "" {
		return "", false
	}
	if strings.HasPrefix(entry, "~") || filepath.IsAbs(entry) {
		return "", false
	}
	resolved := filepath.Join(workspacePath, filepath.FromSlash(entry))
	within, err := pathutil.IsWithinPath(workspacePath, resolved)
	if err != nil || !within {
		return "", false
	}
	return resolved, true
}

// dedupeSkillDirs cleans and deduplicates a discovery set, preserving the first
// (highest-priority) occurrence and dropping empty entries. The same directory
// can legitimately be declared twice — e.g. .agents/skills is both the
// project-local tier and a framework's declared skill directory.
func dedupeSkillDirs(dirs []string) []string {
	seen := make(map[string]struct{}, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		clean := filepath.Clean(dir)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
