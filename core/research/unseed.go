// Unseed — the delete half of the research packs' lifecycle behind the
// sidebar Research gate (config research.enabled): UnseedSkills removes the
// seeded research-* skill directories from the global skills dir and
// UnseedAgents removes the research Subagent Profile from the global agents
// dir when the surface is disabled; re-enabling reseeds them (SeedSkills /
// SeedAgents). Per seedpack.UnseedTarget's contract only byte-identical
// copies (and pack-marked truncated subsets) are removed — user-owned and
// locally modified copies are preserved and reported, so the non-destructive
// seeding invariant holds across enable/disable cycles.
package research

import (
	"log/slog"

	"github.com/v0lka/c0wrk/core/seedpack"
)

// UnseedSkills removes the seeded research-* skill directories from
// destSkillsDir. The caller resolves the destination — the sidebar gate
// passes the global c0wrk skills directory (config.SkillsDir on the agent
// dir), mirroring SeedSkills.
func UnseedSkills(destSkillsDir string, logger *slog.Logger) (*seedpack.UnseedResult, error) {
	return seedpack.UnseedEntries(skillPackFS, embedRoot, destSkillsDir, ResearchSkillNames(), "research skill", logger)
}

// UnseedAgents removes the seeded research Subagent Profile directories from
// destAgentsDir (the global c0wrk agents directory for the sidebar gate),
// mirroring SeedAgents.
func UnseedAgents(destAgentsDir string, logger *slog.Logger) (*seedpack.UnseedResult, error) {
	return seedpack.UnseedEntries(agentPackFS, agentEmbedRoot, destAgentsDir, ResearchAgentNames(), "research agent", logger)
}
