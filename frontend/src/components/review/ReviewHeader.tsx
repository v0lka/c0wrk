import { useState } from 'react'
import { Check, Send, Loader2, MessageSquare, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useReviewStore, totalCommentCount } from '@/stores/reviewStore'
import * as reviewApi from '@/api/review'
import { useReviewActions } from './useReviewActions'

interface ReviewHeaderProps {
  sessionId: string
}

export function ReviewHeader({ sessionId }: ReviewHeaderProps) {
  const [showGeneral, setShowGeneral] = useState(false)
  const reviewState = useReviewStore((s) => s.bySession[sessionId])
  const setGeneralComment = useReviewStore((s) => s.setGeneralComment)
  const { hasComments, isStaging, isSubmitting, handleApprove, handleSubmit } = useReviewActions(sessionId)

  const generalComment = reviewState?.generalComment ?? ''
  const totalComments = reviewState ? totalCommentCount(reviewState) : 0
  const isBusy = isStaging || isSubmitting

  const handleGeneralSave = (text: string) => {
    setGeneralComment(sessionId, text)
    reviewApi.saveReviewGeneralComment(sessionId, text).catch(() => {})
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
            onClick={() => setShowGeneral((v) => !v)}
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
            value={generalComment}
            onChange={(e) => handleGeneralSave(e.target.value)}
            placeholder="General review comment..."
            autoFocus
          />
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
