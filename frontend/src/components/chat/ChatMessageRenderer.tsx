import React from 'react'
import type { DisplayItem, DisplayItemKind } from '@/types/messages'
import { bookmarkKey } from '@/lib/bookmarks'
import { splitTurnWork } from '@/lib/turnWork'
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
import { AutonomyDecisionBlock } from './AutonomyDecisionBlock'
import { ReflectionBlock } from './ReflectionBlock'
import { PlanApprovalPanel } from './PlanApprovalPanel'
import { GoalProposalPanel } from './GoalProposalPanel'
import { ThoughtGroupBlock } from './ThoughtGroupBlock'
import { ChecklistCard } from './ChecklistCard'
import { BookmarkStar } from './BookmarkStar'
import { BookmarkableRow } from './BookmarkableRow'
import { TurnWorkBlock } from './TurnWorkBlock'
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
  autonomy_decision: AutonomyDecisionBlock as ItemRenderer,
  plan_step: PlanStepBlock as ItemRenderer,
  subagent: SubAgentBlock as ItemRenderer,
  reflection: ReflectionBlock as ItemRenderer,
  step_finish: StepFinishMarker as ItemRenderer,
  context_compaction: ContextCompactionBlock as ItemRenderer,
  memory_read: MemoryReadBlock as ItemRenderer,
  plan_review: PlanApprovalPanel as ItemRenderer,
  goal_proposal: GoalProposalPanel as ItemRenderer,
  checklist: ChecklistCard as ItemRenderer,
}

export function CompactErrorFallback() {
  return <div className="text-xs text-destructive p-2">Failed to render message</div>
}

/** Shared empty array — a stable tail for non-last segments. */
const EMPTY: DisplayItem[] = []

const compactErrorFallback = <CompactErrorFallback />

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
  /**
   * Trailing content of the live turn — the streaming answer. Rendered
   * INSIDE the live turn's work block (below the work list) while the block
   * is open, so the stream never jumps between containers.
   * `trailingFooter` carries the always-visible trailing chrome (the activity
   * indicator) that renders OUTSIDE the block.
   */
  trailingContent?: React.ReactNode
  /**
   * Always-visible trailing chrome rendered OUTSIDE the live turn's work
   * block — currently the ActivityIndicator. Because it sits outside the
   * collapsible, the live status ("Thinking...", "Routing request...", the
   * gap dot) stays visible even when the user collapses the live block — the
   * same "status outside the collapsed container" contract the plan view
   * follows for its steps.
   */
  trailingFooter?: React.ReactNode
  /**
   * Whether the session's task is active — decides whether the last turn is
   * still LIVE (block open, running status) or has settled (block collapsed;
   * completed/interrupted status). Only the LAST turn consults it.
   */
  lastTurnActive?: boolean
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
  trailingFooter,
  lastTurnActive = false,
  bookmarkable = true,
}: ChatMessageRendererProps) {
  if (!stickyUserMessages) {
    return (
      <BookmarkableContext.Provider value={bookmarkable}>
        {items.map((item) => renderItem(item, false, bookmarkable))}
        {trailingContent}
        {trailingFooter}
      </BookmarkableContext.Provider>
    )
  }

  const turns = groupIntoStickyTurns(items)
  return (
    <BookmarkableContext.Provider value={bookmarkable}>
      {turns.map((turn, index) => {
        const startsWithUser = turn[0]?.kind === 'user'
        const isLastTurn = index === turns.length - 1
        // Turn segmentation: the turn's work region is cut into ORDERED
        // SEGMENTS by its top-level plan/subagent step blocks — each segment
        // renders [block → its steps]; the step that closed a segment is the
        // FIRST pinned of the next one, so the sequence reproduces the stream
        // exactly: work₁ → step₁ → work₂ → step₂ → … → tail. Only the LAST
        // segment is the live block; every earlier one settled when its step
        // appeared. Turns without a work region keep the plain flat layout.
        const { segments, tail } = splitTurnWork(turn)
        // "Live" = the turn can still produce new items: it is the session's
        // last turn AND the session's task is active. When the task ends
        // without an answer (stop / error / app exit) the block settles the
        // same way a nudge-superseded turn does — fixing both the
        // nudge-superseded-turn rendering and the never-collapsing historical
        // dead turn after a reload.
        const live = isLastTurn && lastTurnActive
        // Tail content of the live turn (the streaming answer) streams INSIDE
        // the live segment's block via its render-slot: the block is open
        // while the turn runs, so the stream is visible from the first chunk,
        // and the settled answer swaps to a tail row without the stream ever
        // jumping between containers. The activity indicator is NOT in the
        // slot — it renders OUTSIDE the blocks (below them), so it stays
        // visible when the user collapses the live block.
        const slot = isLastTurn ? trailingContent : undefined
        // Every segment mounts a block — including empty-work ones (steps
        // with no observable work between them render their block as a thin
        // separator row) — EXCEPT when the whole turn has no segments at all
        // (a plain chat turn: user → answer). The LAST segment is the live
        // one; earlier segments are superseded (settled from birth).
        const hasSegments = segments.length > 0
        return (
          <div key={bookmarkKey(turn[0]!)} className="space-y-4 min-w-0">
            {/* A turn that does not start with a user message (an orphan
             * lead — service/status rows before the first user message) has
             * its ENTIRE content inside the last segment's work (splitTurnWork
             * anchors on the last user message, -1 when absent), so there is
             * no separate lead row to render and no double render. */}
            {startsWithUser && renderItem(turn[0]!, true, bookmarkable)}
            {segments.map((seg, segIdx) => {
              const isLastSegment = segIdx === segments.length - 1
              return (
                <div key={seg.work[0] ? bookmarkKey(seg.work[0]) : seg.pinned[0] ? bookmarkKey(seg.pinned[0]) : segIdx} className="space-y-4 min-w-0">
                  {seg.work.length > 0 && (
                    <TurnWorkBlock
                      live={live && isLastSegment}
                      superseded={!isLastSegment}
                      work={seg.work}
                      tail={isLastSegment ? tail : EMPTY}
                      tailSlot={isLastSegment ? slot : undefined}
                    />
                  )}
                  {/* Pinned plan/subagent steps render OUTSIDE the work
                   * block — their status chips must survive a collapse of the
                   * work block (the plan-panel contract). */}
                  {seg.pinned.map((item) => renderItem(item, false, bookmarkable))}
                  {/* An empty LAST segment (steps ran with no observable
                   * work after them) keeps the stream/tail flat — no empty
                   * "Work steps (0)" block. */}
                  {isLastSegment && seg.work.length === 0 && slot}
                  {isLastSegment && tail.map((item) => renderItem(item, false, bookmarkable))}
                </div>
              )
            })}
            {!hasSegments && slot}
            {!hasSegments && tail.map((item) => renderItem(item, false, bookmarkable))}
            {/* The activity indicator renders OUTSIDE the work blocks (and
             * outside any collapsible), so the live status survives a manual
             * collapse of the live turn's block. Last turn only. */}
            {isLastTurn && trailingFooter}
          </div>
        )
      })}
      {turns.length === 0 && trailingContent}
      {turns.length === 0 && trailingFooter}
    </BookmarkableContext.Provider>
  )
}
