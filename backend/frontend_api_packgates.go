// Sidebar surface gates: SetResearchEnabled / SetSpecsEnabled persist the
// config research.enabled / specs.enabled toggles (Settings → General) and
// apply their pack side-effect in the same beat — enabling seeds the
// surface's packs, disabling unseeds them (seedGlobalPacks consults the same
// gates at launch, so a disabled surface stays unseeded across restarts).
// See the sidebar-packs ADR and specs/domains/{research,specs}.md.
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
// to match `enabled`. Callers must hold configMu; researchSeedMu orders this
// against the launch seed (configMu → researchSeedMu everywhere:
// seedGlobalPacks reads the gates BEFORE taking researchSeedMu, so the
// nesting never inverts). Per-pack failures are logged and never fatal — the
// persisted gate stays authoritative, exactly like the launch seed.
func (f *FrontendAPI) applyResearchPacksLocked(enabled bool) {
	skillsDir := config.SkillsDir(f.agentDir)
	agentsDir := config.AgentsDir(f.agentDir)

	if enabled {
		if res, err := research.SeedSkills(skillsDir, f.log()); err != nil {
			f.log().Warn("research surface enable: research skill-pack seeding failed",
				"skills_dir", skillsDir, "error", err)
		} else {
			f.log().Info("research surface enabled; research skill-pack seeded",
				"skills_dir", skillsDir,
				"seeded", len(res.Seeded), "updated", len(res.Updated),
				"current", len(res.Current), "preserved", len(res.Preserved))
		}
		if res, err := papers.SeedSkills(skillsDir, f.log()); err != nil {
			f.log().Warn("research surface enable: paper skill-pack seeding failed",
				"skills_dir", skillsDir, "error", err)
		} else {
			f.log().Info("research surface enabled; paper skill-pack seeded",
				"skills_dir", skillsDir,
				"seeded", len(res.Seeded), "updated", len(res.Updated),
				"current", len(res.Current), "preserved", len(res.Preserved))
		}
		if res, err := research.SeedAgents(agentsDir, f.log()); err != nil {
			f.log().Warn("research surface enable: research agent-pack seeding failed",
				"agents_dir", agentsDir, "error", err)
		} else {
			f.log().Info("research surface enabled; research agent-pack seeded",
				"agents_dir", agentsDir,
				"seeded", len(res.Seeded), "updated", len(res.Updated),
				"current", len(res.Current), "preserved", len(res.Preserved))
		}
	} else {
		if res, err := research.UnseedSkills(skillsDir, f.log()); err != nil {
			f.log().Warn("research surface disable: research skill-pack unseed failed",
				"skills_dir", skillsDir, "error", err)
		} else {
			f.log().Info("research surface disabled; research skill-pack unseeded",
				"skills_dir", skillsDir,
				"removed", len(res.Removed),
				"preserved_user", len(res.PreservedUser),
				"preserved_modified", len(res.PreservedModified))
		}
		if res, err := papers.UnseedSkills(skillsDir, f.log()); err != nil {
			f.log().Warn("research surface disable: paper skill-pack unseed failed",
				"skills_dir", skillsDir, "error", err)
		} else {
			f.log().Info("research surface disabled; paper skill-pack unseeded",
				"skills_dir", skillsDir,
				"removed", len(res.Removed),
				"preserved_user", len(res.PreservedUser),
				"preserved_modified", len(res.PreservedModified))
		}
		if res, err := research.UnseedAgents(agentsDir, f.log()); err != nil {
			f.log().Warn("research surface disable: research agent-pack unseed failed",
				"agents_dir", agentsDir, "error", err)
		} else {
			f.log().Info("research surface disabled; research agent-pack unseeded",
				"agents_dir", agentsDir,
				"removed", len(res.Removed),
				"preserved_user", len(res.PreservedUser),
				"preserved_modified", len(res.PreservedModified))
		}
	}

	// The seeded/unseeded directories are c0wrk-owned content; drop any
	// cached skill/agent listing so the next read rescans them.
	f.invalidateSkillCache()
	f.invalidateAgentCache()
}

// applySpecsPacksLocked seeds or unseeds the specs surface's packs — the
// spec skill pack plus the specs Subagent Profile — to match `enabled`,
// under the same contract as applyResearchPacksLocked.
func (f *FrontendAPI) applySpecsPacksLocked(enabled bool) {
	skillsDir := config.SkillsDir(f.agentDir)
	agentsDir := config.AgentsDir(f.agentDir)

	if enabled {
		if res, err := specs.SeedSkills(skillsDir, f.log()); err != nil {
			f.log().Warn("specs surface enable: spec skill-pack seeding failed",
				"skills_dir", skillsDir, "error", err)
		} else {
			f.log().Info("specs surface enabled; spec skill-pack seeded",
				"skills_dir", skillsDir,
				"seeded", len(res.Seeded), "updated", len(res.Updated),
				"current", len(res.Current), "preserved", len(res.Preserved))
		}
		if res, err := specs.SeedAgents(agentsDir, f.log()); err != nil {
			f.log().Warn("specs surface enable: spec agent-pack seeding failed",
				"agents_dir", agentsDir, "error", err)
		} else {
			f.log().Info("specs surface enabled; spec agent-pack seeded",
				"agents_dir", agentsDir,
				"seeded", len(res.Seeded), "updated", len(res.Updated),
				"current", len(res.Current), "preserved", len(res.Preserved))
		}
	} else {
		if res, err := specs.UnseedSkills(skillsDir, f.log()); err != nil {
			f.log().Warn("specs surface disable: spec skill-pack unseed failed",
				"skills_dir", skillsDir, "error", err)
		} else {
			f.log().Info("specs surface disabled; spec skill-pack unseeded",
				"skills_dir", skillsDir,
				"removed", len(res.Removed),
				"preserved_user", len(res.PreservedUser),
				"preserved_modified", len(res.PreservedModified))
		}
		if res, err := specs.UnseedAgents(agentsDir, f.log()); err != nil {
			f.log().Warn("specs surface disable: spec agent-pack unseed failed",
				"agents_dir", agentsDir, "error", err)
		} else {
			f.log().Info("specs surface disabled; spec agent-pack unseeded",
				"agents_dir", agentsDir,
				"removed", len(res.Removed),
				"preserved_user", len(res.PreservedUser),
				"preserved_modified", len(res.PreservedModified))
		}
	}

	f.invalidateSkillCache()
	f.invalidateAgentCache()
}
