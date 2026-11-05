// Unseed — the delete half of the paper-study skill pack's lifecycle behind
// the sidebar Research gate (config research.enabled): UnseedSkills removes
// the seeded study-paper skill directory from the global skills dir when the
// surface is disabled; re-enabling reseeds it (SeedSkills). Per
// seedpack.UnseedTarget's contract only byte-identical copies (and
// pack-marked truncated subsets) are removed — user-owned and locally
// modified copies are preserved and reported.
package papers

import (
	"log/slog"

	"github.com/v0lka/c0wrk/core/seedpack"
)

// UnseedSkills removes the seeded paper-study skill directories from
// destSkillsDir. The caller resolves the destination — the sidebar gate
// passes the global c0wrk skills directory (config.SkillsDir on the agent
// dir), mirroring SeedSkills.
func UnseedSkills(destSkillsDir string, logger *slog.Logger) (*seedpack.UnseedResult, error) {
	return seedpack.UnseedEntries(skillPackFS, embedRoot, destSkillsDir, SkillNames(), "paper skill", logger)
}
