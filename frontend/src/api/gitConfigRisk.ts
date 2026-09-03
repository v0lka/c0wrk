// Git-config intake risk warning subscription.
//
// The backend emits the global `project:git_config_risk` event when a project
// switch or an added work directory opens a repository whose .git/config
// carries dangerous keys (command-bearing filters, merge drivers, textconv,
// fsmonitor, hooksPath, include directives, ...). See
// backend/frontend_api_gitconfig_risk.go. A clean repository emits nothing.
//
// This module is the single subscription path for that event — UI code never
// subscribes to the raw event name (one import path for backend calls).

import { onGlobalEvent } from './runtime'
import type { GitConfigRiskData } from '@/types/events'
import { isGitConfigRiskData } from '@/types/events'

/** Subscribe to `project:git_config_risk` (dangerous git config detected in a
 *  newly opened project or added work directory). Payloads are validated with
 *  isGitConfigRiskData, so malformed emissions are dropped rather than
 *  crashing the handler. Returns an unsubscribe function. */
export function onGitConfigRisk(cb: (data: GitConfigRiskData) => void): () => void {
  return onGlobalEvent('project:git_config_risk', (data) => {
    if (data && isGitConfigRiskData(data)) cb(data)
  })
}
