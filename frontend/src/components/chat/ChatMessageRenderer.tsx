import React from 'react'
import { DisplayItem } from '@/stores/chatStore'
import { UserMessage } from './UserMessage'
import { AssistantMessage } from './AssistantMessage'
import { ThoughtBlock } from './ThoughtBlock'
import { ToolBlock } from './ToolBlock'
import { PlanStepBlock } from './PlanStepBlock'
import { ToolConfirmation } from './ToolConfirmation'
import { AskUserPanel } from './AskUserPanel'
import { ResumeActionPanel } from './ResumeActionPanel'
import { StepLimitPrompt } from './StepLimitPrompt'
import { ErrorBlock } from './ErrorBlock'
import { ServiceMessage } from './ServiceMessage'
import { ReflectionBlock } from './ReflectionBlock'
import { ActionPlaceholder } from './ActionPlaceholder'
import { ThoughtGroupBlock } from './ThoughtGroupBlock'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { ActivityIndicator } from './ActivityIndicator'
import { CheckCircle2, BookOpen, Minimize2, ChevronDown, ChevronRight, Check, Loader2, X } from 'lucide-react'

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'

interface ChatMessageRendererProps {
  displayItems: DisplayItem[]
  lastUserMessageId: string | null
  streamingText: string | null
}

const MEMORY_LABELS: Record<string, string> = {
  read_evidence: 'Read evidence',
  read_step_output: 'Read step output',
  list_step_outputs: 'Listed step outputs',
  store_fact: 'Stored fact',
  search_facts: 'Searched facts',
}

interface MemoryBlockProps {
  toolName: string
  args: string
  parsedArgs?: Record<string, unknown>
  result?: string
  resultLen?: number
  status: 'running' | 'success' | 'error'
}

function formatMemoryResultLen(len: number): string {
  if (len >= 1000) {
    return (len / 1000).toFixed(1).replace(/\.0$/, '') + 'K chars'
  }
  return len + ' chars'
}

const MemoryBlock = React.memo(function MemoryBlock({ toolName, args, parsedArgs, result, resultLen, status }: MemoryBlockProps) {
  const [isOpen, setIsOpen] = React.useState(false)
  const [showFull, setShowFull] = React.useState(false)

  const MAX_PREVIEW = 200
  const label = MEMORY_LABELS[toolName] ?? 'Memory operation'

  const argEntries: [string, unknown][] | null = (() => {
    try {
      const obj = parsedArgs ?? JSON.parse(args)
      if (obj && typeof obj === 'object' && !Array.isArray(obj)) {
        return Object.entries(obj)
      }
    } catch { /* fall through */ }
    return null
  })()

  const formatValue = (v: unknown): string => {
    if (v === null || v === undefined) return String(v)
    if (typeof v === 'string') return v
    if (typeof v === 'object') return JSON.stringify(v)
    return String(v)
  }

  const formattedArgs = argEntries
    ? argEntries.map(([k, v]) => `- ${k}: ${formatValue(v)}`).join('\n')
    : args

  const isArgsLong = formattedArgs.length > MAX_PREVIEW || formattedArgs.includes('\n')
  const displayArgs = (!showFull && isArgsLong) ? formattedArgs.slice(0, MAX_PREVIEW) + '...' : formattedArgs

  const isResultLong = !!result && (result.length > MAX_PREVIEW || result.includes('\n'))
  const displayResult = result && (!showFull && isResultLong) ? result.slice(0, MAX_PREVIEW) + '...' : result

  const hasLongContent = isArgsLong || isResultLong

  const StatusIcon = status === 'success' ? Check : status === 'error' ? X : Loader2
  const statusClass = status === 'success'
    ? 'text-success'
    : status === 'error'
      ? 'text-destructive'
      : 'text-muted-foreground animate-spin'

  return (
    <Collapsible open={isOpen} onOpenChange={setIsOpen} className="group">
      <CollapsibleTrigger className="flex items-center gap-1.5 text-muted-foreground hover:text-foreground transition-colors">
        <span className="opacity-0 group-hover:opacity-100 transition-opacity inline-flex">
          {isOpen ? (
            <ChevronDown className="h-3.5 w-3.5" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5" />
          )}
        </span>
        <StatusIcon className={`h-3.5 w-3.5 ${statusClass}`} />
        <BookOpen className="h-3.5 w-3.5 text-accent" />
        <span className="text-sm">{label}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-2 border-l-2 border-accent/30 bg-muted/30 rounded p-3 space-y-3 min-w-0">
          {formattedArgs && (
            <div>
              <span className="text-xs text-muted-foreground font-medium">Arguments</span>
              <pre className="mt-1 font-mono text-xs text-muted-foreground whitespace-pre-wrap break-all">
                {displayArgs}
              </pre>
            </div>
          )}
          {result !== undefined && (
            <div>
              <div className="flex items-center gap-2">
                <span className="text-xs text-muted-foreground font-medium">Result</span>
                {resultLen !== undefined && resultLen > 500 && (
                  <span className="text-xs text-muted-foreground/50 bg-muted/50 px-1.5 py-0.5 rounded">
                    {formatMemoryResultLen(resultLen)}
                  </span>
                )}
              </div>
              <pre className="mt-1 font-mono text-xs text-muted-foreground whitespace-pre-wrap break-all">
                {displayResult}
              </pre>
            </div>
          )}
          {hasLongContent && (
            <button
              onClick={(e) => {
                e.stopPropagation()
                setShowFull(!showFull)
              }}
              className="text-xs text-muted-foreground hover:text-foreground hover:bg-accent/50 active:bg-accent/70 rounded px-1 py-0.5 mt-1 transition-colors"
            >
              {showFull ? 'Show less' : 'Show more'}
            </button>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
})

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
      return <ToolBlock key={item.id} toolName={item.toolName} args={item.args} parsedArgs={item.parsedArgs} result={item.result} resultLen={item.resultLen} status={item.status} source={item.source} />
    case 'plan_step':
      return <PlanStepBlock key={item.id} stepId={item.stepId} stepNum={item.stepNum} title={item.title} description={item.description} status={item.status} duration={item.duration} error={item.error} isRetry={item.isRetry} children={item.children} renderItem={(child) => renderDisplayItem(child, lastUserMessageId)} />
    case 'tool_confirm':
      return <ToolConfirmation key={item.message.id} sessionId={item.message.sessionId} metadata={item.message.metadata} />
    case 'ask_user':
      return <AskUserPanel key={item.message.id} sessionId={item.message.sessionId} metadata={item.message.metadata} />
    case 'resume_action':
      return <ResumeActionPanel key={item.message.id} sessionId={item.message.sessionId} content={item.message.content} metadata={item.message.metadata} />
    case 'step_limit':
      return <StepLimitPrompt key={item.message.id} sessionId={item.message.sessionId} metadata={item.message.metadata} />
    case 'error':
      return <ErrorBlock key={item.message.id} content={item.message.content} />
    case 'service':
      return <ServiceMessage key={item.id} id={item.id} variant={item.variant} content={item.content} metadata={item.metadata} />
    case 'step_finish':
      return (
        <div key={item.id} className="flex items-center gap-1.5 text-sm text-muted-foreground">
          <CheckCircle2 className="h-3.5 w-3.5 text-success" />
          <span>{item.stepNum ? `Finished step ${item.stepNum}` : 'Finished'}</span>
        </div>
      )
    case 'memory_read':
      return <MemoryBlock key={item.id} toolName={item.toolName} args={item.args} parsedArgs={item.parsedArgs} result={item.result} resultLen={item.resultLen} status={item.status} />
    case 'action_placeholder':
      return <ActionPlaceholder key={item.id} label={item.label} />
    case 'context_compaction':
      return (
        <div key={item.id} className="flex items-center gap-1.5 text-sm text-muted-foreground">
          <Minimize2 className="h-3.5 w-3.5 text-info" />
          <span>Context compacted from {item.beforePercent}% to {item.afterPercent}%</span>
        </div>
      )
    case 'thought_group':
      return <ThoughtGroupBlock key={item.id} thoughts={item.thoughts} />
    case 'reflection':
      return <ReflectionBlock key={item.id} summary={item.summary} suggestedAction={item.suggestedAction} rootCause={item.rootCause} failureAnalysis={item.failureAnalysis} actionPlan={item.actionPlan} reasoning={item.reasoning} hypotheses={item.hypotheses} attempt={item.attempt} maxAttempts={item.maxAttempts} />
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
