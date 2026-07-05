import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Check, X, MessageSquare, ExternalLink } from 'lucide-react'
import { emit } from '@/api/runtime'
import type { DisplayItem } from '@/types/messages'
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

  if (!requestId || !sessionId) return null

  const resolve = (decision: 'approve' | 'request_changes' | 'abandon', fb?: string) => {
    try {
      emit('plan_approval_response', {
        request_id: requestId,
        decision,
        feedback: fb ?? '',
      })
      useChatStore.getState().updateMessage(sessionId, item.message.id, {
        metadata: { ...item.message.metadata, resolved: true, decision },
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
      <div className="space-y-2">
        <p className="text-xs text-muted-foreground">Describe what needs to change:</p>
        <textarea
          className="w-full rounded-md border border-border bg-background p-2 text-sm resize-none"
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
    )
  }

  return (
    <div className="space-y-2">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/10 p-2">
          <p className="text-xs text-destructive">{error}</p>
        </div>
      )}
      {item.message.content && (
        <div className="max-h-48 overflow-y-auto custom-scrollbar rounded-md border border-border bg-background p-3">
          <pre className="text-xs whitespace-pre-wrap font-mono">{item.message.content}</pre>
        </div>
      )}
      <div className="flex items-center gap-2 text-sm">
        <span className="text-muted-foreground">The Conductor has published a plan for your approval.</span>
        {planPath && (
          <Button variant="ghost" size="sm" onClick={openInViewer}>
            <ExternalLink className="size-3.5 mr-1" /> Open in viewer
          </Button>
        )}
      </div>
      <div className="flex gap-2">
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
  )
}
