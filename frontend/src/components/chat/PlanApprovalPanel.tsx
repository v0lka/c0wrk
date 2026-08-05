import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Check, X, MessageSquare, ExternalLink, AlertTriangle, FileText } from 'lucide-react'
import { emit } from '@/api/runtime'
import type { DisplayItem } from '@/types/messages'
import { getPlanReviewResolution } from '@/types/messages'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'

interface PlanApprovalPanelProps {
  item: Extract<DisplayItem, { kind: 'plan_review' }>
}

export function PlanApprovalPanel({ item }: PlanApprovalPanelProps) {
  const sessionId = useSessionStore((s) => s.activeSessionId)
  const requestId = item.message.metadata?.request_id as string | undefined
  const planPath = item.message.metadata?.plan_path as string | undefined
  const error = item.message.metadata?.error as string | undefined
  const [showFeedback, setShowFeedback] = useState(false)
  const [feedback, setFeedback] = useState('')

  const decision = getPlanReviewResolution(item.message.metadata)

  if (decision === 'approve') {
    return (
      <div className="rounded-md border border-success/30 bg-success/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <Check className="h-3.5 w-3.5 shrink-0 text-success" />
          <span>Plan Review</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Plan approved</p>
      </div>
    )
  }
  if (decision === 'request_changes') {
    return (
      <div className="rounded-md border border-info/30 bg-info/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <MessageSquare className="h-3.5 w-3.5 shrink-0 text-info" />
          <span>Plan Review</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Changes requested</p>
      </div>
    )
  }
  if (decision === 'abandon') {
    return (
      <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <X className="h-3.5 w-3.5 shrink-0 text-destructive" />
          <span>Plan Review</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Plan abandoned</p>
      </div>
    )
  }
  // Resolved without a recorded decision — stale prompt reconciled on reload.
  if (item.message.metadata?.resolved === true) {
    return (
      <div className="rounded-md border border-border bg-background/50 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span>Plan Review</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Dismissed</p>
      </div>
    )
  }

  if (!requestId || !sessionId) return null

  const resolve = (d: 'approve' | 'request_changes' | 'abandon', fb?: string) => {
    try {
      emit('plan_approval_response', {
        request_id: requestId,
        decision: d,
        feedback: fb ?? '',
      })
      useChatStore.getState().updateMessage(sessionId, item.message.id, {
        metadata: { ...item.message.metadata, resolved: true, decision: d },
      })
    } catch {
      useChatStore.getState().updateMessage(sessionId, item.message.id, {
        metadata: { ...item.message.metadata, error: 'Failed to send approval response' },
      })
    }
  }

  const openInViewer = () => {
    if (!planPath) return
    useFileViewerStore.getState().openFile(planPath)
    useFileViewerStore.getState().setFileContent(planPath, item.message.content, 'markdown')
  }

  if (showFeedback) {
    return (
      <div className="rounded-md border border-info/30 bg-info/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <MessageSquare className="h-3.5 w-3.5 shrink-0 text-info" />
          <span>Plan Review</span>
        </div>
        <div className="mt-1.5 space-y-1.5">
          <p className="text-xs text-muted-foreground/60">Describe what needs to change:</p>
          <textarea
            className="w-full rounded-md border border-border bg-background p-2 text-xs resize-none custom-scrollbar"
            rows={3}
            value={feedback}
            onChange={(e) => setFeedback(e.target.value)}
            placeholder="Your feedback for the plan..."
          />
          <div className="flex gap-2 justify-end">
            <Button variant="ghost" size="sm" onClick={() => setShowFeedback(false)}>Cancel</Button>
            <Button size="sm" onClick={() => resolve('request_changes', feedback)}>Send Feedback</Button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="rounded-md border border-info/30 bg-info/5 px-3 py-2">
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <FileText className="h-3.5 w-3.5 shrink-0 text-info" />
        <span>Plan Review</span>
      </div>
      <div className="mt-1.5 space-y-1.5">
        {error && (
          <div className="rounded-md border border-destructive/30 bg-destructive/10 p-2">
            <p className="text-xs text-destructive">{error}</p>
          </div>
        )}
        {item.message.content && (
          <div className="max-h-48 overflow-y-auto custom-scrollbar rounded-md border border-border bg-background p-2">
            <pre className="text-xs whitespace-pre-wrap font-mono">{item.message.content}</pre>
          </div>
        )}
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <AlertTriangle className="h-3 w-3 shrink-0 text-info" />
          <span>The Conductor has published a plan for your approval.</span>
          {planPath && (
            <Button variant="ghost" size="sm" onClick={openInViewer} className="text-xs h-6 px-1.5">
              <ExternalLink className="size-3 mr-1" /> Open in viewer
            </Button>
          )}
        </div>
        <div className="flex gap-2 pt-0.5">
          <Button variant="default" size="sm" onClick={() => resolve('approve')}>
            <Check className="size-3.5 mr-1" /> Approve
          </Button>
          <Button variant="outline" size="sm" onClick={() => setShowFeedback(true)}>
            <MessageSquare className="size-3.5 mr-1" /> Request Changes
          </Button>
          <Button variant="ghost" size="sm" onClick={() => resolve('abandon')}>
            <X className="size-3.5 mr-1" /> Abandon
          </Button>
        </div>
      </div>
    </div>
  )
}
