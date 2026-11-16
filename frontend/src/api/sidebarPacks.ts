// Sidebar surface-gate RPC bridge (config `research.enabled` /
// `specs.enabled`) — thin wrappers over the desktop App bindings, mirroring
// backend/frontend_api_packgates.go. Both calls persist the Settings →
// General toggles AND apply their side-effect in the same call, so the
// sidebar tab and the skill catalog follow without a restart. SetResearchEnabled
// additionally owns the Research pack (enabling seeds its globally-seeded
// skills/agents, disabling unseeds them); SetSpecsEnabled is pure VISIBILITY —
// c0wrk ships no spec pack (ADR-126), so it never writes to or removes from
// disk. A rejected toggle throws: the config was not persisted and the
// side-effect did not run, so the UI must roll back.

import { getApp } from './runtime'
import { logger } from '@/lib/logger'

/**
 * Toggle the research sidebar surface gate. Throws on failure — unlike a
 * read, a rejected toggle must surface to the UI (rollback).
 */
export async function setResearchEnabled(enabled: boolean): Promise<void> {
  try {
    const app = getApp()
    await app.SetResearchEnabled(enabled)
  } catch (err) {
    logger.error('Failed to set research enabled:', err)
    throw err
  }
}

/**
 * Toggle the specs sidebar surface gate. Same persist-and-apply contract as
 * setResearchEnabled, minus the pack half: the Specs gate is pure visibility
 * (c0wrk ships no spec pack — ADR-126), so this call never seeds or unseeds
 * anything on disk.
 */
export async function setSpecsEnabled(enabled: boolean): Promise<void> {
  try {
    const app = getApp()
    await app.SetSpecsEnabled(enabled)
  } catch (err) {
    logger.error('Failed to set specs enabled:', err)
    throw err
  }
}
