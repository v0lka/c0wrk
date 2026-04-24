import type { ChatMessageUI, MessageType, DisplayItem, GroupedMessages } from '@/types/messages'
import type { ChatMessage, PlanGroup, PlanItem } from '@/types/models'
import { reconstructContent, buildHistoryId, collapseThoughts, extractMeta } from './chatUtilsHelpers'
import {
  handlePlanStepStart, handlePlanStepComplete, handleReflection,
  handleToolCall, handleToolResult, handleActionMessage,
  type ToolLike, type PlanStep,
} from './chatGroupingHandlers'
import { usePlanStore } from '@/stores/planStore'

export { collapseThoughts } from './chatUtilsHelpers'

// Role-to-type mapping for history conversion
export const roleToType: Record<string, MessageType> = {
  user: 'user', assistant: 'assistant', tool_call: 'tool_call', tool_result: 'tool_result',
  routing: 'routing', reflection: 'reflection', plan: 'plan', error: 'error',
  thought: 'thought', thinking: 'thinking', step_done: 'step_done',
  plan_step_start: 'plan_step_start', plan_step_complete: 'plan_step_complete',
  retry: 'retry', step_retry: 'step_retry',
  subagent_launch: 'subagent_launch', subagent_complete: 'subagent_complete',
  tool_confirm: 'tool_confirm', ask_user: 'ask_user', task_cancelled: 'error',
  status: 'status', task_resumed: 'task_resumed',
  task_failed_resumable: 'error', step_limit: 'assistant', context_compaction: 'assistant',
}

/** Convert a persisted ChatMessage to ChatMessageUI, matching live event shape. */
export function chatMessageToUI(msg: ChatMessage): ChatMessageUI {
  let metadata: Record<string, unknown> | undefined
  if (msg.metadata) {
    try {
      metadata = typeof msg.metadata === 'string' ? JSON.parse(msg.metadata) : msg.metadata
    } catch { metadata = undefined }
  }
  const msgType = roleToType[msg.role] || 'assistant'
  const timestamp = msg.created_at ? new Date(msg.created_at).getTime() : 0
  const content = reconstructContent(msg.role, msg.content, metadata)
  const id = buildHistoryId(msg.id, msg.role, metadata, timestamp)
  return { id, sessionId: msg.session_id, type: msgType, content, metadata, timestamp }
}

/** Transform a flat list of ChatMessageUI into a display-ready tree. */
export function groupMessages(messages: ChatMessageUI[]): GroupedMessages {
  const items: DisplayItem[] = []
  const pendingActions: DisplayItem[] = []
  const openSteps = new Map<string, PlanStep>()
  const stepIdCounts = new Map<string, number>()
  const stepIndexMap = new Map<string, { num: number; title: string; description: string }>()
  const toolItemsByKey = new Map<string, ToolLike>()
  const pendingResults = new Map<string, { result?: string; resultLen?: number; error?: boolean }>()

  const pushItem = (item: DisplayItem, planStepId?: string) => {
    const container = planStepId ? openSteps.get(planStepId) : null
    if (container) { container.children.push(item) } else { items.push(item) }
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
    if (msg.type === 'reflection') { handleReflection(msg, meta, openSteps, items); continue }

    switch (msg.type) {
      case 'user': pushItem({ kind: 'user', message: msg }, planStepId); break
      case 'assistant': pushItem({ kind: 'assistant', message: msg }, planStepId); break
      case 'thought':
        pushItem({ kind: 'thought', id: msg.id, stepNum: (meta?.step_num as number) ?? 0, content: msg.content, reasoning: meta?.reasoning as string | undefined }, planStepId)
        break
      case 'tool_call': handleToolCall(msg, meta, planStepId, stepIndexMap, toolItemsByKey, pendingResults, pushItem); break
      case 'tool_result': handleToolResult(meta, toolItemsByKey, pendingResults); break
      case 'tool_confirm': case 'ask_user': case 'task_failed_resumable': case 'step_limit':
        handleActionMessage(msg, meta, planStepId, pendingActions, pushItem); break
      case 'context_compaction': {
        const bp = (meta?.before_percent as number) ?? 0
        const ap = (meta?.after_percent as number) ?? 0
        pushItem({ kind: 'context_compaction', id: msg.id, beforePercent: Math.round(bp), afterPercent: Math.round(ap) }, planStepId)
        break
      }
      case 'error': pushItem({ kind: 'error', message: msg }, planStepId); break
      case 'routing': case 'retry': case 'step_retry':
        pushItem({ kind: 'service', id: msg.id, variant: msg.type as 'routing' | 'retry' | 'step_retry', content: msg.content, metadata: meta }, planStepId); break
      case 'status':
        pushItem({ kind: 'service', id: msg.id, variant: 'status', content: msg.content, metadata: meta }, planStepId); break
      case 'step_done': case 'thinking': case 'subagent_launch': case 'subagent_complete': case 'task_resumed': break
      default: break
    }
  }

  for (const item of items) { if (item.kind === 'plan_step') item.children = collapseThoughts(item.children) }
  return { items: collapseThoughts(items), pendingActions }
}

const EMPTY_PENDING: DisplayItem[] = []

/** Lightweight pending actions scan — avoids full groupMessages pipeline. */
export function extractPendingActions(messages: ChatMessageUI[]): DisplayItem[] {
  const actions: DisplayItem[] = []
  for (const msg of messages) {
    if (msg.metadata?.resolved === true) continue
    if (msg.type === 'tool_confirm') actions.push({ kind: 'tool_confirm', message: msg })
    else if (msg.type === 'ask_user') actions.push({ kind: 'ask_user', message: msg })
    else if (msg.type === 'task_failed_resumable') actions.push({ kind: 'resume_action', message: msg })
    else if (msg.type === 'step_limit') actions.push({ kind: 'step_limit', message: msg })
  }
  return actions.length > 0 ? actions : EMPTY_PENDING
}

/** Rebuild planStore from persisted history messages (called after history load). */
export function rebuildPlanFromHistory(messages: ChatMessageUI[]): void {
  const store = usePlanStore.getState()
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

  group.completedCount = group.items.filter(
    it => it.status === 'completed' || it.status === 'failed'
  ).length

  store.setPlan(group)
}
