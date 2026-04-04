import { create } from 'zustand'

export type MessageType =
  | 'user' | 'assistant' | 'thinking' | 'step_done' | 'tool_call' | 'tool_result'
  | 'tool_confirm' | 'ask_user' | 'routing' | 'eval' | 'reflection' | 'plan' | 'error' | 'thought'
  | 'plan_step_start' | 'plan_step_complete' | 'retry' | 'escalation' | 'ac_extracted' | 'subagent_launch' | 'subagent_complete'

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
  | { kind: 'tool'; id: string; toolName: string; args: string; result?: string; resultLen?: number; status: 'running' | 'success' | 'error' | 'awaiting_confirmation' }
  | { kind: 'tool_confirm'; message: ChatMessageUI }
  | { kind: 'ask_user'; message: ChatMessageUI }
  | { kind: 'error'; message: ChatMessageUI }
  | { kind: 'service'; id: string; variant: 'routing' | 'retry' | 'escalation' | 'ac_extracted'; content: string; metadata?: Record<string, unknown> }
  | { kind: 'plan_step'; id: string; stepId: string; stepNum: number; title: string; status: 'running' | 'completed' | 'failed'; duration?: number; isRetry?: boolean; children: DisplayItem[] }
  | { kind: 'action_placeholder'; id: string; label: string }
  | { kind: 'thought_group'; id: string; thoughts: Array<{ content: string; reasoning?: string }> }

export interface GroupedMessages {
  items: DisplayItem[]
  pendingActions: DisplayItem[]
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
        // Skip subagent tool calls – activity is already visible in plan step history
        if ((meta?.tool as string) === 'subagent') break
        const isAwaiting = meta?.awaiting_confirmation === true
        const hasResult = meta?.completed === true
        const toolItem: DisplayItem & { kind: 'tool' } = {
          kind: 'tool',
          id: msg.id,
          toolName: (meta?.tool as string) || 'Tool',
          args: (meta?.args as string) || '',
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
          pushItem({ kind: 'tool_confirm', message: msg }, planStepId)
        } else {
          pendingActions.push({ kind: 'tool_confirm', message: msg })
          pushItem({ kind: 'action_placeholder', id: msg.id, label: 'Awaiting confirmation...' }, planStepId)
        }
        break
      }

      case 'ask_user': {
        const resolved = meta?.resolved === true
        if (resolved) {
          pushItem({ kind: 'ask_user', message: msg }, planStepId)
        } else {
          pendingActions.push({ kind: 'ask_user', message: msg })
          pushItem({ kind: 'action_placeholder', id: msg.id, label: 'Awaiting your answer...' }, planStepId)
        }
        break
      }

      case 'error':
        pushItem({ kind: 'error', message: msg }, planStepId)
        break

      case 'routing':
      case 'retry':
      case 'escalation': {
        pushItem({
          kind: 'service',
          id: msg.id,
          variant: msg.type as 'routing' | 'retry' | 'escalation',
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

  // Post-process: collapse consecutive orchestrator-level thoughts into thought_groups
  const collapsed: DisplayItem[] = []
  let i = 0
  while (i < items.length) {
    if (items[i].kind === 'thought') {
      const thoughts: Array<{ content: string; reasoning?: string }> = []
      const firstId = (items[i] as DisplayItem & { kind: 'thought' }).id
      while (i < items.length && items[i].kind === 'thought') {
        const t = items[i] as DisplayItem & { kind: 'thought' }
        thoughts.push({ content: t.content, reasoning: t.reasoning })
        i++
      }
      if (thoughts.length === 1) {
        // Single thought — keep as-is (go back one)
        collapsed.push(items[i - 1])
      } else {
        collapsed.push({ kind: 'thought_group', id: `tg-${firstId}`, thoughts })
      }
    } else {
      collapsed.push(items[i])
      i++
    }
  }

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
  contextFill: ContextFillState | null
  activityStatus: string | null
  isTaskActive: boolean
  addMessage: (sessionId: string, msg: ChatMessageUI) => void
  updateMessage: (sessionId: string, id: string, updates: Partial<ChatMessageUI>) => void
  setMessages: (sessionId: string, msgs: ChatMessageUI[]) => void
  clearMessages: (sessionId: string) => void
  setStreaming: (text: string | null) => void
  appendStreamToken: (token: string) => void
  setThinking: (thinking: boolean) => void
  setContextFill: (data: ContextFillState | null) => void
  setActivityStatus: (status: string | null) => void
  pendingActions: DisplayItem[]
  setPendingActions: (actions: DisplayItem[]) => void
  setTaskActive: (active: boolean) => void
  clearSessionUIState: () => void
}

export const useChatStore = create<ChatState>((set) => ({
  messages: {},
  streamingText: null,
  isThinking: false,
  contextFill: null,
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
    const existing = updated[idx]
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
  setContextFill: (data) => set({ contextFill: data }),
  setActivityStatus: (status) => set({ activityStatus: status }),
  pendingActions: [],
  setPendingActions: (actions) => set({ pendingActions: actions }),
  setTaskActive: (active) => set({ isTaskActive: active }),
  clearSessionUIState: () => set({
    activityStatus: null,
    streamingText: null,
    isThinking: false,
    contextFill: null,
  }),
}))
