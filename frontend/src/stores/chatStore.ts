import { create } from 'zustand'

export type MessageType =
  | 'user' | 'assistant' | 'thinking' | 'step_done' | 'tool_call' | 'tool_result'
  | 'tool_confirm' | 'ask_user' | 'routing' | 'eval' | 'reflection' | 'plan' | 'error' | 'thought'
  | 'plan_step_start' | 'plan_step_complete' | 'retry' | 'ac_extracted' | 'subagent_launch' | 'subagent_complete' | 'status'
  | 'task_failed_resumable'

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
  | { kind: 'tool'; id: string; toolName: string; args: string; parsedArgs?: Record<string, unknown>; result?: string; resultLen?: number; status: 'running' | 'success' | 'error' | 'awaiting_confirmation' }
  | { kind: 'tool_confirm'; message: ChatMessageUI }
  | { kind: 'ask_user'; message: ChatMessageUI }
  | { kind: 'error'; message: ChatMessageUI }
  | { kind: 'service'; id: string; variant: 'routing' | 'retry' | 'ac_extracted' | 'status'; content: string; metadata?: Record<string, unknown> }
  | { kind: 'plan_step'; id: string; stepId: string; stepNum: number; title: string; status: 'running' | 'completed' | 'failed'; duration?: number; isRetry?: boolean; children: DisplayItem[] }
  | { kind: 'step_finish'; id: string; stepNum?: number }
  | { kind: 'action_placeholder'; id: string; label: string }
  | { kind: 'thought_group'; id: string; thoughts: Array<{ content: string; reasoning?: string }> }
  | { kind: 'resume_action'; message: ChatMessageUI }

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
  const makeToolKey = (planStepId: string | undefined, step: number | string): string =>
    `${planStepId ?? ''}:${step}`

  const pushItem = (item: DisplayItem, planStepId?: string) => {
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
        openSteps.delete(stepId)
      }
      continue
    }

    // Skip orchestration events handled elsewhere
    if (['eval', 'reflection'].includes(msg.type)) continue

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
        }
        const stepNum = meta?.step as number | string
        if (stepNum !== undefined) toolItemsByStep.set(makeToolKey(planStepId, stepNum), toolItem)
        pushItem(toolItem, planStepId)
        break
      }

      case 'tool_result': {
        const stepNum = meta?.step as number | string
        const resultPlanStepId = meta?.plan_step_id as string | undefined
        if (stepNum !== undefined) {
          const toolItem = toolItemsByStep.get(makeToolKey(resultPlanStepId, stepNum))
          if (toolItem) {
            toolItem.result = (meta?.result as string) ?? (meta?.result_preview as string)
            toolItem.resultLen = meta?.result_len as number
            toolItem.status = 'success'
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
        if (!resolved) {
          pendingActions.push({ kind: 'resume_action', message: msg })
        }
        pushItem({ kind: 'resume_action', message: msg }, planStepId)
        break
      }

      case 'error':
        pushItem({ kind: 'error', message: msg }, planStepId)
        break

      case 'routing':
      case 'retry': {
        pushItem({
          kind: 'service',
          id: msg.id,
          variant: msg.type as 'routing' | 'retry',
          content: msg.content,
          metadata: meta,
        }, planStepId)
        break
      }

      case 'ac_extracted': {
        pushItem({
          kind: 'service',
          id: msg.id,
          variant: 'ac_extracted',
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
        break

      default:
        break
    }
  }

  // Collapse thoughts inside plan step children
  for (const item of items) {
    if (item.kind === 'plan_step') {
      (item as DisplayItem & { kind: 'plan_step' }).children = collapseThoughts(
        (item as DisplayItem & { kind: 'plan_step' }).children
      )
    }
  }

  // Post-process: collapse consecutive orchestrator-level thoughts into thought_groups
  const collapsed = collapseThoughts(items)

  return { items: collapsed, pendingActions }
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
  setSessionTokens: (inputTokens: number, outputTokens: number) => void
  setActivityStatus: (status: string | null) => void
  pendingActions: DisplayItem[]
  setPendingActions: (actions: DisplayItem[]) => void
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
  clearStepContextFill: (stepId) => set((s) => {
    // eslint-disable-next-line @typescript-eslint/no-unused-vars
    const { [stepId]: _, ...rest } = s.stepContextFill
    return { stepContextFill: rest }
  }),
  setSessionTokens: (inputTokens, outputTokens) => set({
    sessionInputTokens: inputTokens,
    sessionOutputTokens: outputTokens,
  }),
  setActivityStatus: (status) => set({ activityStatus: status }),
  pendingActions: [],
  setPendingActions: (actions) => set({ pendingActions: actions }),
  resolveAction: (sessionId, messageId, metadataUpdates) => set((s) => {
    // 1. Update the message metadata (mark resolved)
    const msgs = s.messages[sessionId]
    let newMessages = s.messages
    if (msgs) {
      const idx = msgs.findIndex(m => m.id === messageId)
      if (idx !== -1) {
        const updated = [...msgs]
        const existing = updated[idx]!
        updated[idx] = {
          ...existing,
          metadata: { ...existing.metadata, resolved: true, ...metadataUpdates },
        }
        newMessages = { ...s.messages, [sessionId]: updated }
      }
    }
    // 2. Remove from pendingActions immediately
    const newPending = s.pendingActions.filter(a => {
      if (a.kind === 'tool_confirm' || a.kind === 'ask_user' || a.kind === 'resume_action') {
        return a.message.id !== messageId
      }
      return true
    })
    return { messages: newMessages, pendingActions: newPending }
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
        // Also remove from pendingActions
        const newPending = s.pendingActions.filter(a => {
          if (a.kind === 'tool_confirm' || a.kind === 'ask_user' || a.kind === 'resume_action') {
            return a.message.id !== m.id
          }
          return true
        })
        return { messages: { ...s.messages, [sessionId]: updated }, pendingActions: newPending }
      }
    }
    return s
  }),
  setTaskActive: (active) => set({ isTaskActive: active }),
  clearSessionUIState: () => set({
    activityStatus: null,
    streamingText: null,
    isThinking: false,
    stepContextFill: {},
    sessionInputTokens: 0,
    sessionOutputTokens: 0,
  }),
}))
