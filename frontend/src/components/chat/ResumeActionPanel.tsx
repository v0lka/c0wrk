import { AlertTriangle, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useChatStore } from '@/stores/chatStore'
import { resumeTask } from '@/api/chat'
import type { DisplayItem } from '@/types/messages'

type ResumeItem = Extract<DisplayItem, { kind: 'resume_action' }>

export function ResumeActionPanel({ item }: { item: ResumeItem }) {
  const { sessionId, content, metadata } = item.message

  if (metadata?.resolved === true) return null

  const handleResume = () => {
    useChatStore.getState().updateMessage(sessionId, item.message.id, { metadata: { resolved: true } })
    resumeTask(sessionId).catch(() => { /* error event will handle */ })
  }

  return (
    <div className="border-2 border-destructive/50 rounded-lg p-4 bg-destructive/5 max-w-full overflow-hidden">
      <div className="flex items-center gap-2 mb-3">
        <AlertTriangle className="h-4 w-4 text-destructive" />
        <span className="text-sm font-medium">Task Failed</span>
      </div>
      <p className="text-sm text-muted-foreground mb-4">{content}</p>
      <Button size="sm" onClick={handleResume} className="text-xs">
        <RefreshCw className="h-3.5 w-3.5 mr-1.5" />Resume
      </Button>
    </div>
  )
}
