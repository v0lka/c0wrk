import { create } from 'zustand'

export type MessageType =
  | 'user' | 'assistant' | 'thinking' | 'step_done' | 'tool_call' | 'tool_result'
  | 'tool_confirm' | 'ask_user' | 'routing' | 'reflection' | 'plan' | 'error' | 'thought'
  | 'plan_step_start' | 'plan_step_complete' | 'retry' | 'step_retry' | 'subagent_launch' | 'subagent_complete' | 'status'
  | 'task_failed_resumable'
  | 'task_resumed'
  | 'step_limit'
  | 'context_compaction'

export interface ChatMessageUI {
  id: string
  sessionId: string
  type: MessageType
  content: string
  metadata?: Record<string, unknown>
  timestamp: number
}



export type DisplayItem =
  | { kind: 'user'; message: ChatMessageUI }
  | { kind: 'assistant'; message: ChatMessageUI }
  | { kind: 'thought'; id: string; stepNum: number; content: string; reasoning?: string }
  | { kind: 'tool'; id: string; toolName: string; args: string; parsedArgs?: Record<string, unknown>; result?: string; resultLen?: number; status: 'running' | 'success' | 'error' | 'awaiting_confirmation'; source?: string }
  | { kind: 'tool_confirm'; message: ChatMessageUI }
  | { kind: 'ask_user'; message: ChatMessageUI }
  | { kind: 'error'; message: ChatMessageUI }
  | { kind: 'service'; id: string; variant: 'routing' | 'retry' | 'step_retry' | 'status'; content: string; metadata?: Record<string, unknown> }
  | { kind: 'plan_step'; id: string; stepId: string; stepNum: number; title: string; description?: string; status: 'running' | 'completed' | 'failed'; duration?: number; error?: string; isRetry?: boolean; children: DisplayItem[] }
  | { kind: 'reflection'; id: string; summary: string; suggestedAction: string; rootCause: string; failureAnalysis: string; actionPlan: string; reasoning: string; hypotheses: string[]; attempt: number; maxAttempts: number }
  | { kind: 'step_finish'; id: string; stepNum?: number }
  | { kind: 'memory_read'; id: string; toolName: string; args: string; parsedArgs?: Record<string, unknown>; result?: string; resultLen?: number; status: 'running' | 'success' | 'error' }
  | { kind: 'action_placeholder'; id: string; label: string }
  | { kind: 'thought_group'; id: string; thoughts: Array<{ content: string; reasoning?: string }> }
  | { kind: 'resume_action'; message: ChatMessageUI }
  | { kind: 'step_limit'; message: ChatMessageUI }
  | { kind: 'context_compaction'; id: string; beforePercent: number; afterPercent: number }

export interface GroupedMessages {
  items: DisplayItem[]
  pendingActions: DisplayItem[]
}

function collapseThoughts(items: DisplayItem[]): DisplayItem[] {
  const result: DisplayItem[] = []
  let i = 0
  while (i < items.length) {
    const current = items[i]
    if (!current) break
    if (current.kind === 'thought') {
      const thoughts: Array<{ content: string; reasoning?: string }> = []
      const firstId = current.id
      while (i < items.length) {
        const item = items[i]
        if (!item || item.kind !== 'thought') break
        thoughts.push({ content: item.content, reasoning: item.reasoning })
        i++
      }
      if (thoughts.length === 1) {
        // i was already incremented past the single thought
        const prev = items[i - 1]!
        result.push(prev)
      } else {
        result.push({ kind: 'thought_group', id: `tg-${firstId}`, thoughts })
      }
    } else {
      result.push(current)
      i++
    }
  }
  return result
}

export function groupMessages(messages: ChatMessageUI[]): GroupedMessages {
  const items: DisplayItem[] = []
  const pendingActions: DisplayItem[] = []
  const openSteps = new Map<string, DisplayItem & { kind: 'plan_step' }>()
  const stepIdCounts = new Map<string, number>()
  const stepIndexMap = new Map<string, { num: number; title: string }>()
  const toolItemsByStep = new Map<string, DisplayItem & { kind: 'tool' }>()
  // Buffer for tool_result messages that arrive before their matching tool_call
  const pendingResults = new Map<string, { result?: string; resultLen?: number; error?: boolean }>()
  const makeToolKey = (planStepId: string | undefined, step: number | string, callIdx?: number | string, retryAttempt?: number | string): string =>
    `${planStepId ?? ''}:${step}${callIdx !== undefined ? `:${callIdx}` : ''}${retryAttempt ? `:r${retryAttempt}` : ''}`

  const pushItem = (item: DisplayItem, planStepId?: string) => {
    // Check plan step container
    const container = planStepId ? openSteps.get(planStepId) : null
    if (container) {
      container.children.push(item)
    } else {
      items.push(item)
    }
  }

  for (const msg of messages) {
    const meta = (typeof msg.metadata === 'object' && msg.metadata !== null)
      ? msg.metadata as Record<string, unknown>
      : undefined

    // Build step index from plan events
    if (msg.type === 'plan') {
      const steps = (meta?.steps as Array<{ id?: string; description: string }>) || []
      steps.forEach((s, i) => {
        if (s.id) stepIndexMap.set(s.id, { num: i + 1, title: s.description })
      })
      continue
    }

    // Handle plan step lifecycle
    if (msg.type === 'plan_step_start') {
      const stepId = (meta?.step_id as string) || ''
      const info = stepIndexMap.get(stepId) || { num: 0, title: (meta?.description as string) || stepId }
      const count = (stepIdCounts.get(stepId) ?? 0) + 1
      stepIdCounts.set(stepId, count)
      const stepItem: DisplayItem & { kind: 'plan_step' } = {
        kind: 'plan_step',
        id: msg.id,
        stepId,
        stepNum: info.num,
        title: info.title,
        status: 'running',
        children: [],
        ...(count > 1 ? { isRetry: true } : {}),
      }
      openSteps.set(stepId, stepItem)
      items.push(stepItem)
      continue
    }

    if (msg.type === 'plan_step_complete') {
      const stepId = (meta?.step_id as string) || ''
      const step = openSteps.get(stepId)
      if (step) {
        step.status = (meta?.success as boolean) ? 'completed' : 'failed'
        if (meta?.duration !== undefined) step.duration = meta.duration as number
        if (!meta?.success && meta?.error) step.error = meta.error as string
        openSteps.delete(stepId)
      }
      continue
    }

    if (msg.type === 'reflection') {
      const reflectionItem: DisplayItem = {
        kind: 'reflection',
        id: msg.id,
        summary: (meta?.summary as string) || '',
        suggestedAction: (meta?.suggested_action as string) || '',
        rootCause: (meta?.root_cause as string) || '',
        failureAnalysis: (meta?.failure_analysis as string) || '',
        actionPlan: (meta?.action_plan as string) || '',
        reasoning: (meta?.reasoning as string) || '',
        hypotheses: (meta?.insights as string[]) || [],
        attempt: (meta?.attempt as number) || 0,
        maxAttempts: (meta?.max_attempts as number) || 0,
      }
      // Nest inside the most recently opened plan step if available
      const planStepIdRef = meta?.plan_step_id as string | undefined
      const container = planStepIdRef ? openSteps.get(planStepIdRef) : null
      if (container) {
        container.children.push(reflectionItem)
      } else {
        // Try to nest in any open step, otherwise top-level
        const openStepEntries = [...openSteps.values()]
        if (openStepEntries.length > 0) {
          openStepEntries[openStepEntries.length - 1]!.children.push(reflectionItem)
        } else {
          items.push(reflectionItem)
        }
      }
      continue
    }

    const planStepId = meta?.plan_step_id as string | undefined

    switch (msg.type) {
      case 'user':
        pushItem({ kind: 'user', message: msg }, planStepId)
        break

      case 'assistant':
        pushItem({ kind: 'assistant', message: msg }, planStepId)
        break

      case 'thought':
        pushItem({
          kind: 'thought',
          id: msg.id,
          stepNum: (meta?.step_num as number) ?? 0,
          content: msg.content,
          reasoning: meta?.reasoning as string | undefined,
        }, planStepId)
        break

      case 'tool_call': {
        const toolName = (meta?.tool as string) || ''
        // Skip subagent tool calls – activity is already visible in plan step history
        if (toolName === 'subagent') break
        // Render finish tool as a compact "Finished step N" message
        if (toolName === 'finish') {
          const planStepNum = planStepId ? stepIndexMap.get(planStepId)?.num : undefined
          pushItem({ kind: 'step_finish', id: msg.id, stepNum: planStepNum }, planStepId)
          break
        }
        // Render memory/blackboard tools as compact collapsible items
        if (['read_evidence', 'read_step_output', 'list_step_outputs', 'store_fact', 'search_facts'].includes(toolName)) {
          const hasMemResult = meta?.completed === true
          const memoryItem: DisplayItem & { kind: 'memory_read' } = {
            kind: 'memory_read',
            id: msg.id,
            toolName,
            args: (meta?.args as string) || '',
            parsedArgs: meta?.parsed_args as Record<string, unknown> | undefined,
            result: hasMemResult ? ((meta?.result as string) ?? (meta?.result_preview as string)) : undefined,
            resultLen: hasMemResult ? (meta?.result_len as number) : undefined,
            status: hasMemResult ? 'success' : 'running',
          }
          const memStepNum = meta?.step as number | string
          const memCallIdx = meta?.call_idx as number | undefined
          const memRetryAttempt = meta?.retry_attempt as number | undefined
          const memToolCallId = meta?.tool_call_id as string | undefined
          // Prefer tool_call_id for matching, fall back to composite key
          const memKey = memToolCallId || (memStepNum !== undefined ? makeToolKey(planStepId, memStepNum, memCallIdx, memRetryAttempt) : undefined)
          if (memKey) {
            toolItemsByStep.set(memKey, memoryItem as unknown as DisplayItem & { kind: 'tool' })
            const pending = pendingResults.get(memKey)
            if (pending) {
              memoryItem.result = pending.result
              memoryItem.resultLen = pending.resultLen
              memoryItem.status = pending.error ? 'error' : 'success'
              pendingResults.delete(memKey)
            }
          }
          pushItem(memoryItem, planStepId)
          break
        }
        const isAwaiting = meta?.awaiting_confirmation === true
        const hasResult = meta?.completed === true
        const toolItem: DisplayItem & { kind: 'tool' } = {
          kind: 'tool',
          id: msg.id,
          toolName: toolName || 'Tool',
          args: (meta?.args as string) || '',
          parsedArgs: meta?.parsed_args as Record<string, unknown> | undefined,
          result: hasResult ? ((meta?.result as string) ?? (meta?.result_preview as string)) : undefined,
          resultLen: hasResult ? (meta?.result_len as number) : undefined,
          status: hasResult ? 'success' : (isAwaiting ? 'awaiting_confirmation' : 'running'),
          source: meta?.source as string | undefined,
        }
        const stepNum = meta?.step as number | string
        const callIdx = meta?.call_idx as number | undefined
        const retryAttempt = meta?.retry_attempt as number | undefined
        const toolCallId = meta?.tool_call_id as string | undefined
        // Prefer tool_call_id for matching, fall back to composite key
        const key = toolCallId || (stepNum !== undefined ? makeToolKey(planStepId, stepNum, callIdx, retryAttempt) : undefined)
        if (key) {
          toolItemsByStep.set(key, toolItem)
          // Apply any pending result that arrived before this tool_call
          const pending = pendingResults.get(key)
          if (pending) {
            toolItem.result = pending.result
            toolItem.resultLen = pending.resultLen
            toolItem.status = pending.error ? 'error' : 'success'
            pendingResults.delete(key)
          }
        }
        pushItem(toolItem, planStepId)
        break
      }

      case 'tool_result': {
        const stepNum = meta?.step as number | string
        const resultPlanStepId = meta?.plan_step_id as string | undefined
        const resultCallIdx = meta?.call_idx as number | undefined
        const retryAttempt = meta?.retry_attempt as number | undefined
        const toolCallId = meta?.tool_call_id as string | undefined
        // Prefer tool_call_id for matching, fall back to composite key
        const key = toolCallId || (stepNum !== undefined ? makeToolKey(resultPlanStepId, stepNum, resultCallIdx, retryAttempt) : undefined)
        if (key) {
          const toolItem = toolItemsByStep.get(key)
          if (toolItem) {
            toolItem.result = (meta?.result as string) ?? (meta?.result_preview as string)
            toolItem.resultLen = meta?.result_len as number
            toolItem.status = (meta?.error === true) ? 'error' : 'success'
          } else {
            // tool_call hasn't been processed yet — buffer the result
            pendingResults.set(key, {
              result: (meta?.result as string) ?? (meta?.result_preview as string),
              resultLen: meta?.result_len as number,
              error: meta?.error === true,
            })
          }
        }
        break
      }

      case 'tool_confirm': {
        const resolved = meta?.resolved === true
        if (resolved) {
          // Resolved confirmations should not appear inline — they were already
          // handled via PendingActionsBar; the tool result is shown as a normal tool call.
          break
        }
        pendingActions.push({ kind: 'tool_confirm', message: msg })
        pushItem({ kind: 'action_placeholder', id: msg.id, label: 'Awaiting confirmation...' }, planStepId)
        break
      }

      case 'ask_user': {
        const resolved = meta?.resolved === true
        if (resolved) {
          // Resolved ask_user items should not appear inline — they were already
          // handled via PendingActionsBar; the response is captured elsewhere.
          break
        }
        pendingActions.push({ kind: 'ask_user', message: msg })
        pushItem({ kind: 'action_placeholder', id: msg.id, label: 'Awaiting your answer...' }, planStepId)
        break
      }

      case 'task_failed_resumable': {
        const resolved = meta?.resolved === true
        if (resolved) {
          break
        }
        pendingActions.push({ kind: 'resume_action', message: msg })
        break
      }

      case 'step_limit': {
        const resolved = meta?.resolved === true
        if (resolved) {
          break
        }
        pendingActions.push({ kind: 'step_limit', message: msg })
        pushItem({ kind: 'action_placeholder', id: msg.id, label: 'Step limit reached — awaiting decision...' }, planStepId)
        break
      }

      case 'context_compaction': {
        const bp = (meta?.before_percent as number) ?? 0
        const ap = (meta?.after_percent as number) ?? 0
        pushItem({
          kind: 'context_compaction',
          id: msg.id,
          beforePercent: Math.round(bp),
          afterPercent: Math.round(ap),
        }, planStepId)
        break
      }

      case 'error':
        pushItem({ kind: 'error', message: msg }, planStepId)
        break

      case 'routing':
      case 'retry':
      case 'step_retry': {
        pushItem({
          kind: 'service',
          id: msg.id,
          variant: msg.type as 'routing' | 'retry' | 'step_retry',
          content: msg.content,
          metadata: meta,
        }, planStepId)
        break
      }

      case 'status': {
        pushItem({
          kind: 'service',
          id: msg.id,
          variant: 'status',
          content: msg.content,
          metadata: meta,
        }, planStepId)
        break
      }

      // Skip lifecycle markers
      case 'step_done':
      case 'thinking':
      case 'subagent_launch':
      case 'subagent_complete':
      case 'task_resumed':
        break

      default:
        break
    }
  }

  // Collapse thoughts inside plan step children
  for (const item of items) {
    if (item.kind === 'plan_step') {
      item.children = collapseThoughts(item.children)
    }
  }

  // Post-process: collapse consecutive orchestrator-level thoughts into thought_groups
  const collapsed = collapseThoughts(items)

  return { items: collapsed, pendingActions }
}

const EMPTY_PENDING: DisplayItem[] = []

/** Lightweight extraction of pending actions from messages (avoids full groupMessages). */
export function extractPendingActions(messages: ChatMessageUI[]): DisplayItem[] {
  const actions: DisplayItem[] = []
  for (const msg of messages) {
    const resolved = msg.metadata?.resolved === true
    if (resolved) continue
    if (msg.type === 'tool_confirm') {
      actions.push({ kind: 'tool_confirm', message: msg })
    } else if (msg.type === 'ask_user') {
      actions.push({ kind: 'ask_user', message: msg })
    } else if (msg.type === 'task_failed_resumable') {
      actions.push({ kind: 'resume_action', message: msg })
    } else if (msg.type === 'step_limit') {
      actions.push({ kind: 'step_limit', message: msg })
    }
  }
  return actions.length > 0 ? actions : EMPTY_PENDING
}

export interface ContextFillState {
  fillPercent: number
  usedTokens: number
  maxTokens: number
  status: string
}

interface ChatState {
  messages: Record<string, ChatMessageUI[]> // sessionId -> messages
  streamingText: string | null
  isThinking: boolean
  stepContextFill: Record<string, ContextFillState> // per-step context fill
  sessionInputTokens: number
  sessionOutputTokens: number
  sessionModel: string
  sessionFamily: string
  activityStatus: string | null
  isTaskActive: boolean
  addMessage: (sessionId: string, msg: ChatMessageUI) => void
  updateMessage: (sessionId: string, id: string, updates: Partial<ChatMessageUI>) => void
  setMessages: (sessionId: string, msgs: ChatMessageUI[]) => void
  clearMessages: (sessionId: string) => void
  setStreaming: (text: string | null) => void
  appendStreamToken: (token: string) => void
  setThinking: (thinking: boolean) => void
  setStepContextFill: (stepId: string, data: ContextFillState) => void
  clearStepContextFill: (stepId: string) => void
  setSessionTokens: (inputTokens: number, outputTokens: number, model?: string, family?: string) => void
  setActivityStatus: (status: string | null) => void
  resolveAction: (sessionId: string, messageId: string, metadataUpdates?: Record<string, unknown>) => void
  resolveResumeMessage: (sessionId: string) => void
  setTaskActive: (active: boolean) => void
  clearSessionUIState: () => void
}

export const useChatStore = create<ChatState>((set) => ({
  messages: {},
  streamingText: null,
  isThinking: false,
  stepContextFill: {},
  sessionInputTokens: 0,
  sessionOutputTokens: 0,
  sessionModel: '',
  sessionFamily: '',
  activityStatus: null,
  isTaskActive: false,
  addMessage: (sessionId, msg) => set((s) => ({
    messages: {
      ...s.messages,
      [sessionId]: [...(s.messages[sessionId] || []), msg],
    },
  })),
  updateMessage: (sessionId, id, updates) => set((s) => {
    const msgs = s.messages[sessionId]
    if (!msgs) return s
    const idx = msgs.findIndex(m => m.id === id)
    if (idx === -1) return s
    const updated = [...msgs]
    const existing = updated[idx]!
    updated[idx] = {
      ...existing,
      ...updates,
      metadata: updates.metadata
        ? { ...existing.metadata, ...updates.metadata }
        : existing.metadata,
    }
    return { messages: { ...s.messages, [sessionId]: updated } }
  }),
  setMessages: (sessionId, msgs) => set((s) => ({
    messages: { ...s.messages, [sessionId]: msgs },
  })),
  clearMessages: (sessionId) => set((s) => ({
    messages: { ...s.messages, [sessionId]: [] },
  })),
  setStreaming: (text) => set({ streamingText: text }),
  appendStreamToken: (token) => set((s) => ({
    streamingText: (s.streamingText || '') + token,
  })),
  setThinking: (thinking) => set({ isThinking: thinking }),
  setStepContextFill: (stepId, data) => set((s) => ({
    stepContextFill: { ...s.stepContextFill, [stepId]: data },
  })),
  clearStepContextFill: (stepId) => set((s) => ({
    stepContextFill: Object.fromEntries(
      Object.entries(s.stepContextFill).filter(([k]) => k !== stepId)
    ),
  })),
  setSessionTokens: (inputTokens, outputTokens, model?, family?) => set({
    sessionInputTokens: inputTokens,
    sessionOutputTokens: outputTokens,
    ...(model !== undefined && { sessionModel: model }),
    ...(family !== undefined && { sessionFamily: family }),
  }),
  setActivityStatus: (status) => set({ activityStatus: status }),
  resolveAction: (sessionId, messageId, metadataUpdates) => set((s) => {
    const msgs = s.messages[sessionId]
    if (!msgs) return s
    const idx = msgs.findIndex(m => m.id === messageId)
    if (idx === -1) return s
    const updated = [...msgs]
    const existing = updated[idx]!
    updated[idx] = {
      ...existing,
      metadata: { ...existing.metadata, resolved: true, ...metadataUpdates },
    }
    return { messages: { ...s.messages, [sessionId]: updated } }
  }),
  resolveResumeMessage: (sessionId) => set((s) => {
    const msgs = s.messages[sessionId]
    if (!msgs) return s
    // Find the most recent unresolved task_failed_resumable message
    for (let i = msgs.length - 1; i >= 0; i--) {
      const m = msgs[i]
      if (!m) continue
      if (m.type === 'task_failed_resumable' && m.metadata?.resolved !== true) {
        const updated = [...msgs]
        updated[i] = {
          ...m,
          metadata: { ...m.metadata, resolved: true },
        }
        return { messages: { ...s.messages, [sessionId]: updated } }
      }
    }
    return s
  }),
  setTaskActive: (active) => set({ isTaskActive: active }),
  clearSessionUIState: () => set({
    activityStatus: null,
    streamingText: null,
    isThinking: false,
    isTaskActive: false,
    stepContextFill: {},
    sessionInputTokens: 0,
    sessionOutputTokens: 0,
    sessionModel: '',
    sessionFamily: '',
  }),
}))
