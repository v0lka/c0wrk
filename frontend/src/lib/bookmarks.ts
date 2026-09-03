// Chat bookmark helpers — stable event keys, default titles, and item lookup.

import type { DisplayItem } from '@/types/messages'

/**
 * Stable identifier for a DisplayItem, matching the key used by
 * ChatMessageRenderer to dedupe and correlate rendered items. Persisted as the
 * bookmark's event_key so navigation and preview can map back to the item.
 */
export function bookmarkKey(item: DisplayItem): string {
  if ('message' in item) return item.message.id
  return item.id
}

/**
 * Collapse whitespace to a single short line for list display. No fixed
 * character cap: visual truncation is CSS-based (ellipsis at panel width) in
 * BookmarksPanel, so wide panels show more text than narrow ones.
 */
function collapseTitle(text: string): string {
  return text.replace(/\s+/g, ' ').trim()
}

/** Derive a sensible default title for a bookmark created from a chat item. */
export function bookmarkDefaultTitle(item: DisplayItem): string {
  switch (item.kind) {
    case 'user':
    case 'assistant':
      return collapseTitle(item.message.content) || (item.kind === 'user' ? 'Message' : 'Answer')
    case 'thought':
      return item.reasoning && item.reasoning.trim()
        ? collapseTitle(item.reasoning)
        : collapseTitle(item.content) || 'Thought'
    case 'thought_group':
      return item.thoughts.length === 1
        ? collapseTitle(item.thoughts[0]?.reasoning ?? item.thoughts[0]?.content ?? '')
        : `${item.thoughts.length} thoughts`
    case 'tool':
      return item.toolName || 'Tool'
    case 'tool_confirm':
      return collapseTitle(item.message.content) || 'Confirm tool'
    case 'ask_user':
      return collapseTitle(item.message.content) || 'Question'
    case 'step_limit':
      return collapseTitle(item.message.content) || 'Step limit'
    case 'resume_action':
      return collapseTitle(item.message.content) || 'Resume'
    case 'error':
      return collapseTitle(item.message.content) || 'Error'
    case 'service':
      return collapseTitle(item.content) || 'Status'
    case 'plan_step':
      return `Step ${item.stepNum}: ${item.title}`
    case 'subagent':
      return item.title || `Delegated: ${item.stepId}`
    case 'reflection':
      return collapseTitle(item.summary) || 'Reflection'
    case 'step_finish':
      return item.stepNum ? `Finished step ${item.stepNum}` : 'Finished'
    case 'context_compaction':
      return 'Context compaction'
    case 'memory_read':
      return collapseTitle(item.content) || 'Memory read'
    case 'plan_review':
      return 'Plan review'
    case 'review_prompt':
      return 'Code review'
    case 'goal_proposal':
      return 'Proposed goal'
    case 'checklist':
      return 'Checklist'
  }
}

/** Recursively flatten a DisplayItem tree (plan_step/subagent children). */
export function flattenDisplayItems(items: DisplayItem[]): DisplayItem[] {
  const out: DisplayItem[] = []
  for (const item of items) {
    out.push(item)
    if (item.kind === 'plan_step' || item.kind === 'subagent') {
      out.push(...flattenDisplayItems(item.children))
    }
  }
  return out
}

/** Index a DisplayItem tree by {@link bookmarkKey}. */
export function indexDisplayItems(items: DisplayItem[]): Map<string, DisplayItem> {
  const map = new Map<string, DisplayItem>()
  for (const item of flattenDisplayItems(items)) {
    map.set(bookmarkKey(item), item)
  }
  return map
}
