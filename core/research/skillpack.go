// Package research provides the RESEARCH mode skill-pack: a versioned,
// embed-bundled set of the seven research-* skills (sourced from the
// engineering skills set) that are seeded into a project's local
// `.agents/skills` directory when RESEARCH mode is enabled.
//
// Seeding is idempotent, crash-safe, and non-destructive to user-authored
// skills. Classification compares the CONTENT HASH of the on-disk tree
// against the embedded pack (never mtime/size, never the marker alone):
//   - Writes are staged into a hidden sibling temp directory and swapped in
//     with a single rename, so an interrupted run can never leave a
//     truncated tree at the destination; leftovers from a hard kill are
//     swept on the next run.
//   - A directory whose content equals the pack is Current and left
//     untouched; a missing or stale `.seed-version` marker on such a
//     directory is re-stamped (an interrupted legacy write left complete
//     content without a marker).
//   - A pack-marked truncated subset of the pack (an interrupted write — the
//     "marker + truncated tree" state that a marker-first ordering would
//     produce) is repaired, never classified Current.
//   - A pack-marked directory at the current version whose content diverges
//     from the pack was edited locally (or its marker was spoofed): it is
//     preserved untouched and reported as Modified — never silently
//     overwritten, never reported as the current pack.
//   - A pack-marked directory from an older pack version is overwritten in
//     full (a pack upgrade) and re-marked.
//   - An existing directory with NO marker whose content differs from the
//     pack is user-owned and never clobbered.
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
	Updated   []string // overwritten to the current pack version (upgrade or truncated-tree repair)
	Current   []string // content matches the pack; left untouched
	Preserved []string // user-owned (no marker, content differs); left untouched
	Modified  []string // pack-marked but content diverges (user edit or spoofed marker); left untouched
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
// for the classification rules. It never overwrites a directory that lacks a
// pack marker unless its content is byte-identical to the pack (in which case
// only the marker is re-stamped).
func SeedSkills(destSkillsDir string, logger *slog.Logger) (*SeedSkillsResult, error) {
	if destSkillsDir == "" {
		return nil, errors.New("research.SeedSkills: destSkillsDir is empty")
	}
	if err := os.MkdirAll(destSkillsDir, 0o755); err != nil {
		return nil, fmt.Errorf("research.SeedSkills: create skills dir %q: %w", destSkillsDir, err)
	}

	// Sweep away staging/backup leftovers from a previous interrupted run
	// (a hard kill can leave hidden sibling dirs behind; see stageAndSwap).
	sweepSeedLeftovers(destSkillsDir)

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
			"modified", len(result.Modified),
		)
	}
	return result, nil
}

// seedOne handles a single embedded skill: classifies the destination by
// content hash against the embedded pack and performs the required write via
// the crash-safe staging swap (see seedstaging.go).
func seedOne(name, target string, logger *slog.Logger) (seedAction, error) {
	st, packHash, err := inspectSeedTarget(skillPackFS, embedRoot, name, target)
	if err != nil {
		return actionSeed, err
	}
	action, restamp := classifySeed(st, packHash, CurrentSeedVersion)
	switch action {
	case actionPreserve:
		if logger != nil {
			logger.Debug("research skill preserved (user-owned)", "skill", name, "dir", target)
		}
		return actionPreserve, nil
	case actionModified:
		if logger != nil {
			logger.Info("research skill locally modified; preserved", "skill", name, "dir", target)
		}
		return actionModified, nil
	case actionCurrent:
		if restamp {
			// Content is exactly the pack; the marker is missing or stale
			// (interrupted legacy write or manual edit): re-claim it.
			if err := writeMarkerAtomic(target, CurrentSeedVersion); err != nil {
				return actionCurrent, err
			}
		}
		return actionCurrent, nil
	}
	// actionSeed / actionUpdate: stage the full tree in a hidden sibling dir
	// and swap it into place atomically. The staging tree is complete (and
	// the old directory is moved aside, not deleted first), so an
	// interruption leaves the target either fully old or fully new.
	if err := stageAndSwap(name, target, CurrentSeedVersion, func(stagingDir string) error {
		return writeEmbedTree(name, stagingDir)
	}); err != nil {
		return action, err
	}
	return action, nil
}

// writeEmbedTree writes the embedded subtree for one skill to dest, creating
// intermediate directories as needed. embed paths use forward slashes.
func writeEmbedTree(name, dest string) error {
	srcRoot := path.Join(embedRoot, name)
	return fs.WalkDir(skillPackFS, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Path relative to the skill's embed root (forward-slash math), then
		// converted to the host separator. The walk root itself maps to ".".
		rel := strings.TrimPrefix(p, srcRoot)
		rel = strings.TrimPrefix(rel, "/")
		target := dest
		if rel != "" {
			target = filepath.Join(dest, filepath.FromSlash(rel))
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rerr := skillPackFS.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		return seedWriteFile(target, data, 0o644)
	})
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
	case actionModified:
		r.Modified = append(r.Modified, name)
	}
}

// sortAll sorts every bucket of a result for deterministic output.
func sortAll(r *SeedSkillsResult) {
	sort.Strings(r.Seeded)
	sort.Strings(r.Updated)
	sort.Strings(r.Current)
	sort.Strings(r.Preserved)
	sort.Strings(r.Modified)
}
