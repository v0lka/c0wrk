// System-level process introspection API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'

/**
 * Fetch the resident set size (RSS) of the c0wrk desktop process, in bytes.
 *
 * getApp() throws synchronously when the Wails bindings are absent
 * (dev-frontend, vitest) and the RPC itself can reject on a backend failure —
 * both propagate to the caller, which treats them as "indicator unavailable"
 * and hides silently, so nothing is logged here per poll. Only a malformed
 * successful response (backend schema drift) is logged before rethrowing.
 */
export async function getProcessMemory(): Promise<number> {
  const app = getApp()
  const result = await app.GetProcessMemory()
  if (typeof result !== 'number' || !Number.isFinite(result) || result < 0) {
    logger.error('getProcessMemory: unexpected response shape', result)
    throw new Error('GetProcessMemory returned a non-numeric value')
  }
  return result
}
