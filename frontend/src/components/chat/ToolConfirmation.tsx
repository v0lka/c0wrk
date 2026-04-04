import { useState } from 'react'
import { AlertTriangle, Check, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useWails } from '@/hooks/useWails'
import { useChatStore } from '@/stores/chatStore'

interface ToolConfirmationProps {
  sessionId: string
  metadata?: unknown
}

export function ToolConfirmation({ sessionId, metadata }: ToolConfirmationProps) {
  const { runtime } = useWails()
  const [resolved, setResolved] = useState<'confirmed' | 'denied' | null>(null)

  const meta = metadata as Record<string, unknown> | undefined
  const tool = meta?.tool as string | undefined
  const args = meta?.args as string | undefined
  const confirmId = meta?.confirm_id as string | undefined
  const reasoning = meta?.reasoning as string | undefined
  const toolMsgId = meta?.tool_msg_id as string | undefined

  const handleResponse = (decision: 'allow_once' | 'deny') => {
    if (!runtime) return

    const isConfirm = decision === 'allow_once'
    setResolved(isConfirm ? 'confirmed' : 'denied')

    runtime.EventsEmit('tool_confirm_response', {
      confirm_id: confirmId,
      decision,
    })

    // Update the linked tool_call to remove awaiting_confirmation
    if (toolMsgId) {
      const msgs = useChatStore.getState().messages[sessionId] || []
      const toolMsg = msgs.find(m => m.id === toolMsgId)
      if (toolMsg) {
        useChatStore.getState().updateMessage(sessionId, toolMsgId, {
          metadata: { ...toolMsg.metadata, awaiting_confirmation: false },
        })
      }
    }

    // Mark this confirmation as resolved so groupMessages stops extracting it
    useChatStore.getState().updateMessage(sessionId, `tool-confirm-${confirmId}`, {
      metadata: { resolved: true },
    })

    // Update activity status
    if (isConfirm) {
      useChatStore.getState().setActivityStatus(`Running tool: ${tool}...`)
    } else {
      useChatStore.getState().setActivityStatus(null)
    }
  }

  // Resolved state — compact line replaces the panel
  if (resolved === 'confirmed') {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <Check className="h-3.5 w-3.5 text-emerald-500" />
        <span className="text-sm">Confirmed: {tool}</span>
      </div>
    )
  }

  if (resolved === 'denied') {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <X className="h-3.5 w-3.5 text-red-500" />
        <span className="text-sm">Denied: {tool}</span>
      </div>
    )
  }

  // Format JSON for display
  const formatJson = (data: string | undefined) => {
    if (!data) return null
    try {
      const parsed = JSON.parse(data)
      return JSON.stringify(parsed, null, 2)
    } catch {
      return data
    }
  }

  return (
    <div className="border-2 border-amber-500/50 rounded-lg p-4 bg-amber-500/5 max-w-full overflow-hidden">
      {/* Header */}
      <div className="flex items-center gap-2 mb-3">
        <AlertTriangle className="h-4 w-4 text-amber-500" />
        <span className="text-sm font-medium">Tool Confirmation Required</span>
      </div>

      {/* Judge reasoning — yellow/amber warning style */}
      {reasoning && (
        <div className="mb-4 p-3 bg-amber-500/10 border border-amber-500/30 rounded-md">
          <div className="flex items-start gap-2">
            <AlertTriangle className="h-4 w-4 text-amber-400 shrink-0 mt-0.5" />
            <div>
              <p className="text-xs font-medium text-amber-300 mb-1">LLM Judge Verdict</p>
              <p className="text-sm text-amber-100">{reasoning}</p>
            </div>
          </div>
        </div>
      )}

      {/* Tool info */}
      <div className="mb-4 space-y-2">
        <p className="text-sm">
          <span className="text-muted-foreground">Tool:</span>{' '}
          <span className="font-medium">{tool || 'Unknown'}</span>
        </p>
        {args && (
          <div className="min-w-0 overflow-hidden">
            <p className="text-xs text-muted-foreground mb-1">Input:</p>
            <pre className="p-2 bg-background/50 rounded text-xs font-mono overflow-x-auto border border-border max-w-full min-w-0">
              <code>{formatJson(args)}</code>
            </pre>
          </div>
        )}
      </div>

      {/* Action buttons */}
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          variant="default"
          onClick={() => handleResponse('allow_once')}
          className="text-xs"
          aria-label="Allow this tool action once"
        >
          Allow Once
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={() => handleResponse('deny')}
          className="text-xs"
          aria-label="Deny this tool action"
        >
          Deny
        </Button>
      </div>
    </div>
  )
}
