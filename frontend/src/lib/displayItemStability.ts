/**
 * Structural identity helpers for {@link DisplayItem} trees.
 *
 * `groupMessages` rebuilds its whole item tree on every store change — a new
 * message, a resolved confirmation, a streamed tail. Each wrapper object is
 * therefore brand new on each rebuild, which defeats the `React.memo`
 * comparators that keep an unrelated message from re-rendering (and re-parsing
 * its Markdown) when a single message changes.
 *
 * The two helpers here attack the problem from opposite ends:
 *
 *  - {@link areDisplayItemsEqual} decides whether two items render identical
 *    output. It compares the item's stable key plus its rendered payload —
 *    NEVER object identity — so it stays correct when the wrappers are fresh.
 *    It is the comparator the memoized chat blocks pass to `React.memo`.
 *  - {@link stabilizeDisplayItems} reuses the PREVIOUS item object (and its
 *    children) whenever {@link areDisplayItemsEqual} reports no change, so a
 *    re-grouped list keeps referential identity for the items that did not
 *    change. That makes the plain `React.memo` blocks that rely on identity
 *    (ToolCard, ThoughtBlock, PlanApprovalPanel) skip re-rendering too.
 *
 * Correctness rests on one invariant of the chat store: an UNCHANGED message
 * keeps its object identity across store updates (the reducers spread the
 * index and only replace the mutated entry). A message-backed item is thus
 * "equal" exactly when its `message` reference is unchanged.
 */

import type { ChatMessageUI, DisplayItem } from '@/types/messages'
import { bookmarkKey } from './bookmarks'

type MessageBackedItem = { message: ChatMessageUI }
type ToolItem = Extract<DisplayItem, { kind: 'tool' }>
type ServiceItem = Extract<DisplayItem, { kind: 'service' }>
type ThoughtItem = Extract<DisplayItem, { kind: 'thought' }>
type ThoughtGroupItem = Extract<DisplayItem, { kind: 'thought_group' }>
type ChecklistItem = Extract<DisplayItem, { kind: 'checklist' }>
type ReflectionItem = Extract<DisplayItem, { kind: 'reflection' }>
type StepFinishItem = Extract<DisplayItem, { kind: 'step_finish' }>
type CompactionItem = Extract<DisplayItem, { kind: 'context_compaction' }>
type MemoryReadItem = Extract<DisplayItem, { kind: 'memory_read' }>
type PlanStepItem = Extract<DisplayItem, { kind: 'plan_step' }>
type SubAgentItem = Extract<DisplayItem, { kind: 'subagent' }>
type GoalProposalItem = Extract<DisplayItem, { kind: 'goal_proposal' }>

function stringArraysEqual(a: readonly string[], b: readonly string[]): boolean {
  if (a === b) return true
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false
  }
  return true
}

function thoughtsEqual(
  a: ReadonlyArray<{ content: string; reasoning?: string }>,
  b: ReadonlyArray<{ content: string; reasoning?: string }>,
): boolean {
  if (a === b) return true
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    const x = a[i]!
    const y = b[i]!
    if (x.content !== y.content || x.reasoning !== y.reasoning) return false
  }
  return true
}

function checklistItemsEqual(
  a: ReadonlyArray<{ text: string; checked: boolean }>,
  b: ReadonlyArray<{ text: string; checked: boolean }>,
): boolean {
  if (a === b) return true
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    const x = a[i]!
    const y = b[i]!
    if (x.text !== y.text || x.checked !== y.checked) return false
  }
  return true
}

function childrenEqual(a: readonly DisplayItem[], b: readonly DisplayItem[]): boolean {
  if (a === b) return true
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    if (!areDisplayItemsEqual(a[i]!, b[i]!)) return false
  }
  return true
}

/**
 * Whether two display items would render identical output. Compares by kind,
 * stable key, and rendered payload — never by object identity — so a fresh
 * wrapper around unchanged data is reported equal (and its block can skip the
 * re-render). Recurses into plan-step / subagent children.
 */
export function areDisplayItemsEqual(a: DisplayItem, b: DisplayItem): boolean {
  if (a === b) return true
  if (a.kind !== b.kind) return false

  switch (a.kind) {
    case 'user':
    case 'assistant':
    case 'error':
    case 'tool_confirm':
    case 'ask_user':
    case 'step_limit':
    case 'resume_action':
    case 'plan_review':
    case 'review_prompt':
    case 'autonomy_decision': {
      const ia = a as MessageBackedItem
      const ib = b as MessageBackedItem
      return ia.message === ib.message
    }
    case 'goal_proposal': {
      const ia = a as GoalProposalItem
      const ib = b as GoalProposalItem
      return (
        ia.message === ib.message &&
        ia.condition === ib.condition &&
        ia.verify === ib.verify &&
        ia.verification_mode === ib.verification_mode
      )
    }
    case 'tool': {
      const ia = a as ToolItem
      const ib = b as ToolItem
      return (
        ia.id === ib.id &&
        ia.toolName === ib.toolName &&
        ia.args === ib.args &&
        ia.parsedArgs === ib.parsedArgs &&
        ia.result === ib.result &&
        ia.resultLen === ib.resultLen &&
        ia.status === ib.status &&
        ia.source === ib.source &&
        ia.attachmentName === ib.attachmentName
      )
    }
    case 'service': {
      const ia = a as ServiceItem
      const ib = b as ServiceItem
      return (
        ia.id === ib.id &&
        ia.variant === ib.variant &&
        ia.content === ib.content &&
        ia.metadata === ib.metadata
      )
    }
    case 'thought': {
      const ia = a as ThoughtItem
      const ib = b as ThoughtItem
      return (
        ia.id === ib.id &&
        ia.stepNum === ib.stepNum &&
        ia.content === ib.content &&
        ia.reasoning === ib.reasoning
      )
    }
    case 'thought_group': {
      const ia = a as ThoughtGroupItem
      const ib = b as ThoughtGroupItem
      return ia.id === ib.id && thoughtsEqual(ia.thoughts, ib.thoughts)
    }
    case 'checklist': {
      const ia = a as ChecklistItem
      const ib = b as ChecklistItem
      return (
        ia.id === ib.id &&
        ia.stepId === ib.stepId &&
        ia.active === ib.active &&
        checklistItemsEqual(ia.items, ib.items)
      )
    }
    case 'reflection': {
      const ia = a as ReflectionItem
      const ib = b as ReflectionItem
      return (
        ia.id === ib.id &&
        ia.summary === ib.summary &&
        ia.suggestedAction === ib.suggestedAction &&
        ia.rootCause === ib.rootCause &&
        ia.failureAnalysis === ib.failureAnalysis &&
        ia.actionPlan === ib.actionPlan &&
        ia.reasoning === ib.reasoning &&
        ia.attempt === ib.attempt &&
        ia.maxAttempts === ib.maxAttempts &&
        stringArraysEqual(ia.hypotheses, ib.hypotheses)
      )
    }
    case 'step_finish': {
      const ia = a as StepFinishItem
      const ib = b as StepFinishItem
      return ia.id === ib.id && ia.stepNum === ib.stepNum
    }
    case 'context_compaction': {
      const ia = a as CompactionItem
      const ib = b as CompactionItem
      return (
        ia.id === ib.id &&
        ia.beforePercent === ib.beforePercent &&
        ia.afterPercent === ib.afterPercent
      )
    }
    case 'memory_read': {
      const ia = a as MemoryReadItem
      const ib = b as MemoryReadItem
      return ia.id === ib.id && ia.content === ib.content && ia.stepNum === ib.stepNum
    }
    case 'plan_step': {
      const ia = a as PlanStepItem
      const ib = b as PlanStepItem
      return (
        ia.id === ib.id &&
        ia.stepId === ib.stepId &&
        ia.stepNum === ib.stepNum &&
        ia.title === ib.title &&
        ia.description === ib.description &&
        ia.status === ib.status &&
        ia.duration === ib.duration &&
        ia.error === ib.error &&
        (ia.isRetry ?? false) === (ib.isRetry ?? false) &&
        childrenEqual(ia.children, ib.children)
      )
    }
    case 'subagent': {
      const ia = a as SubAgentItem
      const ib = b as SubAgentItem
      return (
        ia.id === ib.id &&
        ia.stepId === ib.stepId &&
        ia.title === ib.title &&
        ia.description === ib.description &&
        ia.status === ib.status &&
        ia.duration === ib.duration &&
        ia.error === ib.error &&
        childrenEqual(ia.children, ib.children)
      )
    }
  }
}

/**
 * Reuse `prev` item objects in `next` wherever nothing changed, so a
 * re-grouped list keeps referential identity for the items it did not touch
 * (and their children). Items whose key is new, whose kind changed, or whose
 * payload differs are taken from `next` unchanged. Best-effort: a missed reuse
 * only costs an extra render — it can never show stale data, because a reuse
 * requires {@link areDisplayItemsEqual} to hold.
 */
export function stabilizeDisplayItems(prev: DisplayItem[], next: DisplayItem[]): DisplayItem[] {
  if (prev.length === 0 || next.length === 0) return next
  const prevByKey = new Map<string, DisplayItem>()
  for (const item of prev) prevByKey.set(bookmarkKey(item), item)
  const out: DisplayItem[] = new Array<DisplayItem>(next.length)
  for (let i = 0; i < next.length; i++) {
    out[i] = stabilizeItem(prevByKey, next[i]!)
  }
  return out
}

function stabilizeItem(prevByKey: Map<string, DisplayItem>, next: DisplayItem): DisplayItem {
  const prev = prevByKey.get(bookmarkKey(next))
  if (!prev || prev.kind !== next.kind) return next
  if (areDisplayItemsEqual(prev, next)) return prev
  // A plan-step / subagent whose own fields or a descendant changed still gets
  // a fresh wrapper, but its unchanged children are reused so only the affected
  // branch re-renders.
  if (next.kind === 'plan_step' && prev.kind === 'plan_step') {
    return { ...next, children: stabilizeDisplayItems(prev.children, next.children) }
  }
  if (next.kind === 'subagent' && prev.kind === 'subagent') {
    return { ...next, children: stabilizeDisplayItems(prev.children, next.children) }
  }
  return next
}
