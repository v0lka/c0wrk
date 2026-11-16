// Sidebar surface gates: SetResearchEnabled / SetSpecsEnabled persist the
// config research.enabled / specs.enabled toggles (Settings → General).
//
// The two surfaces are deliberately NOT symmetric any more:
//
//   - research.enabled still carries a PACK side-effect. Enabling seeds the
//     research/paper packs, disabling unseeds them. The apply runs under
//     researchSeedMu (configMu → researchSeedMu) and writes the gate mirror
//     under the same lock before its disk pass, so the launch seed — which
//     re-reads that mirror inside its own critical section — can never act on
//     a stale gate.
//   - specs.enabled is a PURE visibility gate. The spec-* skillset is no longer
//     seeded or unseeded by c0wrk: an installed copy is ordinary agent skill
//     data, recognition happens through SDD detection wherever the skills live,
//     and nothing on disk is touched by the toggle.
//
// See the sidebar-packs ADR and specs/domains/{research,specs}.md.
package backend

import (
	"errors"
	"fmt"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/papers"
	"github.com/v0lka/c0wrk/core/research"
)

// SetResearchEnabled toggles the research sidebar surface gate (config
// research.enabled) and persists it. A request matching the stored EFFECTIVE
// value is a no-op (nil default counts as on); a failed disk write rolls the
// in-memory value back. On success the pack side-effect runs in the same
// call — enabling seeds the research/paper packs, disabling unseeds them
// (only byte-identical copies are removed; user-owned and locally modified
// copies are preserved, the modified ones named in a warning) — and
// config:updated is announced.
func (f *FrontendAPI) SetResearchEnabled(enabled bool) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	if f.configPath == "" {
		return errors.New("config path not set")
	}
	if f.config.Research.PackEnabled() == enabled {
		return nil // already in the requested state — no state change
	}

	prev := f.config.Research.Enabled
	f.config.Research.Enabled = &enabled
	if err := config.Save(f.config, f.configPath); err != nil {
		f.config.Research.Enabled = prev
		return fmt.Errorf("failed to persist research config: %w", err)
	}
	f.applyResearchPacksLocked(enabled)
	f.emitConfigUpdated()
	return nil
}

// SetSpecsEnabled toggles the specs sidebar surface gate (config
// specs.enabled) and persists it. The gate is a PURE visibility switch: it
// carries no pack side-effect, because the spec-* skillset is no longer
// seeded or unseeded by c0wrk — the skills are plain agent skills that the
// detection layer recognises wherever they are installed (an already-seeded
// copy keeps working, and removing one never happens behind the operator's
// back). A request matching the stored EFFECTIVE value is a no-op (nil
// default counts as on); a failed disk write rolls the in-memory value back.
// On success config:updated is announced, which is what makes the sidebar tab
// (and the panel's next GetSpecsStatus, whose `present` folds this gate in)
// follow the flip live.
func (f *FrontendAPI) SetSpecsEnabled(enabled bool) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	if f.configPath == "" {
		return errors.New("config path not set")
	}
	if f.config.Specs.PackEnabled() == enabled {
		return nil // already in the requested state — no state change
	}

	prev := f.config.Specs.Enabled
	f.config.Specs.Enabled = &enabled
	if err := config.Save(f.config, f.configPath); err != nil {
		f.config.Specs.Enabled = prev
		return fmt.Errorf("failed to persist specs config: %w", err)
	}
	f.emitConfigUpdated()
	return nil
}

// researchPackGate reads the research sidebar gate (research.enabled) under
// configMu — seedGlobalPacks runs outside the config lock. A nil config
// fail-opens: an unset gate means on, matching the pack defaults that predate
// the gate.
func (f *FrontendAPI) researchPackGate() bool {
	f.configMu.Lock()
	defer f.configMu.Unlock()
	if f.config == nil {
		return true
	}
	return f.config.Research.PackEnabled()
}

// applyResearchPacksLocked seeds or unseeds the research surface's packs —
// the research and paper skill packs plus the research Subagent Profile —
// to match `enabled`. Callers must hold configMu; the whole pass runs under
// researchSeedMu (configMu → researchSeedMu everywhere: seedGlobalPacks
// reads the gates under configMu BEFORE taking researchSeedMu, so the
// nesting never inverts), serializing the toggle against the launch seed
// and every other pack pass. The gate mirror is written under the same lock
// BEFORE the disk pass, so a launch seed that acquires the lock afterwards
// re-reads the toggled value instead of acting on a pre-toggle snapshot.
// Per-pack failures are logged and never fatal — the persisted gate stays
// authoritative, and the next launch seed reconciles the drift.
func (f *FrontendAPI) applyResearchPacksLocked(enabled bool) {
	f.researchSeedMu.Lock()
	defer f.researchSeedMu.Unlock()
	f.packGateResearch = &enabled
	f.runResearchPacks(enabled)
}

// runResearchPacks performs the research surface's disk pass: seeding the
// research and paper skill packs plus the research Subagent Profile when
// `on` is set, unseeding them otherwise — only byte-identical copies (and
// pack-marked truncated subsets) are removed; user-owned and locally
// modified copies are preserved, the modified ones named in a warning. The
// shared body of the toggle apply and the launch seed: callers must hold
// researchSeedMu; no locks are taken here. Per-pack failures are logged and
// never fatal, and the skill/agent caches are invalidated so the next read
// rescans the changed directories.
func (f *FrontendAPI) runResearchPacks(on bool) {
	skillsDir := config.SkillsDir(f.agentDir)
	agentsDir := config.AgentsDir(f.agentDir)

	if on {
		if res, err := research.SeedSkills(skillsDir, f.log()); err != nil {
			f.log().Warn("research skill-pack seeding failed",
				"skills_dir", skillsDir, "error", err)
		} else {
			f.log().Info("research skill-pack seeded",
				"skills_dir", skillsDir,
				"seeded", len(res.Seeded), "updated", len(res.Updated),
				"current", len(res.Current), "preserved", len(res.Preserved))
		}
		if res, err := papers.SeedSkills(skillsDir, f.log()); err != nil {
			f.log().Warn("paper skill-pack seeding failed",
				"skills_dir", skillsDir, "error", err)
		} else {
			f.log().Info("paper skill-pack seeded",
				"skills_dir", skillsDir,
				"seeded", len(res.Seeded), "updated", len(res.Updated),
				"current", len(res.Current), "preserved", len(res.Preserved))
		}
		if res, err := research.SeedAgents(agentsDir, f.log()); err != nil {
			f.log().Warn("research agent-pack seeding failed",
				"agents_dir", agentsDir, "error", err)
		} else {
			f.log().Info("research agent-pack seeded",
				"agents_dir", agentsDir,
				"seeded", len(res.Seeded), "updated", len(res.Updated),
				"current", len(res.Current), "preserved", len(res.Preserved))
		}
	} else {
		if res, err := research.UnseedSkills(skillsDir, f.log()); err != nil {
			f.log().Warn("research skill-pack unseed failed",
				"skills_dir", skillsDir, "error", err)
		} else {
			f.log().Info("research skill-pack unseeded",
				"skills_dir", skillsDir,
				"removed", len(res.Removed),
				"missing", len(res.Missing),
				"preserved_user", len(res.PreservedUser),
				"preserved_modified", len(res.PreservedModified),
				"failed", len(res.Failed))
		}
		if res, err := papers.UnseedSkills(skillsDir, f.log()); err != nil {
			f.log().Warn("paper skill-pack unseed failed",
				"skills_dir", skillsDir, "error", err)
		} else {
			f.log().Info("paper skill-pack unseeded",
				"skills_dir", skillsDir,
				"removed", len(res.Removed),
				"missing", len(res.Missing),
				"preserved_user", len(res.PreservedUser),
				"preserved_modified", len(res.PreservedModified),
				"failed", len(res.Failed))
		}
		if res, err := research.UnseedAgents(agentsDir, f.log()); err != nil {
			f.log().Warn("research agent-pack unseed failed",
				"agents_dir", agentsDir, "error", err)
		} else {
			f.log().Info("research agent-pack unseeded",
				"agents_dir", agentsDir,
				"removed", len(res.Removed),
				"missing", len(res.Missing),
				"preserved_user", len(res.PreservedUser),
				"preserved_modified", len(res.PreservedModified),
				"failed", len(res.Failed))
		}
	}

	// The seeded/unseeded directories are c0wrk-owned content; drop any
	// cached skill/agent listing so the next read rescans them.
	f.invalidateSkillCache()
	f.invalidateAgentCache()
}
