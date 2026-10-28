// Package research — skill-pack: the seven embedded research-* skills,
// seeded globally once per launch by FrontendAPI.seedGlobalPacks into the
// c0wrk global skills directory before the skill watchers start (RESEARCH is
// always on for real projects — no per-project enable step). The seeded
// copies land where the per-session SkillManager discovers them (a
// project-local .agents/skills copy still outranks the global one), so they
// enter the router catalog without a catalog change.
//
// Seeding is idempotent, crash-safe, and non-destructive: destinations are
// classified by CONTENT HASH against the embedded pack (never mtime/size,
// never the marker alone). Full classification and crash-safety rules:
// specs/domains/research.md (seeding invariants).
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

	"github.com/v0lka/sp4rk/safeio"
)

// skillPackFS embeds the seven research-* skill directories; the plain
// directory embed excludes dotfiles/underscore files, keeping the bundle
// deterministic.
//
//go:embed skills
var skillPackFS embed.FS

// embedRoot is the path within skillPackFS holding the skill directories;
// embed.FS paths are always forward-slash regardless of host OS.
const embedRoot = "skills"

// CurrentSeedVersion is the pack version stamped into each seeded skill's
// marker; bump it when embedded content changes and copies should refresh.
const CurrentSeedVersion = "2"

// seedVersionFile is the marker filename whose presence identifies a
// directory as pack-seeded (safe to overwrite on a version bump).
const seedVersionFile = ".seed-version"

// SeedSkillsResult reports the per-skill outcome of a SeedSkills call; each
// slice holds skill names, sorted for deterministic output.
type SeedSkillsResult struct {
	Seeded    []string // newly written (directory did not exist)
	Updated   []string // overwritten to the current pack version (upgrade or truncated-tree repair)
	Current   []string // content matches the pack; left untouched
	Preserved []string // user-owned (no marker, content differs); left untouched
	Modified  []string // pack-marked but content diverges (user edit or spoofed marker); left untouched
}

// ResearchSkillNames returns the sorted pack skill names, read from the
// embed.FS at call time to stay in sync with the embedded tree.
func ResearchSkillNames() []string {
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

// SeedSkills materializes the embedded research skill-pack into
// destSkillsDir, created if missing. The caller resolves the destination —
// seedGlobalPacks passes the global c0wrk skills directory (ADR-056), not a
// project-local pack path.
//
// Idempotent per the package docs: a marker-less directory is never
// overwritten unless byte-identical to the pack (then only the marker is
// re-stamped).
func SeedSkills(destSkillsDir string, logger *slog.Logger) (*SeedSkillsResult, error) {
	if destSkillsDir == "" {
		return nil, errors.New("research.SeedSkills: destSkillsDir is empty")
	}
	if err := safeio.MkdirAllRealWithin(filepath.Dir(filepath.Dir(destSkillsDir)), destSkillsDir, 0o755); err != nil {
		return nil, fmt.Errorf("research.SeedSkills: create skills dir %q: %w", destSkillsDir, err)
	}

	// Sweep leftovers from an interrupted previous run (see stageAndSwap).
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

// seedOne classifies one destination by content hash and performs the write
// via the crash-safe staging swap (seedstaging.go).
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
			// Content equals the pack; marker missing/stale: re-claim it.
			if err := writeMarkerAtomic(target, CurrentSeedVersion); err != nil {
				return actionCurrent, err
			}
		}
		return actionCurrent, nil
	}
	// actionSeed/actionUpdate: stage the full tree in a hidden sibling dir
	// and swap — an interruption leaves the target fully old or fully new.
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
		return seedWriteFile(target, data, 0o644)
	})
}

// readSeedVersion returns the pack marker's presence and version; missing
// either reads (false, "").
func readSeedVersion(target string) (hasMarker bool, version string) {
	data, err := safeio.ReadFile(filepath.Join(target, seedVersionFile))
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
