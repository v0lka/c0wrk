import { createContext } from 'react'

/**
 * Propagates whether the current render scope is bookmarkable down into nested
 * collapsible renderers (PlanStepBlock / SubAgentBlock), so their children get
 * stars when rendering in the chat stream but stay star-free inside tooltip
 * previews (which render ChatMessageRenderer with bookmarkable=false).
 *
 * Kept in its own file (not alongside components in ChatMessageRenderer.tsx) so
 * the components file only exports components and fast-refresh keeps working.
 */
export const BookmarkableContext = createContext(true)
