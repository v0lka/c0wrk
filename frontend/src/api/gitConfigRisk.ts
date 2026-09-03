// Git-config intake risk warning subscription.
//
// The backend emits the global `project:git_config_risk` event when a project
// switch or an added work directory opens a repository whose .git/config
// carries dangerous keys (command-bearing filters, merge drivers, textconv,
// fsmonitor, hooksPath, include directives, ...). See
// backend/frontend_api_gitconfig_risk.go. A clean repository emits nothing.
//
// This module is the single subscription path for that event — UI code never
// subscribes to the raw event name (one import path for backend calls) — and
// the single RPC path for the trusted-repository list that suppresses the
// warning (Settings → Security → Trusted repos, and the toast's "Trust this
// repo" action).

import { onGlobalEvent, getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isGitConfigRiskData } from '@/types/events'
import { isArrayOf } from '@/types/guards'
import type { GitConfigRiskData } from '@/types/events'

const isString = (v: unknown): v is string => typeof v === 'string'

/** Subscribe to `project:git_config_risk` (dangerous git config detected in a
 *  newly opened project or added work directory). Payloads are validated with
 *  isGitConfigRiskData, so malformed emissions are dropped rather than
 *  crashing the handler. Returns an unsubscribe function. */
export function onGitConfigRisk(cb: (data: GitConfigRiskData) => void): () => void {
  return onGlobalEvent('project:git_config_risk', (data) => {
    if (data && isGitConfigRiskData(data)) cb(data)
  })
}

/** Repository roots the user marked trusted — their intake warning is
 *  suppressed. Always returns a string[] (the backend never sends null). */
export async function getTrustedGitRepos(): Promise<string[]> {
  try {
    const app = getApp()
    const result = await app.GetTrustedGitRepos()
    if (!isArrayOf(result, isString)) {
      throw new Error('getTrustedGitRepos: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to list trusted git repositories:', err)
    throw err
  }
}

/** Mark a repository root as trusted (suppresses its intake warning; the
 *  spawn-layer git neutralization stays fully in force). `path` must be the
 *  exact absolute path the warning carried (risk.path). */
export async function trustGitRepo(path: string): Promise<void> {
  try {
    const app = getApp()
    await app.TrustGitRepo(path)
  } catch (err) {
    logger.error('Failed to trust git repository:', err)
    throw err
  }
}

/** Remove a repository root from the trusted list — its intake warning
 *  returns on the next open. Idempotent for absent entries. */
export async function removeTrustedGitRepo(path: string): Promise<void> {
  try {
    const app = getApp()
    await app.RemoveTrustedGitRepo(path)
  } catch (err) {
    logger.error('Failed to remove trusted git repository:', err)
    throw err
  }
}
