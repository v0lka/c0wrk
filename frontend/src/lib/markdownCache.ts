/**
 * Bounded LRU cache of parsed-markdown render nodes.
 *
 * ReactMarkdown runs the remark/rehype pipeline on every render. The chat
 * re-renders the whole transcript whenever one message changes, so without a
 * cache every assistant message would re-run that pipeline each time. Caching
 * the mounted `<ReactMarkdown>` ELEMENT by (content, resolution variant) lets
 * React bail out of re-rendering that subtree — an identical element carries an
 * identical `props` object, so `beginWork` short-circuits and the parse never
 * runs again.
 *
 * Lives in its own module (not markdownConfig.tsx) because markdownConfig also
 * exports the `Markdown` component — mixing component and non-component exports
 * breaks React Fast Refresh (react-refresh/only-export-components).
 */

import type { ReactNode } from 'react'

/** Maximum number of parsed-markdown nodes retained (LRU). */
export const MARKDOWN_NODE_CACHE_MAX = 200

const cache = new Map<string, ReactNode>()
let hits = 0
let misses = 0

/**
 * Return the cached render node for `key`, creating (and caching) it via
 * `create` on a miss. Recency is refreshed on a hit so the LRU evicts the
 * least-recently-used entry once the cap is exceeded.
 */
export function getMarkdownNode(key: string, create: () => ReactNode): ReactNode {
  const cached = cache.get(key)
  if (cached !== undefined) {
    hits++
    // Map preserves insertion order, so the first key is the LRU.
    cache.delete(key)
    cache.set(key, cached)
    return cached
  }
  misses++
  const node = create()
  cache.set(key, node)
  if (cache.size > MARKDOWN_NODE_CACHE_MAX) {
    const oldest = cache.keys().next().value
    if (oldest !== undefined) cache.delete(oldest)
  }
  return node
}

/** Parsed-markdown cache counters and current size (test observability). */
export function markdownCacheStats(): { hits: number; misses: number; size: number } {
  return { hits, misses, size: cache.size }
}

/** Clear the parsed-markdown cache and its counters. */
export function resetMarkdownCache(): void {
  cache.clear()
  hits = 0
  misses = 0
}
