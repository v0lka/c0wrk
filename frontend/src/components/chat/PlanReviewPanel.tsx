import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ClipboardList, Check, X } from 'lucide-react'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatStore } from '@/stores/chatStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { usePlanReviewStore } from '@/stores/planReviewStore'
import { approvePlan, rejectPlan } from '@/api/planReview'
import { planReviewResolved } from '@/types/messages'
import type { DisplayItem } from '@/types/messages'

interface PlanReviewPanelProps {
  item: Extract<DisplayItem, { kind: 'plan_review' }>
}

export function PlanReviewPanel({ item }: PlanReviewPanelProps) {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const planReview = usePlanReviewStore((s) => s.planReview)
  const [feedback, setFeedback] = useState('')
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (!activeSessionId) return null
  const planPath = item.message.metadata?.planPath as string | undefined
  const planContent = item.message.metadata?.planContent as string | undefined
  const isResolved = item.message.metadata?.resolved === true

  if (isResolved) {
    const decision = item.message.metadata?.decision as string | undefined
    if (decision === 'accepted') {
      return (
        <div className="flex items-center gap-1.5 text-muted-foreground">
          <Check className="h-3.5 w-3.5 text-success" />
          <span className="text-sm">Plan accepted</span>
        </div>
      )
    }
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <X className="h-3.5 w-3.5 text-destructive" />
        <span className="text-sm">Plan rejected</span>
      </div>
    )
  }

  const handleAccept = async () => {
    if (!planPath || isSubmitting) return
    setIsSubmitting(true)
    setError(null)
    try {
      await approvePlan(activeSessionId, planPath)
      // Only mark resolved after the RPC succeeds. The plan_review_accepted
      // backend event serves as a safety net if this update races with it.
      useChatStore.getState().updateMessage(activeSessionId, item.message.id, {
        metadata: { ...item.message.metadata, resolved: true, decision: 'accepted' },
      })
      useChatStore.getState().setActivityStatus(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Approval failed')
    } finally {
      setIsSubmitting(false)
    }
  }

  const handleReject = async () => {
    if (!planPath || isSubmitting) return
    setIsSubmitting(true)
    setError(null)
    try {
      await rejectPlan(activeSessionId, feedback)
      useChatStore.getState().updateMessage(activeSessionId, item.message.id, {
        metadata: planReviewResolved('rejected'),
      })
      if (planPath) useFileViewerStore.getState().closeFile(planPath)
      useChatStore.getState().setActivityStatus(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Rejection failed')
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <div className="border-2 border-highlight/50 rounded-lg p-4 bg-highlight/5 max-w-full overflow-hidden">
      {!planReview && (
        <>
          <div className="flex items-center gap-2 mb-3">
            <ClipboardList className="h-4 w-4 text-highlight shrink-0" />
            <span className="text-sm font-medium">Plan is ready for review — open in right panel</span>
          </div>
          <div className="mb-4 space-y-2">
            {planPath && (
              <p className="text-sm">
                <span className="text-muted-foreground">Plan:</span>{' '}
                <span className="font-medium font-mono text-xs break-all">{planPath}</span>
              </p>
            )}
            {planContent && (
              <div className="min-w-0 overflow-hidden">
                <p className="text-xs text-muted-foreground mb-1">Preview:</p>
                <pre className="p-2 bg-background/50 rounded text-xs font-mono overflow-x-auto custom-scrollbar border border-border max-w-full min-w-0 max-h-24 overflow-y-auto">
                  <code>{planContent.length > 500 ? planContent.slice(0, 500) + '\u2026' : planContent}</code>
                </pre>
              </div>
            )}
          </div>
        </>
      )}
      {error && (
        <div className="mb-4 p-3 bg-destructive/10 border border-destructive/30 rounded-md">
          <p className="text-xs text-destructive">{error}</p>
        </div>
      )}
      <div className="flex flex-wrap gap-2 mb-3">
        <Button size="sm" onClick={handleAccept} disabled={isSubmitting} className="text-xs">
          Accept <Check className="ml-1 h-3 w-3" />
        </Button>
        <Button size="sm" variant="outline" onClick={handleReject} disabled={isSubmitting} className="text-xs">
          Reject <X className="ml-1 h-3 w-3" />
        </Button>
      </div>
      <Input
        value={feedback}
        onChange={(e) => setFeedback(e.target.value)}
        placeholder="Feedback (optional) — describe what needs to change..."
        className="text-xs"
      />
    </div>
  )
}
