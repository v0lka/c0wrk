// Package research provides the RESEARCH mode agent-pack: a versioned,
// embed-bundled Subagent Profile (AGENT.md) seeded into a project's local
// `.agents/agents` directory when RESEARCH mode is enabled. It mirrors the
// skill-pack (skillpack.go) so a research step can be delegated via the
// `#research` mention or delegate(agent:"research").
//
// Seeding is idempotent, crash-safe, and non-destructive to user-authored
// profiles. Classification compares the CONTENT HASH of the on-disk tree
// against the embedded pack (never mtime/size, never the marker alone):
//   - Writes are staged into a hidden sibling temp directory and swapped in
//     with a single rename, so an interrupted run can never leave a
//     truncated tree at the destination; leftovers from a hard kill are
//     swept on the next run.
//   - A directory whose content equals the pack is Current and left
//     untouched; a missing or stale `.seed-version` marker on such a
//     directory is re-stamped.
//   - A pack-marked truncated subset of the pack (an interrupted write — the
//     "marker + truncated tree" state that a marker-first ordering would
//     produce) is repaired, never classified Current.
//   - A pack-marked directory at the current version whose content diverges
//     from the pack was edited locally (or its marker was spoofed): it is
//     preserved untouched and reported as Modified.
//   - A pack-marked directory from an older pack version is overwritten in
//     full (a pack upgrade) and re-marked.
//   - An existing directory with NO marker whose content differs from the
//     pack is user-owned and never clobbered.
//
// Because seeded profiles land in the project-local `.agents/agents`
// directory — which both ListAgents and the per-session AgentManager always
// prepend to their discovery list — they enter the subagent roster
// automatically; no catalog change is required. The activating API layer
// computes the destination via config.ProjectAgentsPath and passes it to
// SeedAgents.
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

// agentPackFS embeds the built-in research Subagent Profile directories (each
// containing an AGENT.md). The directive uses a plain directory embed so
// dotfiles/underscore-prefixed files (e.g. stray .DS_Store) are excluded by the
// embed rules, keeping the bundle deterministic — mirroring skillPackFS.
//
//go:embed agents
var agentPackFS embed.FS

// agentEmbedRoot is the path within agentPackFS under which the profile
// directories live. embed.FS always uses forward slashes regardless of host OS.
const agentEmbedRoot = "agents"

// AgentSeedVersion is the pack version stamped into each seeded profile's
// sidecar marker. Bump this when the embedded AGENT.md content changes and
// existing seeded copies should be refreshed on the next EnableResearch. It is
// deliberately separate from CurrentSeedVersion (the skill-pack version) so the
// two packs can bump independently.
const AgentSeedVersion = "1"

// SeedAgentsResult reports the per-profile outcome of a SeedAgents call. Each
// slice holds profile names (e.g. "research") and is sorted for deterministic
// output. It mirrors SeedSkillsResult.
type SeedAgentsResult struct {
	Seeded    []string // newly written (directory did not exist)
	Updated   []string // overwritten to the current pack version (upgrade or truncated-tree repair)
	Current   []string // content matches the pack; left untouched
	Preserved []string // user-owned (no marker, content differs); left untouched
	Modified  []string // pack-marked but content diverges (user edit or spoofed marker); left untouched
}

// ResearchAgentNames returns the sorted names of the research Subagent
// Profiles bundled in the pack, read from the embed.FS at call time so it stays
// in sync with the embedded tree.
func ResearchAgentNames() []string {
	entries, err := fs.ReadDir(agentPackFS, agentEmbedRoot)
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

// SeedAgents materializes the embedded research agent-pack into destAgentsDir.
// destAgentsDir is the project-local agents directory (typically
// config.ProjectAgentsPath(workspacePath)); it is created if missing. The
// destination must be a real, writable filesystem path.
//
// The function is safe to call repeatedly (idempotent): see the package docs
// for the classification rules. It never overwrites a directory that lacks a
// pack marker unless its content is byte-identical to the pack (in which case
// only the marker is re-stamped).
func SeedAgents(destAgentsDir string, logger *slog.Logger) (*SeedAgentsResult, error) {
	if destAgentsDir == "" {
		return nil, errors.New("research.SeedAgents: destAgentsDir is empty")
	}
	if err := os.MkdirAll(destAgentsDir, 0o755); err != nil {
		return nil, fmt.Errorf("research.SeedAgents: create agents dir %q: %w", destAgentsDir, err)
	}

	// Sweep away staging/backup leftovers from a previous interrupted run
	// (a hard kill can leave hidden sibling dirs behind; see stageAndSwap).
	sweepSeedLeftovers(destAgentsDir)

	result := &SeedAgentsResult{}
	names := ResearchAgentNames()
	for _, name := range names {
		target := filepath.Join(destAgentsDir, name)
		action, err := seedOneAgent(name, target, logger)
		if err != nil {
			return nil, fmt.Errorf("research.SeedAgents: seed %q: %w", name, err)
		}
		appendAgentResult(result, action, name)
	}

	sortAgentResult(result)
	if logger != nil {
		logger.Info("research agent-pack seeded",
			"dest", destAgentsDir,
			"version", AgentSeedVersion,
			"seeded", len(result.Seeded),
			"updated", len(result.Updated),
			"current", len(result.Current),
			"preserved", len(result.Preserved),
			"modified", len(result.Modified),
		)
	}
	return result, nil
}

// seedOneAgent handles a single embedded profile: classifies the destination
// by content hash against the embedded pack and performs the required write
// via the crash-safe staging swap (see seedstaging.go). It reuses the
// seedAction enum and the staging helpers from skillpack.go/seedstaging.go
// (same package).
func seedOneAgent(name, target string, logger *slog.Logger) (seedAction, error) {
	st, packHash, err := inspectSeedTarget(agentPackFS, agentEmbedRoot, name, target)
	if err != nil {
		return actionSeed, err
	}
	action, restamp := classifySeed(st, packHash, AgentSeedVersion)
	switch action {
	case actionPreserve:
		if logger != nil {
			logger.Debug("research agent preserved (user-owned)", "agent", name, "dir", target)
		}
		return actionPreserve, nil
	case actionModified:
		if logger != nil {
			logger.Info("research agent profile locally modified; preserved", "agent", name, "dir", target)
		}
		return actionModified, nil
	case actionCurrent:
		if restamp {
			if err := writeMarkerAtomic(target, AgentSeedVersion); err != nil {
				return actionCurrent, err
			}
		}
		return actionCurrent, nil
	}
	if err := stageAndSwap(name, target, AgentSeedVersion, func(stagingDir string) error {
		return writeAgentEmbedTree(name, stagingDir)
	}); err != nil {
		return action, err
	}
	return action, nil
}

// writeAgentEmbedTree writes the embedded subtree for one profile to dest,
// creating intermediate directories as needed. embed paths use forward slashes.
func writeAgentEmbedTree(name, dest string) error {
	srcRoot := path.Join(agentEmbedRoot, name)
	return fs.WalkDir(agentPackFS, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Path relative to the profile's embed root (forward-slash math), then
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
		data, rerr := agentPackFS.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		return seedWriteFile(target, data, 0o644)
	})
}

// appendAgentResult records a profile name under the bucket for its action.
func appendAgentResult(r *SeedAgentsResult, action seedAction, name string) {
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

// sortAgentResult sorts every bucket of a result for deterministic output.
func sortAgentResult(r *SeedAgentsResult) {
	sort.Strings(r.Seeded)
	sort.Strings(r.Updated)
	sort.Strings(r.Current)
	sort.Strings(r.Preserved)
	sort.Strings(r.Modified)
}
