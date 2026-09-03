import React from 'react'
import type { DisplayItem, DisplayItemKind } from '@/types/messages'
import { bookmarkKey } from '@/lib/bookmarks'
import { BookmarkableContext } from './BookmarkableContext'
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
import { PlanApprovalPanel } from './PlanApprovalPanel'
import { ReviewPromptBlock } from './ReviewPromptBlock'
import { GoalProposalPanel } from './GoalProposalPanel'
import { ThoughtGroupBlock } from './ThoughtGroupBlock'
import { ChecklistCard } from './ChecklistCard'
import { BookmarkStar } from './BookmarkStar'
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
  context_compaction: ContextCompactionBlock as ItemRenderer,
  memory_read: MemoryReadBlock as ItemRenderer,
  plan_review: PlanApprovalPanel as ItemRenderer,
  review_prompt: ReviewPromptBlock as ItemRenderer,
  goal_proposal: GoalProposalPanel as ItemRenderer,
  checklist: ChecklistCard as ItemRenderer,
}

export function CompactErrorFallback() {
  return <div className="text-xs text-destructive p-2">Failed to render message</div>
}

const compactErrorFallback = <CompactErrorFallback />

/**
 * BookmarkableRow — a chat item's left gutter + content.
 *
 * The unbookmarked star's visibility is driven by the ChatHoverRegion
 * controller (single active bookmark across the whole chat). This row carries
 * `data-bookmark-id={bookmarkKey(item)}`, which the region resolves via
 * `closest('[data-bookmark-id]')` to reveal exactly the row under the pointer
 * and hide every other star. The `group` class is retained for other hover
 * revealers (e.g. the Copy/Save actions in MessageFooter), not for the star.
 */
function BookmarkableRow({ item, content }: { item: DisplayItem; content: React.ReactNode }) {
  const key = bookmarkKey(item)
  return (
    <div
      data-bookmark-id={key}
      className="group relative flex items-start gap-2"
    >
      <div className="w-5 shrink-0 flex items-start">
        <BookmarkStar item={item} />
      </div>
      <div className="min-w-0 flex-1">{content}</div>
    </div>
  )
}

function renderItem(item: DisplayItem, stickyUserMessage: boolean, bookmarkable: boolean): React.ReactNode {
  const key = bookmarkKey(item)

  if (item.kind === 'user') {
    const star = bookmarkable ? <BookmarkStar item={item} /> : null
    const content = (
      <ErrorBoundary key={key} fallback={compactErrorFallback}>
        <UserMessage item={item} sticky={stickyUserMessage} bookmarkStar={stickyUserMessage ? star : null} />
      </ErrorBoundary>
    )
    if (!bookmarkable) return content
    // A sticky (pinned) user message cannot be wrapped in a layout shell — the
    // extra flex parent would shrink the sticky element's containing block and
    // break position:sticky — so its star is rendered inside UserMessage.
    if (stickyUserMessage) return content
    return <BookmarkableRow key={key} item={item} content={content} />
  }

  const Component = renderers[item.kind]
  if (!Component) return null
  const content = (
    <ErrorBoundary key={key} fallback={compactErrorFallback}>
      <Component item={item} />
    </ErrorBoundary>
  )
  if (!bookmarkable) return content
  return <BookmarkableRow key={key} item={item} content={content} />
}

function groupIntoStickyTurns(items: DisplayItem[]): DisplayItem[][] {
  const groups: DisplayItem[][] = []

  for (const item of items) {
    if (item.kind === 'user' || groups.length === 0) {
      groups.push([item])
    } else {
      groups[groups.length - 1]!.push(item)
    }
  }

  return groups
}

interface ChatMessageRendererProps {
  items: DisplayItem[]
  stickyUserMessages?: boolean
  trailingContent?: React.ReactNode
  /**
   * Whether to render the bookmark gutter/star and data-bookmark-id anchors.
   * Defaults true (chat stream). Disable for bookmark tooltip previews so a
   * rendered card does not render its own star.
   */
  bookmarkable?: boolean
}

export function ChatMessageRenderer({
  items,
  stickyUserMessages = false,
  trailingContent,
  bookmarkable = true,
}: ChatMessageRendererProps) {
  if (!stickyUserMessages) {
    return (
      <BookmarkableContext.Provider value={bookmarkable}>
        {items.map((item) => renderItem(item, false, bookmarkable))}
        {trailingContent}
      </BookmarkableContext.Provider>
    )
  }

  const turns = groupIntoStickyTurns(items)
  return (
    <BookmarkableContext.Provider value={bookmarkable}>
      {turns.map((turn, index) => {
        const startsWithUser = turn[0]?.kind === 'user'
        const isLastTurn = index === turns.length - 1
        return (
          <div key={bookmarkKey(turn[0]!)} className="space-y-4 min-w-0">
            {turn.map((item) => renderItem(item, startsWithUser && item.kind === 'user', bookmarkable))}
            {isLastTurn && trailingContent}
          </div>
        )
      })}
      {turns.length === 0 && trailingContent}
    </BookmarkableContext.Provider>
  )
}
