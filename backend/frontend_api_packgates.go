// Sidebar surface gates: SetResearchEnabled / SetSpecsEnabled persist the
// config research.enabled / specs.enabled toggles (Settings → General) and
// apply their pack side-effect in the same beat — enabling seeds the
// surface's packs, disabling unseeds them. The apply runs under
// researchSeedMu (configMu → researchSeedMu) and writes the gate mirror
// under the same lock before its disk pass, so the launch seed — which
// re-reads that mirror inside its own critical section — can never act on a
// stale gate; a disabled surface is unseeded at launch too, reconciling any
// drift a failed toggle-time removal left behind. See the sidebar-packs ADR
// and specs/domains/{research,specs}.md.
package backend

import (
	"errors"
	"fmt"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/papers"
	"github.com/v0lka/c0wrk/core/research"
	"github.com/v0lka/c0wrk/core/specs"
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
// specs.enabled) and persists it — the SetResearchEnabled contract applied
// to the specs surface (the six spec-* skills + the specs Subagent Profile).
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
	f.applySpecsPacksLocked(enabled)
	f.emitConfigUpdated()
	return nil
}

// sidebarPackGates reads the two sidebar surface gates (research.enabled /
// specs.enabled) under configMu — seedGlobalPacks runs outside the config
// lock. A nil config fail-opens: unset gates mean on, matching the pack
// defaults that predate the gates.
func (f *FrontendAPI) sidebarPackGates() (researchOn, specsOn bool) {
	f.configMu.Lock()
	defer f.configMu.Unlock()
	if f.config == nil {
		return true, true
	}
	return f.config.Research.PackEnabled(), f.config.Specs.PackEnabled()
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

// applySpecsPacksLocked seeds or unseeds the specs surface's packs — the
// spec skill pack plus the specs Subagent Profile — to match `enabled`,
// under the same contract as applyResearchPacksLocked (configMu held by the
// caller; the pass serialized on researchSeedMu, the gate mirror written
// before the disk pass).
func (f *FrontendAPI) applySpecsPacksLocked(enabled bool) {
	f.researchSeedMu.Lock()
	defer f.researchSeedMu.Unlock()
	f.packGateSpecs = &enabled
	f.runSpecsPacks(enabled)
}

// runSpecsPacks performs the specs surface's disk pass — the spec skill
// pack plus the specs Subagent Profile — under the same contract as
// runResearchPacks: callers must hold researchSeedMu; no locks are taken
// here; per-pack failures are logged and never fatal; the skill/agent
// caches are invalidated afterwards.
func (f *FrontendAPI) runSpecsPacks(on bool) {
	skillsDir := config.SkillsDir(f.agentDir)
	agentsDir := config.AgentsDir(f.agentDir)

	if on {
		if res, err := specs.SeedSkills(skillsDir, f.log()); err != nil {
			f.log().Warn("spec skill-pack seeding failed",
				"skills_dir", skillsDir, "error", err)
		} else {
			f.log().Info("spec skill-pack seeded",
				"skills_dir", skillsDir,
				"seeded", len(res.Seeded), "updated", len(res.Updated),
				"current", len(res.Current), "preserved", len(res.Preserved))
		}
		if res, err := specs.SeedAgents(agentsDir, f.log()); err != nil {
			f.log().Warn("spec agent-pack seeding failed",
				"agents_dir", agentsDir, "error", err)
		} else {
			f.log().Info("spec agent-pack seeded",
				"agents_dir", agentsDir,
				"seeded", len(res.Seeded), "updated", len(res.Updated),
				"current", len(res.Current), "preserved", len(res.Preserved))
		}
	} else {
		if res, err := specs.UnseedSkills(skillsDir, f.log()); err != nil {
			f.log().Warn("spec skill-pack unseed failed",
				"skills_dir", skillsDir, "error", err)
		} else {
			f.log().Info("spec skill-pack unseeded",
				"skills_dir", skillsDir,
				"removed", len(res.Removed),
				"missing", len(res.Missing),
				"preserved_user", len(res.PreservedUser),
				"preserved_modified", len(res.PreservedModified),
				"failed", len(res.Failed))
		}
		if res, err := specs.UnseedAgents(agentsDir, f.log()); err != nil {
			f.log().Warn("spec agent-pack unseed failed",
				"agents_dir", agentsDir, "error", err)
		} else {
			f.log().Info("spec agent-pack unseeded",
				"agents_dir", agentsDir,
				"removed", len(res.Removed),
				"missing", len(res.Missing),
				"preserved_user", len(res.PreservedUser),
				"preserved_modified", len(res.PreservedModified),
				"failed", len(res.Failed))
		}
	}

	f.invalidateSkillCache()
	f.invalidateAgentCache()
}
