/**
 * Sub-functions used by groupMessages in chatUtils.ts.
 * Extracted to keep the main module under 200 lines.
 */
import type { ChatMessageUI, DisplayItem } from '@/types/messages'
import type { TodoItem } from '@/types/models'
import { resolveToolKey } from './chatUtilsHelpers'

export type ToolLike = DisplayItem & { kind: 'tool' }
export type PlanStep = DisplayItem & { kind: 'plan_step' }
export type SubAgentItem = DisplayItem & { kind: 'subagent' }
export type StepLikeItem = PlanStep | SubAgentItem

/**
 * handleStepTodoUpdate processes a step_todo_update message into a
 * DisplayItem.kind='checklist'. Each update supersedes the previous one for
 * the same key (stepId || '' for standalone): the old checklist is removed
 * from its container and replaced by the new one at the current stream
 * position. The sinking post-pass in groupMessages moves active (incomplete)
 * checklists to the end of their container.
 */
export function handleStepTodoUpdate(
  msg: ChatMessageUI, meta: Record<string, unknown> | undefined,
  openSteps: Map<string, StepLikeItem>, items: DisplayItem[],
  checklistsByKey: Map<string, { item: DisplayItem & { kind: 'checklist' }; container: DisplayItem[] }>,
) {
  const stepId = (meta?.step_id as string) || ''
  const rawItems = meta?.items as Array<{ text: string; checked: boolean }> | undefined
  if (!rawItems || rawItems.length === 0) return

  const todoItems: TodoItem[] = rawItems.map((it) => ({ text: it.text, checked: it.checked }))
  const active = todoItems.some((it) => !it.checked)
  const key = stepId // '' for standalone

  // Supersede the previous checklist for this key — remove it from its container.
  const prev = checklistsByKey.get(key)
  if (prev) {
    const idx = prev.container.indexOf(prev.item)
    if (idx !== -1) prev.container.splice(idx, 1)
  }

  const checklistItem: DisplayItem & { kind: 'checklist' } = {
    kind: 'checklist', id: msg.id, stepId: stepId || null, items: todoItems, active,
  }

  // Push into the step's children if stepId matches an open step, otherwise root.
  const container = stepId ? openSteps.get(stepId) : null
  if (container) {
    container.children.push(checklistItem)
  } else {
    items.push(checklistItem)
  }

  checklistsByKey.set(key, { item: checklistItem, container: container ? container.children : items })
}

export function handlePlanStepStart(
  msg: ChatMessageUI, meta: Record<string, unknown> | undefined,
  stepIndexMap: Map<string, { num: number; title: string; description: string }>,
  stepIdCounts: Map<string, number>, openSteps: Map<string, StepLikeItem>, items: DisplayItem[],
) {
  const stepId = (meta?.step_id as string) || ''
  const description = (meta?.description as string) || ''
  const summary = (meta?.summary as string)?.trim() || ''
  const info = stepIndexMap.get(stepId)
  // Skip plan_step blocks for ad-hoc step_ids not in a declared plan
  // (e.g. the Conductor's own update_checklist with step_id "main").
  // Their checklist updates still flow to the chat as DisplayItem.kind='checklist';
  // only the plan_step block is suppressed.
  if (!info && !description && !summary) return
  const resolvedInfo = info || { num: 0, title: summary || description || stepId, description: description || stepId }
  const count = (stepIdCounts.get(stepId) ?? 0) + 1
  stepIdCounts.set(stepId, count)
  const stepItem: PlanStep = {
    kind: 'plan_step', id: msg.id, stepId, stepNum: resolvedInfo.num, title: resolvedInfo.title,
    description: resolvedInfo.description, status: 'running', children: [], ...(count > 1 ? { isRetry: true } : {}),
  }
  openSteps.set(stepId, stepItem)
  items.push(stepItem)
}

export function handlePlanStepComplete(meta: Record<string, unknown> | undefined, openSteps: Map<string, StepLikeItem>) {
  const stepId = (meta?.step_id as string) || ''
  const step = openSteps.get(stepId)
  if (!step) return
  step.status = (meta?.success as boolean) ? 'completed' : 'failed'
  // Subagents receive their duration from subagent_complete; the subsequent
  // plan_step_complete fires with duration=0, so don't overwrite.
  if (step.kind === 'plan_step' && meta?.duration !== undefined) step.duration = meta.duration as number
  if (!meta?.success && meta?.error) step.error = meta.error as string
  openSteps.delete(stepId)
}

export function handleSubAgentLaunch(
  msg: ChatMessageUI, meta: Record<string, unknown> | undefined,
  openSteps: Map<string, StepLikeItem>, items: DisplayItem[],
) {
  const stepId = (meta?.step_id as string) || ''
  const description = (meta?.description as string) || ''
  const existing = openSteps.get(stepId)
  if (existing) {
    // Convert the plan_step into a subagent block in place.
    // The same object reference stays in the items tree and openSteps,
    // so children keep nesting under it.
    const converted = existing as unknown as SubAgentItem
    converted.kind = 'subagent'
    if (description) {
      converted.title = description
      converted.description = description
    }
    return
  }
  // No prior plan_step_start — create a fresh top-level subagent block.
  const subItem: SubAgentItem = {
    kind: 'subagent', id: msg.id, stepId,
    title: description || stepId,
    description: description || undefined,
    status: 'running', children: [],
  }
  openSteps.set(stepId, subItem)
  items.push(subItem)
}

export function handleSubAgentComplete(
  meta: Record<string, unknown> | undefined,
  openSteps: Map<string, StepLikeItem>,
) {
  const stepId = (meta?.step_id as string) || ''
  const step = openSteps.get(stepId)
  if (!step) return
  step.status = (meta?.success as boolean) ? 'completed' : 'failed'
  if (meta?.duration !== undefined) step.duration = meta.duration as number
}

export function handleReflection(
  msg: ChatMessageUI, meta: Record<string, unknown> | undefined,
  openSteps: Map<string, StepLikeItem>, items: DisplayItem[],
) {
  const item: DisplayItem = {
    kind: 'reflection', id: msg.id, summary: (meta?.summary as string) || '',
    suggestedAction: (meta?.suggested_action as string) || '', rootCause: (meta?.root_cause as string) || '',
    failureAnalysis: (meta?.failure_analysis as string) || '', actionPlan: (meta?.action_plan as string) || '',
    reasoning: (meta?.reasoning as string) || '', hypotheses: (meta?.insights as string[]) || [],
    attempt: (meta?.attempt as number) || 0, maxAttempts: (meta?.max_attempts as number) || 0,
  }
  const ref = meta?.plan_step_id as string | undefined
  const container = ref ? openSteps.get(ref) : null
  if (container) { container.children.push(item); return }
  const openEntries = [...openSteps.values()]
  if (openEntries.length > 0) { openEntries[openEntries.length - 1]!.children.push(item) } else { items.push(item) }
}

function applyPending(
  item: ToolLike, key: string | undefined,
  toolItemsByKey: Map<string, ToolLike>,
  pendingResults: Map<string, { result?: string; resultLen?: number; error?: boolean }>,
) {
  if (!key) return
  toolItemsByKey.set(key, item)
  const pending = pendingResults.get(key)
  if (!pending) return
  item.result = pending.result
  item.resultLen = pending.resultLen
  item.status = pending.error ? 'error' : 'success'
  pendingResults.delete(key)
}

export function handleToolCall(
  msg: ChatMessageUI, meta: Record<string, unknown> | undefined, planStepId: string | undefined,
  stepIndexMap: Map<string, { num: number; title: string; description: string }>,
  toolItemsByKey: Map<string, ToolLike>,
  pendingResults: Map<string, { result?: string; resultLen?: number; error?: boolean }>,
  pushItem: (item: DisplayItem, psId?: string) => void,
) {
  const toolName = (meta?.tool as string) || ''
  if (toolName === 'subagent') return
  if (toolName === 'finish') {
    const num = planStepId ? stepIndexMap.get(planStepId)?.num : undefined
    pushItem({ kind: 'step_finish', id: msg.id, stepNum: num }, planStepId)
    return
  }
  const key = meta ? resolveToolKey(meta, planStepId) : undefined
  const hasResult = meta?.completed === true
  const isAwaiting = meta?.awaiting_confirmation === true
  const isError = meta?.error === true
  const toolItem: DisplayItem & { kind: 'tool' } = {
    kind: 'tool', id: msg.id, toolName: toolName || 'Tool', args: (meta?.args as string) || '',
    parsedArgs: meta?.parsed_args as Record<string, unknown> | undefined,
    result: hasResult ? ((meta?.result as string) ?? (meta?.result_preview as string)) : undefined,
    resultLen: hasResult ? (meta?.result_len as number) : undefined,
    status: hasResult ? (isError ? 'error' : 'success') : (isAwaiting ? 'awaiting_confirmation' : 'running'),
    source: meta?.source as string | undefined,
  }
  applyPending(toolItem, key, toolItemsByKey, pendingResults)
  pushItem(toolItem, planStepId)
}

export function handleToolResult(
  meta: Record<string, unknown> | undefined,
  toolItemsByKey: Map<string, ToolLike>,
  pendingResults: Map<string, { result?: string; resultLen?: number; error?: boolean }>,
) {
  if (!meta) return
  const resultPlanStepId = meta.plan_step_id as string | undefined
  const key = resolveToolKey(meta, resultPlanStepId)
  if (!key) return
  const toolItem = toolItemsByKey.get(key)
  if (toolItem) {
    toolItem.result = (meta.result as string) ?? (meta.result_preview as string)
    toolItem.resultLen = meta.result_len as number
    toolItem.status = (meta.error === true) ? 'error' : 'success'
  } else {
    pendingResults.set(key, {
      result: (meta.result as string) ?? (meta.result_preview as string),
      resultLen: meta.result_len as number, error: meta.error === true,
    })
  }
}

export function handleActionMessage(
  msg: ChatMessageUI, meta: Record<string, unknown> | undefined, planStepId: string | undefined,
  pendingActions: DisplayItem[], pushItem: (item: DisplayItem, psId?: string) => void,
) {
  if (meta?.resolved === true) return
  switch (msg.type) {
    case 'tool_confirm':
      pendingActions.push({ kind: 'tool_confirm', message: msg })
      pushItem({ kind: 'action_placeholder', id: msg.id, label: 'Awaiting confirmation...' }, planStepId)
      break
    case 'ask_user':
      pendingActions.push({ kind: 'ask_user', message: msg })
      pushItem({ kind: 'action_placeholder', id: msg.id, label: 'Awaiting your answer...' }, planStepId)
      break
    case 'task_failed_resumable':
      pendingActions.push({ kind: 'resume_action', message: msg })
      break
    case 'step_limit':
      pendingActions.push({ kind: 'step_limit', message: msg })
      pushItem({ kind: 'action_placeholder', id: msg.id, label: 'Step limit reached — awaiting decision...' }, planStepId)
      break
    case 'plan_review':
      pendingActions.push({ kind: 'plan_review', message: msg })
      pushItem({ kind: 'action_placeholder', id: msg.id, label: 'Plan is ready for review...' }, planStepId)
      break
  }
}
