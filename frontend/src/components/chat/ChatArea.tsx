import { useEffect, useLayoutEffect, useRef, useMemo } from 'react'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useChatStore, ChatMessageUI, MessageType, groupMessages } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { UserMessage } from './UserMessage'
import { AssistantMessage } from './AssistantMessage'
import { ThoughtBlock } from './ThoughtBlock'
import { StepGroup } from './StepGroup'
import { ToolConfirmation } from './ToolConfirmation'
import { ErrorBlock } from './ErrorBlock'
import { PlanCard } from './PlanCard'
import { EvalCard } from './EvalCard'
import { ReflectionCard } from './ReflectionCard'
import { ServiceMessage } from './ServiceMessage'
import { useSessionEvents } from '@/hooks/useSessionEvents'
import { MessageCircle } from 'lucide-react'
import { GetSessionHistory } from '../../../wailsjs/go/main/App'
import { session } from '../../../wailsjs/go/models'

// Stable empty array to prevent infinite re-render loops in Zustand selectors
const EMPTY_MESSAGES: ChatMessageUI[] = []

// Role-to-type mapping
const roleToType: Record<string, MessageType> = {
  user: 'user',
  assistant: 'assistant',
  tool_call: 'tool_call',
  tool_result: 'tool_result',
  routing: 'routing',
  eval: 'eval',
  reflection: 'reflection',
  plan: 'plan',
  error: 'error',
  thought: 'thought',
  thinking: 'thinking',
  step_done: 'step_done',
  plan_step_start: 'plan_step_start',
  plan_step_complete: 'plan_step_complete',
  retry: 'retry',
  escalation: 'escalation',
  ac_extracted: 'ac_extracted',
  subagent_launch: 'subagent_launch',
  subagent_complete: 'subagent_complete',
}

// Convert ChatMessage to ChatMessageUI
function chatMessageToUI(msg: session.ChatMessage): ChatMessageUI {
  let metadata: Record<string, unknown> | undefined
  if (msg.metadata) {
    try {
      metadata = typeof msg.metadata === 'string' ? JSON.parse(msg.metadata) : msg.metadata
    } catch {
      metadata = undefined
    }
  }
  return {
    id: `history-${msg.id}`,
    sessionId: msg.session_id,
    type: roleToType[msg.role] || 'assistant',
    content: msg.content,
    metadata,
    timestamp: msg.created_at ? new Date(msg.created_at).getTime() : 0,
  }
}

export function ChatArea() {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const messages = useChatStore(s =>
    activeSessionId ? (s.messages[activeSessionId] ?? EMPTY_MESSAGES) : EMPTY_MESSAGES
  )
  const streamingText = useChatStore(s => s.streamingText)
  const setMessages = useChatStore(s => s.setMessages)
  const scrollRef = useRef<HTMLDivElement>(null)

  // Subscribe to session events
  useSessionEvents(activeSessionId)

  // Load persisted history when active session changes
  useEffect(() => {
    if (!activeSessionId) return

    // Skip if messages already loaded for this session
    const existing = useChatStore.getState().messages[activeSessionId]
    if (existing && existing.length > 0) return

    // Load history from backend
    GetSessionHistory(activeSessionId).then((history) => {
      if (history && history.length > 0) {
        const uiMessages = history.map(chatMessageToUI)
        setMessages(activeSessionId, uiMessages)
      }
    }).catch((err) => {
      console.error('Failed to load session history:', err)
    })
  }, [activeSessionId, setMessages])

  // Auto-scroll to bottom on new messages or streaming text
  useLayoutEffect(() => {
    if (scrollRef.current) {
      const viewport = scrollRef.current.querySelector('[data-slot="scroll-area-viewport"]')
      if (viewport) {
        viewport.scrollTop = viewport.scrollHeight
      }
    }
  }, [messages, streamingText])

  const displayItems = useMemo(() => groupMessages(messages), [messages])

  if (!activeSessionId) {
    return (
      <div className="flex-1 flex items-center justify-center text-muted-foreground">
        <div className="flex flex-col items-center gap-3">
          <MessageCircle className="h-12 w-12 opacity-20" />
          <p>Send a message to start a new conversation</p>
        </div>
      </div>
    )
  }

  const hasContent = messages.length > 0 || !!streamingText

  if (!hasContent) {
    return (
      <div className="flex-1 flex items-center justify-center text-muted-foreground">
        <div className="flex flex-col items-center gap-3">
          <MessageCircle className="h-12 w-12 opacity-20" />
          <p>Send a message to start the conversation</p>
        </div>
      </div>
    )
  }

  return (
    <ScrollArea className="flex-1 min-w-0" ref={scrollRef}>
      <div className="p-4 space-y-4 min-w-0">
        {displayItems.map((item) => {
          switch (item.kind) {
            case 'user':
              return <UserMessage key={item.message.id} content={item.message.content} timestamp={item.message.timestamp} />
            case 'assistant':
              return <AssistantMessage key={item.message.id} content={item.message.content} />
            case 'thought':
              return <ThoughtBlock key={item.id} id={item.id} stepNum={item.stepNum} content={item.content} />
            case 'step_group':
              return <StepGroup key={item.id} id={item.id} steps={item.steps} />
            case 'tool_confirm':
              return <ToolConfirmation key={item.message.id} metadata={item.message.metadata} />
            case 'error':
              return <ErrorBlock key={item.message.id} content={item.message.content} />
            case 'plan':
              return <PlanCard key={item.id} id={item.id} steps={item.steps} />
            case 'eval':
              return <EvalCard key={item.id} id={item.id} passed={item.passed} total={item.total} criteria={item.criteria} />
            case 'reflection':
              return <ReflectionCard key={item.id} id={item.id} summary={item.summary} insights={item.insights} attempt={item.attempt} maxAttempts={item.maxAttempts} />
            case 'service':
              return <ServiceMessage key={item.id} id={item.id} variant={item.variant} content={item.content} metadata={item.metadata} />
            default:
              return null
          }
        })}

        {/* Streaming text indicator */}
        {streamingText && (
          <AssistantMessage
            content={streamingText}
            isStreaming
          />
        )}
      </div>
    </ScrollArea>
  )
}
