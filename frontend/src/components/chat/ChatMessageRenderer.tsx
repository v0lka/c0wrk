import React from 'react'
import { DisplayItem } from '@/stores/chatStore'
import { UserMessage } from './UserMessage'
import { AssistantMessage } from './AssistantMessage'
import { ThoughtBlock } from './ThoughtBlock'
import { ToolBlock } from './ToolBlock'
import { PlanStepBlock } from './PlanStepBlock'
import { ToolConfirmation } from './ToolConfirmation'
import { AskUserPanel } from './AskUserPanel'
import { ErrorBlock } from './ErrorBlock'
import { ServiceMessage } from './ServiceMessage'
import { ActionPlaceholder } from './ActionPlaceholder'
import { ThoughtGroupBlock } from './ThoughtGroupBlock'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { ActivityIndicator } from './ActivityIndicator'
import { CheckCircle2 } from 'lucide-react'

interface ChatMessageRendererProps {
  displayItems: DisplayItem[]
  lastUserMessageId: string | null
  streamingText: string | null
}

// Recursive render function for DisplayItems (supports PlanStepBlock children)
function renderDisplayItem(item: DisplayItem, lastUserMessageId: string | null): React.ReactNode {
  // Skip the last user message since it's pinned at the top
  if (item.kind === 'user' && lastUserMessageId && item.message.id === lastUserMessageId) {
    return null
  }
  switch (item.kind) {
    case 'user':
      return <UserMessage key={item.message.id} content={item.message.content} timestamp={item.message.timestamp} />
    case 'assistant':
      return <AssistantMessage key={item.message.id} content={item.message.content} />
    case 'thought':
      return <ThoughtBlock key={item.id} content={item.content} reasoning={item.reasoning} />
    case 'tool':
      return <ToolBlock key={item.id} toolName={item.toolName} args={item.args} parsedArgs={item.parsedArgs} result={item.result} resultLen={item.resultLen} status={item.status} />
    case 'plan_step':
      return <PlanStepBlock key={item.id} stepId={item.stepId} stepNum={item.stepNum} title={item.title} status={item.status} duration={item.duration} isRetry={item.isRetry} children={item.children} renderItem={(child) => renderDisplayItem(child, lastUserMessageId)} />
    case 'tool_confirm':
      return <ToolConfirmation key={item.message.id} sessionId={item.message.sessionId} metadata={item.message.metadata} />
    case 'ask_user':
      return <AskUserPanel key={item.message.id} sessionId={item.message.sessionId} metadata={item.message.metadata} />
    case 'error':
      return <ErrorBlock key={item.message.id} content={item.message.content} />
    case 'service':
      return <ServiceMessage key={item.id} id={item.id} variant={item.variant} content={item.content} metadata={item.metadata} />
    case 'step_finish':
      return (
        <div key={item.id} className="flex items-center gap-1.5 text-sm text-muted-foreground">
          <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" />
          <span>{item.stepNum ? `Finished step ${item.stepNum}` : 'Finished'}</span>
        </div>
      )
    case 'action_placeholder':
      return <ActionPlaceholder key={item.id} label={item.label} />
    case 'thought_group':
      return <ThoughtGroupBlock key={item.id} thoughts={item.thoughts} />
    default:
      return null
  }
}

export function ChatMessageRenderer({
  displayItems,
  lastUserMessageId,
  streamingText,
}: ChatMessageRendererProps): React.ReactNode {
  return (
    <div className="p-4 space-y-4 min-w-0">
      {displayItems.map((item, idx) => (
        <ErrorBoundary key={'id' in item ? item.id : 'message' in item ? item.message.id : `item-${idx}`} fallback={<div className="text-xs text-destructive p-2">Failed to render message</div>}>
          {renderDisplayItem(item, lastUserMessageId)}
        </ErrorBoundary>
      ))}

      {/* Streaming text indicator */}
      {streamingText && (
        <AssistantMessage
          content={streamingText}
          isStreaming
        />
      )}

      {/* Activity indicator */}
      <ActivityIndicator />
    </div>
  )
}
