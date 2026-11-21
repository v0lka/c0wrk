// Tool manager install-status RPC bridge — thin wrapper over
// GetToolInstallStatus (backend/frontend_api_toolmanager.go), the
// transport-neutral twin of `tool_manager:start` that a webview can miss
// (Wails drops events emitted before the subscription). See
// specs/contracts/desktop-frontend.md ("Tool Manager") and
// specs/domains/frontend/loading-states.md for the splash recovery it serves.

import { getApp } from './runtime'
import { logger } from '@/lib/logger'

/** Splash-denominator snapshot of the startup install flow; the wire shape
 *  is ToolInstallStatusDTO (pending is always a non-null array). */
export interface ToolInstallStatus {
  readonly active: boolean
  readonly pending: readonly ToolInstallPendingTool[]
  readonly tools_total: number
}

export interface ToolInstallPendingTool {
  readonly name: string
  readonly version: string
}

function isToolInstallStatus(d: unknown): d is ToolInstallStatus {
  if (typeof d !== 'object' || d === null) return false
  const o = d as Record<string, unknown>
  if (typeof o.active !== 'boolean' || typeof o.tools_total !== 'number') return false
  if (!Array.isArray(o.pending)) return false
  return o.pending.every((t) =>
    typeof t === 'object' && t !== null &&
    typeof (t as Record<string, unknown>).name === 'string' &&
    typeof (t as Record<string, unknown>).version === 'string',
  )
}

/**
 * Read the install-flow snapshot. Resolves null (never rejects) when the
 * transport is unavailable or the shape is unexpected — a failed read is the
 * caller's signal to fall back to the event-driven behavior, so startup must
 * not surface this as an error.
 */
export async function getToolInstallStatus(): Promise<ToolInstallStatus | null> {
  try {
    const app = getApp()
    const result = await app.GetToolInstallStatus()
    if (!isToolInstallStatus(result)) {
      logger.warn('getToolInstallStatus: unexpected response shape, treating as no snapshot', result)
      return null
    }
    return result
  } catch (err) {
    logger.debug('getToolInstallStatus unavailable, falling back to event-driven splash:', err)
    return null
  }
}
