// Package research — agent-pack: the versioned, embed-bundled research
// Subagent Profile (AGENT.md), seeded globally once per launch by
// FrontendAPI.seedGlobalPacks into the c0wrk global agents directory before
// the agent watchers start. It mirrors the skill-pack (skillpack.go) so a
// research step can be delegated via delegate(agent:"research"); the seeded
// profile lands where ListAgents and the per-session AgentManager discover
// it (a project-local .agents/agents copy still outranks the global one).
//
// Seeding is idempotent, crash-safe, and non-destructive: destinations are
// classified by CONTENT HASH against the embedded pack (never mtime/size,
// never the marker alone). Full rules: specs/domains/research.md.
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

// agentPackFS embeds the research Subagent Profile directories, mirroring
// skillPackFS's plain directory embed (dotfiles excluded, deterministic).
//
//go:embed agents
var agentPackFS embed.FS

// agentEmbedRoot is the path within agentPackFS under which the profile
// directories live. embed.FS always uses forward slashes regardless of host OS.
const agentEmbedRoot = "agents"

// AgentSeedVersion is the pack version stamped into each seeded profile's
// marker; bump it when embedded content changes. Deliberately separate from
// CurrentSeedVersion so the two packs bump independently.
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

// ResearchAgentNames returns the sorted pack profile names, read from the
// embed.FS at call time to stay in sync with the embedded tree.
func ResearchAgentNames() []string {
	entries, err := fs.ReadDir(agentPackFS, agentEmbedRoot)
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

// SeedAgents materializes the embedded research agent-pack into
// destAgentsDir, created if missing. The caller resolves the destination —
// seedGlobalPacks passes the global c0wrk agents directory (ADR-056), not a
// project-local pack path.
//
// Idempotent per the package docs: a marker-less directory is never
// overwritten unless byte-identical to the pack (then only the marker is
// re-stamped).
func SeedAgents(destAgentsDir string, logger *slog.Logger) (*SeedAgentsResult, error) {
	if destAgentsDir == "" {
		return nil, errors.New("research.SeedAgents: destAgentsDir is empty")
	}
	if err := safeio.MkdirAllRealWithin(filepath.Dir(filepath.Dir(destAgentsDir)), destAgentsDir, 0o755); err != nil {
		return nil, fmt.Errorf("research.SeedAgents: create agents dir %q: %w", destAgentsDir, err)
	}

	// Sweep leftovers from an interrupted previous run (see stageAndSwap).
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

// seedOneAgent classifies one destination by content hash and writes via the
// crash-safe staging swap, reusing the seedAction enum and helpers shared
// with skillpack.go.
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
