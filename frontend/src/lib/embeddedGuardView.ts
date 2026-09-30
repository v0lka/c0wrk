// Pure helpers for the embedded-LLM compatibility guard record (no React, no
// DOM — the same split as lib/embeddedLLMLabels / lib/gitGraphRender, so the
// rules stay unit-testable without a renderer).

import type { EmbeddedLLMGuard } from '@/api/embedded'

/** "<repo>#<n>" → the upstream issue URL. Returns null when the citation does
 *  not have the expected shape, so a malformed citation renders as plain text
 *  instead of a broken link. */
export function guardIssueURL(issue: string): string | null {
  const match = /^([\w.-]+)\/([\w.-]+)#(\d+)$/.exec(issue)
  if (!match) return null
  return `https://github.com/${match[1]}/${match[2]}/issues/${match[3]}`
}

/** Whether the decision degraded the install without being acted on — the
 *  "works, but not the way the resolver would prefer" case that renders as a
 *  warning rather than muted context. */
export function guardIsUnapplied(decision: EmbeddedLLMGuard): boolean {
  return !decision.applied
}
