// Package research provides the RESEARCH mode skill-pack: a versioned,
// embed-bundled set of the seven research-* skills (sourced from the
// engineering skills set) that are seeded into a project's local
// `.agents/skills` directory when RESEARCH mode is enabled.
//
// Seeding is idempotent and non-destructive to user-authored skills:
//   - A brand-new skill directory is written together with a sidecar
//     `.seed-version` marker recording the pack version that produced it.
//   - An existing directory whose marker equals the current pack version is
//     left untouched (re-enabling with the same version preserves any user
//     edits made to the seeded skill).
//   - An existing directory whose marker differs from the current pack
//     version is overwritten in full (a pack upgrade) and re-marked.
//   - An existing directory with NO marker is treated as user-owned and is
//     never clobbered.
//
// Because seeded skills land in the project-local `.agents/skills`
// directory — which the per-session SkillManager always prepends to its
// discovery list — they enter the router catalog automatically; no catalog
// change is required. The activating API layer (see Task 6) computes the
// destination via config.ProjectSkillsPath and passes it to SeedSkills.
package research

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// skillPackFS embeds the seven research-* skill directories (each containing
// a SKILL.md and, for some, an assets/ subtree). The directive uses a plain
// directory embed so dotfiles/underscore-prefixed files (e.g. stray .DS_Store)
// are excluded by the embed rules, keeping the bundle deterministic.
//
//go:embed skills
var skillPackFS embed.FS

// embedRoot is the path within skillPackFS under which the skill directories
// live. embed.FS always uses forward slashes regardless of host OS.
const embedRoot = "skills"

// CurrentSeedVersion is the pack version stamped into each seeded skill's
// sidecar marker. Bump this when the embedded skill content changes and
// existing seeded copies should be refreshed on the next EnableResearch.
const CurrentSeedVersion = "2"

// seedVersionFile is the sidecar marker filename written into every seeded
// skill directory. Its presence identifies a directory as pack-seeded
// (and thus safe to overwrite on a version bump).
const seedVersionFile = ".seed-version"

// SeedSkillsResult reports the per-skill outcome of a SeedSkills call.
// Each slice holds skill names (e.g. "research-init") and is sorted for
// deterministic output.
type SeedSkillsResult struct {
	Seeded    []string // newly written (directory did not exist)
	Updated   []string // overwritten to the current pack version
	Current   []string // already at the current version; left untouched
	Preserved []string // user-owned (no marker); left untouched
}

// ResearchSkillNames returns the sorted names of the seven research-*
// skills bundled in the pack, read from the embed.FS at call time so it
// stays in sync with the embedded tree.
func ResearchSkillNames() []string {
	entries, err := fs.ReadDir(skillPackFS, embedRoot)
	if err != nil {
		// The embed tree is compile-time fixed; ReadDir can only fail on a
		// programming error (bad path). Return empty rather than panic.
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// SeedSkills materializes the embedded research skill-pack into destSkillsDir.
// destSkillsDir is the project-local skills directory (typically
// config.ProjectSkillsPath(workspacePath)); it is created if missing. The
// destination must be a real, writable filesystem path.
//
// The function is safe to call repeatedly (idempotent): see the package docs
// for the version-marker rules. It never overwrites a directory that lacks a
// pack marker (user-authored skills are preserved).
func SeedSkills(destSkillsDir string, logger *slog.Logger) (*SeedSkillsResult, error) {
	if destSkillsDir == "" {
		return nil, errors.New("research.SeedSkills: destSkillsDir is empty")
	}
	if err := os.MkdirAll(destSkillsDir, 0o755); err != nil {
		return nil, fmt.Errorf("research.SeedSkills: create skills dir %q: %w", destSkillsDir, err)
	}

	result := &SeedSkillsResult{}
	names := ResearchSkillNames()
	for _, name := range names {
		target := filepath.Join(destSkillsDir, name)
		action, err := seedOne(name, target, logger)
		if err != nil {
			return nil, fmt.Errorf("research.SeedSkills: seed %q: %w", name, err)
		}
		appendToResult(result, action, name)
	}

	sortAll(result)
	if logger != nil {
		logger.Info("research skill-pack seeded",
			"dest", destSkillsDir,
			"version", CurrentSeedVersion,
			"seeded", len(result.Seeded),
			"updated", len(result.Updated),
			"current", len(result.Current),
			"preserved", len(result.Preserved),
		)
	}
	return result, nil
}

// seedAction enumerates the per-skill seeding outcomes.
type seedAction int

const (
	actionSeed     seedAction = iota // newly written
	actionUpdate                     // overwritten (version differed)
	actionCurrent                    // already current, skipped
	actionPreserve                   // user-owned, skipped
)

// seedOne handles a single embedded skill: decides the action from the
// destination's marker state and performs the write when required.
func seedOne(name, target string, logger *slog.Logger) (seedAction, error) {
	hasMarker, existingVersion := readSeedVersion(target)
	dirExists := dirExists(target)

	// User-owned directory: never clobber, regardless of re-enable.
	if dirExists && !hasMarker {
		if logger != nil {
			logger.Debug("research skill preserved (user-owned)", "skill", name, "dir", target)
		}
		return actionPreserve, nil
	}

	// Already at current version: skip to preserve user edits.
	if hasMarker && existingVersion == CurrentSeedVersion {
		return actionCurrent, nil
	}

	// Either new, or a version bump: write the embedded tree afresh.
	// On an update, clear the directory first so stale files (e.g. removed
	// assets between versions) do not linger.
	if dirExists {
		if err := os.RemoveAll(target); err != nil {
			return actionSeed, fmt.Errorf("clear stale dir: %w", err)
		}
	}
	if err := writeEmbedTree(name, target); err != nil {
		return actionSeed, err
	}
	if err := writeSeedVersion(target); err != nil {
		return actionSeed, err
	}

	if hasMarker {
		return actionUpdate, nil
	}
	return actionSeed, nil
}

// writeEmbedTree writes the embedded subtree for one skill to target, creating
// intermediate directories as needed. embed paths use forward slashes.
func writeEmbedTree(name, target string) error {
	srcRoot := path.Join(embedRoot, name)
	return fs.WalkDir(skillPackFS, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Path relative to the skill's embed root (forward-slash math), then
		// converted to the host separator. The walk root itself maps to ".".
		rel := strings.TrimPrefix(p, srcRoot)
		rel = strings.TrimPrefix(rel, "/")
		dest := target
		if rel != "" {
			dest = filepath.Join(target, filepath.FromSlash(rel))
		}
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, rerr := skillPackFS.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(dest, data, 0o644)
	})
}

// writeSeedVersion stamps the current pack version into the skill directory.
func writeSeedVersion(target string) error {
	markerPath := filepath.Join(target, seedVersionFile)
	return os.WriteFile(markerPath, []byte(CurrentSeedVersion), 0o644)
}

// readSeedVersion returns whether the directory carries a pack marker and, if
// so, the version string it records. A missing directory or marker is reported
// as (false, "").
func readSeedVersion(target string) (hasMarker bool, version string) {
	data, err := os.ReadFile(filepath.Join(target, seedVersionFile))
	if err != nil {
		return false, ""
	}
	return true, strings.TrimSpace(string(data))
}

// dirExists reports whether target exists and is a directory.
func dirExists(target string) bool {
	info, err := os.Stat(target)
	return err == nil && info.IsDir()
}

// appendToResult records a skill name under the bucket for its action.
func appendToResult(r *SeedSkillsResult, action seedAction, name string) {
	switch action {
	case actionSeed:
		r.Seeded = append(r.Seeded, name)
	case actionUpdate:
		r.Updated = append(r.Updated, name)
	case actionCurrent:
		r.Current = append(r.Current, name)
	case actionPreserve:
		r.Preserved = append(r.Preserved, name)
	}
}

// sortAll sorts every bucket of a result for deterministic output.
func sortAll(r *SeedSkillsResult) {
	sort.Strings(r.Seeded)
	sort.Strings(r.Updated)
	sort.Strings(r.Current)
	sort.Strings(r.Preserved)
}
