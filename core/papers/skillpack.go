// Package papers provides c0wrk's paper-study skill-pack: the versioned,
// embed-bundled `study-paper` skill, seeded once per launch by
// FrontendAPI.seedGlobalPacks into the c0wrk GLOBAL skills directory
// (~/.c0wrk/.agents/skills) before the skill watchers start; the global copy
// outranks a same-named ~/.agents skill in discovery (a project-local copy
// still wins).
//
// Seeding is idempotent, crash-safe, and non-destructive to user-authored or
// user-edited skills (content-hash classification — the action table lives on
// seedpack.Classify). See specs/domains/papers.md (global skill-pack seeding
// invariant) and specs/decisions/056-research-always-on-global-seeding.md.
// The classification/staging core is the shared core/seedpack package
// (ADR-105 decision 5), common with core/research.
package papers

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

	"github.com/v0lka/c0wrk/core/seedpack"
	"github.com/v0lka/sp4rk/safeio"
)

// skillPackFS embeds the paper-study skill directories (each a SKILL.md plus
// its references/, assets/ and scripts/ subtrees). The plain directory embed
// excludes dotfiles/underscore-prefixed files (e.g. stray .DS_Store), keeping
// the bundle deterministic.
//
//go:embed skills
var skillPackFS embed.FS

// embedRoot is the path within skillPackFS under which the skill directories
// live. embed.FS always uses forward slashes regardless of host OS.
const embedRoot = "skills"

// CurrentSeedVersion is the pack version stamped into each seeded skill's
// sidecar marker; bump it to refresh existing seeded copies on the next
// startup. Deliberately independent of the research pack's version so the two
// packs bump separately (specs/domains/papers.md, configuration table).
const CurrentSeedVersion = "5"

// SeedResult reports the per-skill outcome of a SeedSkills call. Each slice
// holds skill names (e.g. "study-paper") and is sorted for deterministic
// output. It is the shared core/seedpack Result.
type SeedResult = seedpack.Result

// SkillNames returns the sorted names of the skills bundled in the pack, read
// from the embed.FS at call time so it stays in sync with the embedded tree.
func SkillNames() []string {
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

// SeedSkills materializes the embedded paper-study skill-pack into
// destSkillsDir, creating it if missing. The startup seed passes the c0wrk
// GLOBAL skills dir (config.SkillsDir — ADR-056), not a project-local path.
// Idempotent and non-destructive (classification rules: package docs and
// specs/domains/papers.md); a marker-less directory is overwritten only when
// its content is byte-identical to the pack (marker re-stamped).
func SeedSkills(destSkillsDir string, logger *slog.Logger) (*SeedResult, error) {
	if destSkillsDir == "" {
		return nil, errors.New("papers.SeedSkills: destSkillsDir is empty")
	}
	if err := safeio.MkdirAllRealWithin(filepath.Dir(filepath.Dir(destSkillsDir)), destSkillsDir, 0o755); err != nil {
		return nil, fmt.Errorf("papers.SeedSkills: create skills dir %q: %w", destSkillsDir, err)
	}

	// Sweep away staging/backup leftovers from a previous interrupted run
	// (a hard kill can leave hidden sibling dirs behind; see
	// seedpack.StageAndSwap).
	seedpack.SweepLeftovers(destSkillsDir)

	result := &SeedResult{}
	names := SkillNames()
	for _, name := range names {
		target := filepath.Join(destSkillsDir, name)
		action, err := seedOne(name, target, logger)
		if err != nil {
			return nil, fmt.Errorf("papers.SeedSkills: seed %q: %w", name, err)
		}
		result.Append(action, name)
	}

	result.Sort()
	if logger != nil {
		logger.Info("paper skill-pack seeded",
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
// the crash-safe staging swap (core/seedpack).
func seedOne(name, target string, logger *slog.Logger) (seedpack.Action, error) {
	st, packHash, err := seedpack.InspectTarget(skillPackFS, embedRoot, name, target)
	if err != nil {
		return seedpack.ActionSeed, err
	}
	action, restamp := seedpack.Classify(st, packHash, CurrentSeedVersion)
	switch action {
	case seedpack.ActionPreserve:
		if logger != nil {
			logger.Debug("paper skill preserved (user-owned)", "skill", name, "dir", target)
		}
		return seedpack.ActionPreserve, nil
	case seedpack.ActionModified:
		if logger != nil {
			logger.Info("paper skill locally modified; preserved", "skill", name, "dir", target)
		}
		return seedpack.ActionModified, nil
	case seedpack.ActionCurrent:
		if restamp {
			// Content is exactly the pack; the marker is missing or stale
			// (interrupted legacy write or manual edit): re-claim it.
			if err := seedpack.WriteMarkerAtomic(target, CurrentSeedVersion); err != nil {
				return seedpack.ActionCurrent, err
			}
		}
		return seedpack.ActionCurrent, nil
	}
	// ActionSeed / ActionUpdate: stage the full tree in a hidden sibling dir
	// and swap it into place; an interruption leaves the target either fully
	// old or fully new (see seedpack.StageAndSwap).
	if err := seedpack.StageAndSwap(name, target, CurrentSeedVersion, func(stagingDir string) error {
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
		return seedpack.WriteFile(target, data, 0o644)
	})
}
