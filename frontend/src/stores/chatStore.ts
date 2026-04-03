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
  metadata?: Record<string, unknown>
  timestamp: number
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
  | { kind: 'service'; id: string; variant: 'routing' | 'retry' | 'escalation'; content: string; metadata?: Record<string, unknown> }

export function groupMessages(messages: ChatMessageUI[]): DisplayItem[] {
  const items: DisplayItem[] = []
  let currentStepGroup: StepItem[] | null = null
  let stepGroupId = ''

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
        const meta = (typeof msg.metadata === 'object' && msg.metadata !== null)
          ? msg.metadata as Record<string, unknown>
          : undefined
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

      // Skip plan, eval, reflection, and ac_extracted events (handled in panelStore)
      case 'plan':
      case 'plan_step_start':
      case 'plan_step_complete':
      case 'eval':
      case 'reflection':
      case 'ac_extracted':
        break

      case 'routing':
      case 'retry':
      case 'escalation': {
        flushStepGroup()
        const meta = msg.metadata as Record<string, unknown> | undefined
        // Skip orchestration phase messages (handled in panelStore)
        if (meta?.phase === 'orchestration') {
          break
        }
        items.push({
          kind: 'service',
          id: msg.id,
          variant: msg.type as 'routing' | 'retry' | 'escalation',
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
  setActivityStatus: (status) => set({ activityStatus: status }),
  setTaskActive: (active) => set({ isTaskActive: active }),
  clearSessionUIState: () => set({
    activityStatus: null,
    streamingText: null,
    isThinking: false,
    contextFill: null,
  }),
}))
