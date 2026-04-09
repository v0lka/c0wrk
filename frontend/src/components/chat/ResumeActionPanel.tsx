import { useState } from 'react'
import { AlertTriangle, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useChatStore } from '@/stores/chatStore'
import { ResumeTask } from '../../../wailsjs/go/desktop/App'

interface ResumeActionPanelProps {
  sessionId: string
  content: string
  metadata?: Record<string, unknown>
}

export function ResumeActionPanel({ sessionId, content, metadata }: ResumeActionPanelProps) {
  const [resumed, setResumed] = useState(false)

  const resolved = metadata?.resolved === true

  // If resolved (either from local state or metadata), render nothing
  if (resolved || resumed) {
    return null
  }

  const handleResume = () => {
    setResumed(true)
    useChatStore.getState().resolveResumeMessage(sessionId)
    ResumeTask(sessionId).catch(() => {
      // If resume fails, the error event will handle it
    })
  }

  return (
    <div className="border-2 border-red-500/50 rounded-lg p-4 bg-red-500/5 max-w-full overflow-hidden">
      {/* Header */}
      <div className="flex items-center gap-2 mb-3">
        <AlertTriangle className="h-4 w-4 text-red-500" />
        <span className="text-sm font-medium">Task Failed</span>
      </div>

      {/* Failure summary */}
      <p className="text-sm text-muted-foreground mb-4">{content}</p>

      {/* Resume button */}
      <Button
        size="sm"
        variant="default"
        onClick={handleResume}
        className="text-xs"
        aria-label="Resume task execution"
      >
        <RefreshCw className="h-3.5 w-3.5 mr-1.5" />
        Resume
      </Button>
    </div>
  )
}
