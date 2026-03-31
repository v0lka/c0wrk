import { create } from 'zustand'

export type MessageType =
  | 'user' | 'assistant' | 'thinking' | 'step_done' | 'tool_call' | 'tool_result'
  | 'tool_confirm' | 'routing' | 'eval' | 'reflection' | 'plan' | 'error' | 'thought'
  | 'plan_step_start' | 'plan_step_complete' | 'retry' | 'escalation' | 'ac_extracted' | 'subagent_launch' | 'subagent_complete'

export interface ChatMessageUI {
  id: string
  sessionId: string
  type: MessageType
  content: string
  metadata?: Record<string, unknown> | unknown
  timestamp: number
}

export type PlanStepDisplay = {
  id: string
  description: string
  status: 'pending' | 'running' | 'completed' | 'failed'
  duration?: number
}

export type EvalCriterionDisplay = {
  id: string
  name: string
  description: string
  status: 'pass' | 'fail' | 'unclear'
}

export type StepItem = {
  toolCall: ChatMessageUI
  status: 'running' | 'success' | 'error'
  label: string
  detail?: string
  resultPreview?: string
  resultLen?: number
}

export type DisplayItem =
  | { kind: 'user'; message: ChatMessageUI }
  | { kind: 'assistant'; message: ChatMessageUI }
  | { kind: 'thought'; id: string; stepNum: number; content: string }
  | { kind: 'step_group'; id: string; steps: StepItem[] }
  | { kind: 'tool_confirm'; message: ChatMessageUI }
  | { kind: 'error'; message: ChatMessageUI }
  | { kind: 'plan'; id: string; steps: PlanStepDisplay[] }
  | { kind: 'eval'; id: string; passed: number; total: number; criteria: EvalCriterionDisplay[] }
  | { kind: 'reflection'; id: string; summary: string; insights: string[]; attempt: number; maxAttempts: number }
  | { kind: 'service'; id: string; variant: 'routing' | 'retry' | 'escalation' | 'ac_extracted'; content: string; metadata?: Record<string, unknown> }

export function groupMessages(messages: ChatMessageUI[]): DisplayItem[] {
  const items: DisplayItem[] = []
  let currentStepGroup: StepItem[] | null = null
  let stepGroupId = ''
  let lastPlanItem: Extract<DisplayItem, { kind: 'plan' }> | null = null

  const flushStepGroup = () => {
    if (currentStepGroup && currentStepGroup.length > 0) {
      items.push({ kind: 'step_group', id: stepGroupId, steps: [...currentStepGroup] })
      currentStepGroup = null
      stepGroupId = ''
    }
  }

  for (const msg of messages) {
    switch (msg.type) {
      case 'user':
        flushStepGroup()
        items.push({ kind: 'user', message: msg })
        break

      case 'assistant':
        flushStepGroup()
        items.push({ kind: 'assistant', message: msg })
        break

      case 'thought': {
        flushStepGroup()
        const meta = msg.metadata as Record<string, unknown> | undefined
        const stepNum = (meta?.step_num as number) ?? 0
        items.push({ kind: 'thought', id: msg.id, stepNum, content: msg.content })
        break
      }

      case 'tool_call': {
        if (!currentStepGroup) {
          currentStepGroup = []
          stepGroupId = `step-group-${msg.id}`
        }
        const meta = msg.metadata as Record<string, unknown> | undefined
        const completed = meta?.completed as boolean | undefined
        const error = meta?.error as string | undefined
        let status: 'running' | 'success' | 'error' = 'running'
        if (completed) {
          status = error ? 'error' : 'success'
        }
        const tool = (meta?.tool as string) || 'Tool'
        const resultPreview = meta?.result_preview as string | undefined
        const resultLen = meta?.result_len as number | undefined
        currentStepGroup.push({
          toolCall: msg,
          status,
          label: tool,
          detail: undefined,
          resultPreview,
          resultLen,
        })
        break
      }

      case 'tool_result': {
        // Attach result preview to matching tool_call in current step group
        if (currentStepGroup) {
          const meta = msg.metadata as Record<string, unknown> | undefined
          const step = meta?.step as number | undefined
          const preview = meta?.result_preview as string | undefined
          const len = meta?.result_len as number | undefined
          if (step !== undefined) {
            const match = currentStepGroup.find(s => {
              const sMeta = s.toolCall.metadata as Record<string, unknown> | undefined
              return (sMeta?.step as number) === step
            })
            if (match) {
              match.resultPreview = preview
              match.resultLen = len
              match.status = 'success'
            }
          }
        }
        break
      }

      case 'tool_confirm':
        flushStepGroup()
        items.push({ kind: 'tool_confirm', message: msg })
        break

      case 'error':
        flushStepGroup()
        items.push({ kind: 'error', message: msg })
        break

      case 'plan': {
        flushStepGroup()
        const meta = msg.metadata as Record<string, unknown> | undefined
        const rawSteps = (meta?.steps as Array<{ description: string; status?: string }>) || []
        const steps: PlanStepDisplay[] = rawSteps.map((s, i) => ({
          id: String(i + 1),
          description: s.description,
          status: (s.status || 'pending') as PlanStepDisplay['status'],
        }))
        const planItem: Extract<DisplayItem, { kind: 'plan' }> = {
          kind: 'plan',
          id: msg.id,
          steps,
        }
        lastPlanItem = planItem
        items.push(planItem)
        break
      }

      case 'plan_step_start': {
        const meta = msg.metadata as Record<string, unknown> | undefined
        const stepId = meta?.step_id as string | undefined
        if (lastPlanItem && stepId) {
          const step = lastPlanItem.steps.find(s => s.id === stepId)
          if (step) step.status = 'running'
        }
        break
      }

      case 'plan_step_complete': {
        const meta = msg.metadata as Record<string, unknown> | undefined
        const stepId = meta?.step_id as string | undefined
        const success = meta?.success as boolean | undefined
        const duration = meta?.duration as number | undefined
        if (lastPlanItem && stepId) {
          const step = lastPlanItem.steps.find(s => s.id === stepId)
          if (step) {
            step.status = success ? 'completed' : 'failed'
            if (duration !== undefined) step.duration = duration
          }
        }
        break
      }

      case 'eval': {
        flushStepGroup()
        const meta = msg.metadata as Record<string, unknown> | undefined
        const passed = (meta?.passed as number) ?? 0
        const total = (meta?.total as number) ?? 0
        const rawCriteria = (meta?.criteria as Array<{ name: string; description?: string; passed: boolean }>) || []
        const criteria: EvalCriterionDisplay[] = rawCriteria.map((c, i) => ({
          id: String(i + 1),
          name: c.name,
          description: c.description || c.name,
          status: c.passed ? 'pass' as const : 'fail' as const,
        }))
        items.push({ kind: 'eval', id: msg.id, passed, total, criteria })
        break
      }

      case 'reflection': {
        flushStepGroup()
        const meta = msg.metadata as Record<string, unknown> | undefined
        const summary = (meta?.summary as string) || msg.content
        const insights = (meta?.insights as string[]) || []
        const attempt = (meta?.attempt as number) ?? 1
        const maxAttempts = (meta?.max_attempts as number) ?? 3
        items.push({ kind: 'reflection', id: msg.id, summary, insights, attempt, maxAttempts })
        break
      }

      case 'routing':
      case 'retry':
      case 'escalation':
      case 'ac_extracted': {
        flushStepGroup()
        const meta = msg.metadata as Record<string, unknown> | undefined
        items.push({
          kind: 'service',
          id: msg.id,
          variant: msg.type as 'routing' | 'retry' | 'escalation' | 'ac_extracted',
          content: msg.content,
          metadata: meta,
        })
        break
      }

      case 'step_done':
      case 'thinking':
        flushStepGroup()
        break

      default:
        break
    }
  }

  flushStepGroup()
  return items
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
  addMessage: (sessionId: string, msg: ChatMessageUI) => void
  updateMessage: (sessionId: string, id: string, updates: Partial<ChatMessageUI>) => void
  setMessages: (sessionId: string, msgs: ChatMessageUI[]) => void
  clearMessages: (sessionId: string) => void
  setStreaming: (text: string | null) => void
  appendStreamToken: (token: string) => void
  setThinking: (thinking: boolean) => void
  setContextFill: (data: ContextFillState | null) => void
}

export const useChatStore = create<ChatState>((set) => ({
  messages: {},
  streamingText: null,
  isThinking: false,
  contextFill: null,
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
    updated[idx] = { ...updated[idx], ...updates }
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
}))
