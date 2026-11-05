// Sidebar surface-gate RPC bridge (config `research.enabled` /
// `specs.enabled`) — thin wrappers over the desktop App bindings, mirroring
// backend/frontend_api_packgates.go. SetResearchEnabled / SetSpecsEnabled
// persist the Settings → General toggles AND apply the pack side-effect in
// the same call — enabling seeds the surface's globally-seeded skills/agents,
// disabling unseeds them — so the sidebar tab and the skill catalog follow
// without a restart. A rejected toggle throws: the config was not persisted
// and the side-effect did not run, so the UI must roll back.

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
 * Toggle the specs sidebar surface gate — the setResearchEnabled contract
 * applied to the specs surface.
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
