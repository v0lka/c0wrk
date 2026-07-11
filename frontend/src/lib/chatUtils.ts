import type { ChatMessageUI, MessageType, DisplayItem, GroupedMessages } from '@/types/messages'
import type { ChatMessage, PlanGroup, PlanItem } from '@/types/models'
import { reconstructContent, buildHistoryId, collapseThoughts, extractMeta } from './chatUtilsHelpers'
import {
  handlePlanStepStart, handlePlanStepComplete, handleSubAgentLaunch, handleSubAgentComplete,
  handleReflection, handleStepTodoUpdate,
  handleToolCall, handleToolResult, handleActionMessage,
  type ToolLike, type StepLikeItem, type ActionDisplayItem,
} from './chatGroupingHandlers'

export { collapseThoughts } from './chatUtilsHelpers'

// Role-to-type mapping for history conversion.
// The key type is narrowed to known backend roles for compile-time safety (S-35).
type ChatRole = 'user' | 'assistant' | 'tool_call' | 'tool_result'
  | 'routing' | 'reflection' | 'plan' | 'error'
  | 'thought' | 'thinking' | 'step_done'
  | 'plan_step_start' | 'plan_step_complete'
  | 'retry' | 'step_retry'
  | 'subagent_launch' | 'subagent_complete'
  | 'tool_confirm' | 'ask_user' | 'task_cancelled'
  | 'status' | 'task_resumed'
  | 'task_failed_resumable' | 'step_limit' | 'context_compaction'
  | 'step_todo_update' | 'memory_read' | 'plan_review'

export const roleToType: Record<ChatRole, MessageType> = {
  user: 'user', assistant: 'assistant', tool_call: 'tool_call', tool_result: 'tool_result',
  routing: 'routing', reflection: 'reflection', plan: 'plan', error: 'error',
  thought: 'thought', thinking: 'thinking', step_done: 'step_done',
  plan_step_start: 'plan_step_start', plan_step_complete: 'plan_step_complete',
  retry: 'retry', step_retry: 'step_retry',
  subagent_launch: 'subagent_launch', subagent_complete: 'subagent_complete',
  tool_confirm: 'tool_confirm', ask_user: 'ask_user', task_cancelled: 'error',
  status: 'status', task_resumed: 'task_resumed',
  // task_failed_resumable and step_limit keep their own types on reload so
  // groupMessages can render the Resume / decision affordances in the chat
  // stream (their staleness is reconciled against GetSessionRuntimeStatus
  // and GetPendingActions after load).
  task_failed_resumable: 'task_failed_resumable', step_limit: 'step_limit', context_compaction: 'context_compaction',
  step_todo_update: 'step_todo_update',
  memory_read: 'memory_read',
  plan_review: 'plan_review',
}

/** Convert a persisted ChatMessage to ChatMessageUI, matching live event shape. */
export function chatMessageToUI(msg: ChatMessage): ChatMessageUI {
  let metadata: Record<string, unknown> | undefined
  // metadata field comes from the backend as number[] (Go json.RawMessage → Wails byte array).
  // Deserialize to a structured object; also handles string metadata for test compatibility.
  if (msg.metadata) {
    try {
      if (Array.isArray(msg.metadata) && msg.metadata.length > 0) {
        const str = String.fromCharCode(...msg.metadata)
        metadata = JSON.parse(str)
      } else if (typeof msg.metadata === 'string') {
        metadata = JSON.parse(msg.metadata)
      } else {
        // Already a plain object (test-only path)
        metadata = msg.metadata as unknown as Record<string, unknown>
      }
    } catch { metadata = undefined }
  }
  const msgType = roleToType[msg.role as ChatRole] || 'assistant'
  const timestamp = msg.created_at ? new Date(msg.created_at).getTime() : 0
  const content = reconstructContent(msg.role, msg.content, metadata)
  const id = buildHistoryId(msg.id, msg.role, metadata, timestamp)
  return { id, sessionId: msg.session_id, type: msgType, content, metadata, timestamp }
}

/** Transform a flat list of ChatMessageUI into a display-ready tree. */
export function groupMessages(messages: ChatMessageUI[]): GroupedMessages {
  const items: DisplayItem[] = []
  const openSteps = new Map<string, StepLikeItem>()
  const stepIdCounts = new Map<string, number>()
  const stepIndexMap = new Map<string, { num: number; title: string; description: string }>()
  const toolItemsByKey = new Map<string, ToolLike>()
  // Index of tool cards by their message id, each with the container array
  // (root items or a step/subagent's children) they were pushed into. Used to
  // anchor a resolved tool_confirm directly beneath the tool call that
  // triggered it (linked via tool_msg_id).
  const toolItemById = new Map<string, { item: ToolLike; container: DisplayItem[] }>()
  const pendingResults = new Map<string, { result?: string; resultLen?: number; error?: boolean }>()
  // Track the latest checklist per key (stepId || '' for standalone) so
  // earlier superseded updates can be removed from their container, and
  // active (incomplete) checklists can be sunk to the end of their container.
  const checklistsByKey = new Map<string, { item: DisplayItem & { kind: 'checklist' }; container: DisplayItem[] }>()
  // Unresolved pending actions (tool_confirm, ask_user, step_limit,
  // plan_review, resume_action) tracked in stream order so the sinking
  // post-pass can move them to the very bottom of the chat. Resolved actions
  // are pushed into items but NOT tracked here — they stay at their stream
  // position, like settled checklists.
  const activeActions: ActionDisplayItem[] = []

  const pushItem = (item: DisplayItem, planStepId?: string): DisplayItem[] => {
    const container = planStepId ? openSteps.get(planStepId) : null
    if (container) { container.children.push(item); return container.children }
    items.push(item)
    return items
  }

  for (const msg of messages) {
    const meta = extractMeta(msg)
    const planStepId = meta?.plan_step_id as string | undefined

    if (msg.type === 'plan') {
      const steps = (meta?.steps as Array<{ id?: string; summary?: string; description: string }>) || []
      steps.forEach((s, i) => {
        if (s.id) stepIndexMap.set(s.id, { num: i + 1, title: s.summary?.trim() || s.description, description: s.description })
      })
      continue
    }
    if (msg.type === 'plan_step_start') { handlePlanStepStart(msg, meta, stepIndexMap, stepIdCounts, openSteps, items); continue }
    if (msg.type === 'plan_step_complete') { handlePlanStepComplete(meta, openSteps); continue }
    if (msg.type === 'subagent_launch') { handleSubAgentLaunch(msg, meta, openSteps, items); continue }
    if (msg.type === 'subagent_complete') { handleSubAgentComplete(meta, openSteps); continue }
    if (msg.type === 'reflection') { handleReflection(msg, meta, openSteps, items); continue }
    if (msg.type === 'step_todo_update') { handleStepTodoUpdate(msg, meta, openSteps, items, checklistsByKey); continue }

    switch (msg.type) {
      case 'user': pushItem({ kind: 'user', message: msg }, planStepId); break
      case 'assistant': pushItem({ kind: 'assistant', message: msg }, planStepId); break
      case 'thought':
        pushItem({ kind: 'thought', id: msg.id, stepNum: (meta?.step_num as number) ?? 0, content: msg.content, reasoning: meta?.reasoning as string | undefined }, planStepId)
        break
      case 'tool_call': handleToolCall(msg, meta, planStepId, stepIndexMap, toolItemsByKey, pendingResults, pushItem, toolItemById); break
      case 'tool_result': handleToolResult(meta, toolItemsByKey, pendingResults); break
      case 'tool_confirm': case 'ask_user': case 'task_failed_resumable': case 'step_limit': case 'plan_review':
        handleActionMessage(msg, meta, items, activeActions, toolItemById); break
      case 'context_compaction': {
        const bp = (meta?.before_percent as number) ?? 0
        const ap = (meta?.after_percent as number) ?? 0
        pushItem({ kind: 'context_compaction', id: msg.id, beforePercent: Math.round(bp), afterPercent: Math.round(ap) }, planStepId)
        break
      }
      case 'memory_read':
        pushItem({ kind: 'memory_read', id: msg.id, content: msg.content, stepNum: meta?.step_num as number | undefined }, planStepId)
        break
      case 'error': pushItem({ kind: 'error', message: msg }, planStepId); break
      case 'routing': case 'retry': case 'step_retry':
        pushItem({ kind: 'service', id: msg.id, variant: msg.type as 'routing' | 'retry' | 'step_retry', content: msg.content, metadata: meta }, planStepId); break
      case 'status':
        pushItem({ kind: 'service', id: msg.id, variant: 'status', content: msg.content, metadata: meta }, planStepId); break
      case 'step_done': case 'thinking': case 'task_resumed': break
      default: break
    }
  }

  // Sinking: move active (incomplete) checklists to the end of their container
  // so they stay visible at the bottom while new content streams in above them.
  // Settled (all-checked) checklists remain at their stream position.
  for (const { item, container } of checklistsByKey.values()) {
    if (!item.active) continue
    const idx = container.indexOf(item)
    if (idx !== -1) {
      container.splice(idx, 1)
      container.push(item)
    }
  }

  // Keep only the last unresolved plan_review — earlier unresolved ones are
  // superseded by a replan cycle (plan → reject → replan → new plan). Showing
  // all of them would stack stale approval panels at the bottom of the chat.
  // Resolved plan_reviews remain at their stream position.
  const unresolvedPlanReviews = activeActions.filter(a => a.kind === 'plan_review')
  if (unresolvedPlanReviews.length > 1) {
    const keep = unresolvedPlanReviews[unresolvedPlanReviews.length - 1]!
    for (const pr of unresolvedPlanReviews) {
      if (pr === keep) continue
      const idx = items.indexOf(pr)
      if (idx !== -1) items.splice(idx, 1)
    }
    for (let i = activeActions.length - 1; i >= 0; i--) {
      if (activeActions[i]!.kind === 'plan_review' && activeActions[i] !== keep) {
        activeActions.splice(i, 1)
      }
    }
  }

  // Action sinking: move unresolved pending actions to the very end of the
  // root items so they stay visible at the bottom of the chat (below any sunk
  // checklists) while new content streams in above them. Resolved actions are
  // not tracked here and remain at their stream position.
  for (const action of activeActions) {
    const idx = items.indexOf(action)
    if (idx !== -1) {
      items.splice(idx, 1)
      items.push(action)
    }
  }

  for (const item of items) {
    if (item.kind === 'plan_step' || item.kind === 'subagent') {
      item.children = collapseThoughts(item.children)
    }
  }
  return { items: collapseThoughts(items) }
}

/** Actions interface for plan store dependency injection. */
export interface PlanStoreActions {
  clearPlan: () => void
  setPlan: (plan: PlanGroup) => void
}

/** Rebuild planStore from persisted history messages (called after history load). */
export function rebuildPlanFromHistory(messages: ChatMessageUI[], store: PlanStoreActions): void {
  store.clearPlan()

  // Find the last plan message to build the PlanGroup
  let planMsg: ChatMessageUI | undefined
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i]!.type === 'plan') { planMsg = messages[i]; break }
  }
  if (!planMsg) return

  const meta = planMsg.metadata as Record<string, unknown> | undefined
  const steps = (meta?.steps as Array<{ id?: string; description: string; summary?: string; depends_on?: string[] }>) || []
  if (steps.length === 0) return

  const items: PlanItem[] = steps.map((step, i) => ({
    id: step.id ?? `step-${i}`,
    title: step.summary?.trim() || step.description,
    description: step.description,
    summary: step.summary,
    status: 'pending' as const,
    dependsOn: step.depends_on ?? [],
  }))

  const group: PlanGroup = {
    id: planMsg.id,
    items,
    progress: meta?.progress as number | undefined,
    completedCount: 0,
    failedCount: 0,
    totalCount: items.length,
  }

  // Replay plan_step_start / plan_step_complete events after the plan message
  const planIdx = messages.indexOf(planMsg)
  for (let i = planIdx + 1; i < messages.length; i++) {
    const msg = messages[i]!
    const msgMeta = msg.metadata as Record<string, unknown> | undefined
    if (msg.type === 'plan_step_start') {
      const stepId = msgMeta?.step_id as string | undefined
      const item = stepId ? group.items.find(it => it.id === stepId) : undefined
      if (item) item.status = 'running'
    } else if (msg.type === 'plan_step_complete') {
      const stepId = msgMeta?.step_id as string | undefined
      const success = msgMeta?.success as boolean | undefined
      const duration = msgMeta?.duration as number | undefined
      const item = stepId ? group.items.find(it => it.id === stepId) : undefined
      if (item) {
        item.status = success ? 'completed' : 'failed'
        if (duration != null) item.duration = duration
      }
    }
  }

  group.completedCount = group.items.filter(it => it.status === 'completed').length
  group.failedCount = group.items.filter(it => it.status === 'failed').length

  store.setPlan(group)
}
