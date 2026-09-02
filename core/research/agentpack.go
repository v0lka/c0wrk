// Package research provides the RESEARCH mode agent-pack: a versioned,
// embed-bundled Subagent Profile (AGENT.md) seeded into a project's local
// `.agents/agents` directory when RESEARCH mode is enabled. It mirrors the
// skill-pack (skillpack.go) so a research step can be delegated via the
// `#research` mention or delegate(agent:"research").
//
// Seeding is idempotent and non-destructive to user-authored profiles:
//   - A brand-new profile directory is written together with a sidecar
//     `.seed-version` marker recording the pack version that produced it.
//   - An existing directory whose marker equals the current pack version is
//     left untouched (re-enabling with the same version preserves any user
//     edits made to the seeded profile).
//   - An existing directory whose marker differs from the current pack
//     version is overwritten in full (a pack upgrade) and re-marked.
//   - An existing directory with NO marker is treated as user-owned and is
//     never clobbered.
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
	Updated   []string // overwritten to the current pack version
	Current   []string // already at the current version; left untouched
	Preserved []string // user-owned (no marker); left untouched
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
// for the version-marker rules. It never overwrites a directory that lacks a
// pack marker (user-authored profiles are preserved).
func SeedAgents(destAgentsDir string, logger *slog.Logger) (*SeedAgentsResult, error) {
	if destAgentsDir == "" {
		return nil, errors.New("research.SeedAgents: destAgentsDir is empty")
	}
	if err := os.MkdirAll(destAgentsDir, 0o755); err != nil {
		return nil, fmt.Errorf("research.SeedAgents: create agents dir %q: %w", destAgentsDir, err)
	}

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
		)
	}
	return result, nil
}

// seedOneAgent handles a single embedded profile: decides the action from the
// destination's marker state and performs the write when required. It reuses
// the seedAction enum, seedVersionFile, readSeedVersion, and dirExists helpers
// from skillpack.go (same package), but stamps the agent-pack version via
// writeAgentSeedVersion.
func seedOneAgent(name, target string, logger *slog.Logger) (seedAction, error) {
	hasMarker, existingVersion := readSeedVersion(target)
	dirExists := dirExists(target)

	// User-owned directory: never clobber, regardless of re-enable.
	if dirExists && !hasMarker {
		if logger != nil {
			logger.Debug("research agent preserved (user-owned)", "agent", name, "dir", target)
		}
		return actionPreserve, nil
	}

	// Already at current version: skip to preserve user edits.
	if hasMarker && existingVersion == AgentSeedVersion {
		return actionCurrent, nil
	}

	// Either new, or a version bump: write the embedded tree afresh. On an
	// update, clear the directory first so stale files do not linger.
	if dirExists {
		if err := os.RemoveAll(target); err != nil {
			return actionSeed, fmt.Errorf("clear stale dir: %w", err)
		}
	}
	if err := writeAgentEmbedTree(name, target); err != nil {
		return actionSeed, err
	}
	if err := writeAgentSeedVersion(target); err != nil {
		return actionSeed, err
	}

	if hasMarker {
		return actionUpdate, nil
	}
	return actionSeed, nil
}

// writeAgentEmbedTree writes the embedded subtree for one profile to target,
// creating intermediate directories as needed. embed paths use forward slashes.
func writeAgentEmbedTree(name, target string) error {
	srcRoot := path.Join(agentEmbedRoot, name)
	return fs.WalkDir(agentPackFS, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Path relative to the profile's embed root (forward-slash math), then
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
		data, rerr := agentPackFS.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(dest, data, 0o644)
	})
}

// writeAgentSeedVersion stamps the agent-pack version into the profile
// directory. It is version-aware (unlike writeSeedVersion, which stamps the
// skill-pack version) so the two packs can bump independently.
func writeAgentSeedVersion(target string) error {
	markerPath := filepath.Join(target, seedVersionFile)
	return os.WriteFile(markerPath, []byte(AgentSeedVersion), 0o644)
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
	}
}

// sortAgentResult sorts every bucket of a result for deterministic output.
func sortAgentResult(r *SeedAgentsResult) {
	sort.Strings(r.Seeded)
	sort.Strings(r.Updated)
	sort.Strings(r.Current)
	sort.Strings(r.Preserved)
}
