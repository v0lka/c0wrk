import { Check, X, GitPullRequest } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useChatStore } from '@/stores/chatStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useReviewStore } from '@/stores/reviewStore'
import * as reviewApi from '@/api/review'
import { getReviewPromptResolution, reviewPromptResolved } from '@/types/messages'
import type { DisplayItem } from '@/types/messages'

type ReviewPromptItem = Extract<DisplayItem, { kind: 'review_prompt' }>

export function ReviewPromptBlock({ item }: { item: ReviewPromptItem }) {
  const { sessionId, metadata } = item.message
  const updateMessage = useChatStore((s) => s.updateMessage)
  const decision = getReviewPromptResolution(metadata)

  if (decision === 'enter') {
    return (
      <div className="rounded-md border border-success/30 bg-success/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-success">
          <GitPullRequest className="h-3.5 w-3.5 shrink-0" />
          <span>Entered review mode</span>
        </div>
      </div>
    )
  }

  if (decision === 'decline') {
    return (
      <div className="rounded-md border border-border/50 bg-secondary/20 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <X className="h-3.5 w-3.5 shrink-0" />
          <span>Review declined</span>
        </div>
      </div>
    )
  }

  // Stale: resolved without known decision
  if (metadata?.resolved === true) return null

  const handleEnter = () => {
    updateMessage(sessionId, item.message.id, { metadata: reviewPromptResolved('enter') })
    const { openFile, setCollapsed } = useFileViewerStore.getState()
    const { openReviewPage, enterReviewLoop } = useReviewStore.getState()
    openFile('c0wrk:review')
    setCollapsed(false)
    openReviewPage(sessionId)
    enterReviewLoop(sessionId)
    void useReviewStore.getState().loadReview(sessionId)
    void reviewApi.setReviewStatus(sessionId, 'active').catch(() => {})
  }

  const handleDecline = () => {
    updateMessage(sessionId, item.message.id, { metadata: reviewPromptResolved('decline') })
    useReviewStore.getState().resetLoopFlags(sessionId)
  }

  return (
    <div className="rounded-md border border-info/30 bg-info/5 px-3 py-2">
      <div className="flex items-center gap-1.5 text-xs font-medium text-info">
        <GitPullRequest className="h-3.5 w-3.5 shrink-0" />
        <span>Enter review mode?</span>
      </div>
      <p className="mt-1 text-xs text-muted-foreground">
        Task completed with changes. Review them before committing?
      </p>
      <div className="mt-2 flex gap-2">
        <Button size="sm" onClick={handleEnter} className="text-xs">
          <Check className="h-3 w-3 mr-1.5" />Yes
        </Button>
        <Button size="sm" variant="outline" onClick={handleDecline} className="text-xs">
          <X className="h-3 w-3 mr-1.5" />No
        </Button>
      </div>
    </div>
  )
}
