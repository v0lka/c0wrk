import { AlertTriangle, RefreshCw, X, Check } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useChatStore } from '@/stores/chatStore'
import { resumeTask, cancelUnfinishedTask } from '@/api/chat'
import type { DisplayItem } from '@/types/messages'
import { getResumeResolution, resumeResolved } from '@/types/messages'

type ResumeItem = Extract<DisplayItem, { kind: 'resume_action' }>

export function ResumeActionPanel({ item }: { item: ResumeItem }) {
  const { sessionId, content, metadata } = item.message
  const updateMessage = useChatStore(s => s.updateMessage)

  const decision = getResumeResolution(metadata)

  if (decision === 'resumed') {
    return (
      <div className="rounded-md border border-success/30 bg-success/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <Check className="h-3.5 w-3.5 shrink-0 text-success" />
          <span>Task Failed</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Task resumed</p>
      </div>
    )
  }
  if (decision === 'cancelled') {
    return (
      <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <X className="h-3.5 w-3.5 shrink-0 text-destructive" />
          <span>Task Failed</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Task cancelled</p>
      </div>
    )
  }
  // Resolved without a recorded decision (e.g. stale action reconciled on
  // reload) — render nothing at the stream position.
  if (metadata?.resolved === true) return null

  const handleResume = () => {
    updateMessage(sessionId, item.message.id, { metadata: resumeResolved('resumed') })
    resumeTask(sessionId).catch(() => { /* error event will handle */ })
  }

  const handleCancel = () => {
    updateMessage(sessionId, item.message.id, { metadata: resumeResolved('cancelled') })
    cancelUnfinishedTask(sessionId).catch(() => { /* best-effort: UI is already dismissed */ })
  }

  return (
    <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2">
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-destructive" />
        <span>Task Failed</span>
      </div>
      <p className="mt-1.5 text-xs text-muted-foreground">{content}</p>
      <div className="mt-2 flex flex-wrap gap-2">
        <Button size="sm" onClick={handleResume} className="text-xs">
          <RefreshCw className="h-3 w-3 mr-1.5" />Resume
        </Button>
        <Button size="sm" variant="outline" onClick={handleCancel} className="text-xs">
          <X className="h-3 w-3 mr-1.5" />Cancel
        </Button>
      </div>
    </div>
  )
}
