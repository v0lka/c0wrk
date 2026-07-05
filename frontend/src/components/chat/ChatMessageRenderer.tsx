import React from 'react'
import type { DisplayItem, DisplayItemKind } from '@/types/messages'
import { UserMessage } from './UserMessage'
import { AssistantMessage } from './AssistantMessage'
import { ThoughtBlock } from './ThoughtBlock'
import { ToolCard } from './toolCards'
import { PlanStepBlock } from './PlanStepBlock'
import { SubAgentBlock } from './SubAgentBlock'
import { ToolConfirmation } from './ToolConfirmation'
import { AskUserPanel } from './AskUserPanel'
import { ResumeActionPanel } from './ResumeActionPanel'
import { StepLimitPrompt } from './StepLimitPrompt'
import { ErrorBlock } from './ErrorBlock'
import { ServiceMessage } from './ServiceMessage'
import { ReflectionBlock } from './ReflectionBlock'
import { ActionPlaceholder } from './ActionPlaceholder'
import { ThoughtGroupBlock } from './ThoughtGroupBlock'
import { ChecklistCard } from './ChecklistCard'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { CheckCircle2, Minimize2, BookOpen } from 'lucide-react'

// --- Inline small components ---

function StepFinishMarker({ item }: { item: Extract<DisplayItem, { kind: 'step_finish' }> }) {
  return (
    <div className="flex items-center gap-1.5 text-sm text-muted-foreground">
      <CheckCircle2 className="h-3.5 w-3.5 text-success" />
      <span>{item.stepNum ? `Finished step ${item.stepNum}` : 'Finished'}</span>
    </div>
  )
}

function ContextCompactionBlock({ item }: { item: Extract<DisplayItem, { kind: 'context_compaction' }> }) {
  return (
    <div className="flex items-center gap-1.5 text-sm text-muted-foreground">
      <Minimize2 className="h-3.5 w-3.5 text-info" />
      <span>Context compacted from {item.beforePercent}% to {item.afterPercent}%</span>
    </div>
  )
}

function MemoryReadBlock({ item }: { item: Extract<DisplayItem, { kind: 'memory_read' }> }) {
  return (
    <div className="flex items-center gap-1.5 text-sm text-muted-foreground">
      <BookOpen className="h-3.5 w-3.5 text-info" />
      <span>{item.content}</span>
    </div>
  )
}

// --- Component Registry ---

type ItemRenderer = React.ComponentType<{ item: Extract<DisplayItem, { kind: DisplayItemKind }> }>

const renderers: Record<DisplayItemKind, ItemRenderer> = {
  user: UserMessage as ItemRenderer,
  assistant: AssistantMessage as ItemRenderer,
  thought: ThoughtBlock as ItemRenderer,
  thought_group: ThoughtGroupBlock as ItemRenderer,
  tool: ToolCard as ItemRenderer,
  tool_confirm: ToolConfirmation as ItemRenderer,
  ask_user: AskUserPanel as ItemRenderer,
  step_limit: StepLimitPrompt as ItemRenderer,
  resume_action: ResumeActionPanel as ItemRenderer,
  error: ErrorBlock as ItemRenderer,
  service: ServiceMessage as ItemRenderer,
  plan_step: PlanStepBlock as ItemRenderer,
  subagent: SubAgentBlock as ItemRenderer,
  reflection: ReflectionBlock as ItemRenderer,
  step_finish: StepFinishMarker as ItemRenderer,
  action_placeholder: ActionPlaceholder as ItemRenderer,
  context_compaction: ContextCompactionBlock as ItemRenderer,
  memory_read: MemoryReadBlock as ItemRenderer,
  plan_review: (() => null) as ItemRenderer,
  checklist: ChecklistCard as ItemRenderer,
}

export function CompactErrorFallback() {
  return <div className="text-xs text-destructive p-2">Failed to render message</div>
}

const compactErrorFallback = <CompactErrorFallback />

function getItemKey(item: DisplayItem): string {
  if ('message' in item) return item.message.id
  return item.id
}

export function ChatMessageRenderer({ items }: { items: DisplayItem[] }) {
  return (
    <>
      {items.map((item) => {
        const Component = renderers[item.kind]
        if (!Component) return null
        return (
          <ErrorBoundary key={getItemKey(item)} fallback={compactErrorFallback}>
            <Component item={item} />
          </ErrorBoundary>
        )
      })}
    </>
  )
}
