// Package specs — skill-pack: the six vendored spec-* skills (spec-init,
// spec-explore, spec-consult, spec-create, spec-update, spec-check; ADR-105),
// seeded globally once per launch into the c0wrk global skills directory
// before the skill watchers start. The seeded copies land where the
// per-session SkillManager discovers them (a project-local .agents/skills
// copy still outranks the global one), so they enter the router catalog
// without a catalog change. Upstream origin and MIT attribution: LICENSE.md.
//
// Seeding is idempotent, crash-safe, and non-destructive: destinations are
// classified by CONTENT HASH against the embedded pack (never mtime/size,
// never the marker alone). The classification/staging mechanics live in the
// shared core/seedpack package (ADR-105 decision 5); full rules:
// specs/domains/specs.md (seeding invariants).
package specs

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

// skillPackFS embeds the six spec-* skill directories; the plain directory
// embed excludes dotfiles/underscore files, keeping the bundle deterministic.
//
//go:embed skills
var skillPackFS embed.FS

// embedRoot is the path within skillPackFS holding the skill directories;
// embed.FS paths are always forward-slash regardless of host OS.
const embedRoot = "skills"

// CurrentSeedVersion is the pack version stamped into each seeded skill's
// marker; bump it when embedded content changes and copies should refresh.
// Deliberately independent of every other pack's marker (research, papers) so
// the packs bump independently. v2: ADR immutability keys on commit state
// (committed = locked, uncommitted = editable draft).
const CurrentSeedVersion = "2"

// SeedSkillsResult reports the per-skill outcome of a SeedSkills call; each
// slice holds skill names, sorted for deterministic output. It is the shared
// core/seedpack Result.
type SeedSkillsResult = seedpack.Result

// SpecSkillNames returns the sorted pack skill names, read from the embed.FS
// at call time to stay in sync with the embedded tree.
func SpecSkillNames() []string {
	entries, err := fs.ReadDir(skillPackFS, embedRoot)
	if err != nil {
		// Compile-time-fixed tree: ReadDir failing is a programming error; empty, not panic.
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

// SeedSkills materializes the embedded spec skill-pack into destSkillsDir,
// created if missing. The caller resolves the destination — the global seed
// passes the global c0wrk skills directory (ADR-056 precedence), not a
// project-local pack path.
//
// Idempotent per the package docs: a marker-less directory is never
// overwritten unless byte-identical to the pack (then only the marker is
// re-stamped).
func SeedSkills(destSkillsDir string, logger *slog.Logger) (*SeedSkillsResult, error) {
	if destSkillsDir == "" {
		return nil, errors.New("specs.SeedSkills: destSkillsDir is empty")
	}
	if err := safeio.MkdirAllRealWithin(filepath.Dir(filepath.Dir(destSkillsDir)), destSkillsDir, 0o755); err != nil {
		return nil, fmt.Errorf("specs.SeedSkills: create skills dir %q: %w", destSkillsDir, err)
	}

	// Sweep leftovers from an interrupted previous run (see seedpack.StageAndSwap).
	seedpack.SweepLeftovers(destSkillsDir)

	result := &SeedSkillsResult{}
	names := SpecSkillNames()
	for _, name := range names {
		target := filepath.Join(destSkillsDir, name)
		action, err := seedOne(name, target, logger)
		if err != nil {
			return nil, fmt.Errorf("specs.SeedSkills: seed %q: %w", name, err)
		}
		result.Append(action, name)
	}

	result.Sort()
	if logger != nil {
		logger.Info("spec skill-pack seeded",
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

// seedOne classifies one destination by content hash and performs the write
// via the crash-safe staging swap (core/seedpack).
func seedOne(name, target string, logger *slog.Logger) (seedpack.Action, error) {
	st, packHash, err := seedpack.InspectTarget(skillPackFS, embedRoot, name, target)
	if err != nil {
		return seedpack.ActionSeed, err
	}
	action, restamp := seedpack.Classify(st, packHash, CurrentSeedVersion)
	switch action {
	case seedpack.ActionPreserve:
		if logger != nil {
			logger.Debug("spec skill preserved (user-owned)", "skill", name, "dir", target)
		}
		return seedpack.ActionPreserve, nil
	case seedpack.ActionModified:
		if logger != nil {
			logger.Info("spec skill locally modified; preserved", "skill", name, "dir", target)
		}
		return seedpack.ActionModified, nil
	case seedpack.ActionCurrent:
		if restamp {
			// Content equals the pack; marker missing/stale: re-claim it.
			if err := seedpack.WriteMarkerAtomic(target, CurrentSeedVersion); err != nil {
				return seedpack.ActionCurrent, err
			}
		}
		return seedpack.ActionCurrent, nil
	}
	// ActionSeed/ActionUpdate: stage the full tree in a hidden sibling dir
	// and swap — an interruption leaves the target fully old or fully new.
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
		// Embed-root-relative path (forward slashes) converted to the host
		// separator; the walk root maps to ".".
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
