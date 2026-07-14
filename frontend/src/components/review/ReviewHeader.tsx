import { useState } from 'react'
import { Check, Send, Loader2, MessageSquare, X, GitCommit } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useReviewStore, totalCommentCount } from '@/stores/reviewStore'
import * as reviewApi from '@/api/review'
import { useReviewActions } from './useReviewActions'

interface ReviewHeaderProps {
  sessionId: string
  /** Read-only mode: hide all controls (comment/submit/approve buttons). */
  readOnly?: boolean
  /** Commit SHA shown for context in read-only commit-review mode. */
  commitSha?: string
}

export function ReviewHeader({ sessionId, readOnly, commitSha }: ReviewHeaderProps) {
  const [showGeneral, setShowGeneral] = useState(false)
  const [draft, setDraft] = useState('')
  const reviewState = useReviewStore((s) => s.bySession[sessionId])
  const setGeneralComment = useReviewStore((s) => s.setGeneralComment)
  const { hasComments, isStaging, isSubmitting, handleApprove, handleSubmit } = useReviewActions(sessionId)

  const generalComment = reviewState?.generalComment ?? ''
  const totalComments = reviewState ? totalCommentCount(reviewState) : 0
  const isBusy = isStaging || isSubmitting

  // Read-only mode: stripped-down header with no controls. All hooks above
  // are still called unconditionally (React rules of hooks), but none of the
  // interactive state is rendered.
  if (readOnly) {
    return (
      <div className="shrink-0 border-b border-border bg-secondary/30 px-3 py-2">
        <div className="flex items-center gap-2">
          <GitCommit className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="text-sm font-medium">Commit Review</span>
          {commitSha && (
            <code className="text-xs text-info font-mono">{commitSha.slice(0, 7)}</code>
          )}
        </div>
      </div>
    )
  }

  const openGeneral = () => {
    setDraft(generalComment)
    setShowGeneral(true)
  }

  const handleGeneralSave = () => {
    setGeneralComment(sessionId, draft)
    reviewApi.saveReviewGeneralComment(sessionId, draft).catch(() => {})
    setShowGeneral(false)
  }

  return (
    <div className="shrink-0 border-b border-border bg-secondary/30 px-3 py-2 space-y-2">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium">Code Review</span>
          {totalComments > 0 && (
            <span className="text-xs text-muted-foreground">
              {totalComments} comment{totalComments !== 1 ? 's' : ''}
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="xs"
            onClick={() => (showGeneral ? setShowGeneral(false) : openGeneral())}
            disabled={isBusy}
          >
            <MessageSquare className="h-3 w-3 mr-1" />
            Comment All
          </Button>
          {hasComments ? (
            <Button
              size="xs"
              onClick={() => void handleSubmit()}
              disabled={isBusy}
            >
              {isSubmitting ? (
                <Loader2 className="h-3 w-3 mr-1 animate-spin" />
              ) : (
                <Send className="h-3 w-3 mr-1" />
              )}
              Submit
            </Button>
          ) : (
            <Button
              size="xs"
              onClick={() => void handleApprove()}
              disabled={isBusy}
            >
              {isStaging ? (
                <Loader2 className="h-3 w-3 mr-1 animate-spin" />
              ) : (
                <Check className="h-3 w-3 mr-1" />
              )}
              Approve
            </Button>
          )}
        </div>
      </div>

      {showGeneral && (
        <div className="flex items-start gap-2">
          <textarea
            className="flex-1 rounded-md border border-border bg-background p-2 text-xs resize-none"
            rows={2}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder="General review comment..."
            autoFocus
          />
          <Button
            size="xs"
            onClick={handleGeneralSave}
            className="shrink-0"
          >
            Save
          </Button>
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={() => setShowGeneral(false)}
            title="Close"
          >
            <X className="h-3 w-3" />
          </Button>
        </div>
      )}
    </div>
  )
}
