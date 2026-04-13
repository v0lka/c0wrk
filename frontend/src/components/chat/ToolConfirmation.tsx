import { useEffect, useState } from 'react'
import { AlertTriangle, Check, X, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useWails } from '@/hooks/useWails'
import { useChatStore } from '@/stores/chatStore'

export interface ToolConfirmationMetadata {
  tool?: string
  args?: string
  confirm_id?: string
  reasoning?: string
  tool_msg_id?: string
}

interface ToolConfirmationProps {
  sessionId: string
  metadata?: Record<string, unknown>
}

export function ToolConfirmation({ sessionId, metadata }: ToolConfirmationProps) {
  const { runtime } = useWails()
  const [resolved, setResolved] = useState<'confirmed' | 'denied' | null>(null)
  const [judgeReasoning, setJudgeReasoning] = useState<string | null>(null)
  const [judgeLoading, setJudgeLoading] = useState(false)
  const [judgeError, setJudgeError] = useState<string | null>(null)

  const tool = typeof metadata?.tool === 'string' ? metadata.tool : undefined
  const args = typeof metadata?.args === 'string' ? metadata.args : undefined
  const confirmId = typeof metadata?.confirm_id === 'string' ? metadata.confirm_id : undefined
  const toolMsgId = typeof metadata?.tool_msg_id === 'string' ? metadata.tool_msg_id : undefined

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

    // Atomically mark resolved in messages AND remove from pendingActions
    useChatStore.getState().resolveAction(sessionId, `tool-confirm-${confirmId}`)

    // Update activity status
    if (isConfirm) {
      useChatStore.getState().setActivityStatus(`Running tool: ${tool}...`)
    } else {
      useChatStore.getState().setActivityStatus(null)
    }
  }

  // Listen for judge response
  useEffect(() => {
    if (!runtime || !confirmId) return

    const cancel = runtime.EventsOn('tool_judge_response', (data: any) => {
      if (data?.confirm_id !== confirmId) return

      setJudgeLoading(false)
      if (data.error) {
        setJudgeError(data.error)
      }
      if (data.reasoning) {
        setJudgeReasoning(data.reasoning)
      }
    })

    return () => {
      if (cancel) cancel()
    }
  }, [runtime, confirmId])

  const handleAskAgent = () => {
    if (!runtime || !confirmId) return
    setJudgeLoading(true)
    setJudgeError(null)
    runtime.EventsEmit('tool_judge_request', {
      confirm_id: confirmId,
    })
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
      {judgeReasoning && (
        <div className="mb-4 p-3 bg-amber-500/10 border border-amber-500/30 rounded-md">
          <div className="flex items-start gap-2">
            <AlertTriangle className="h-4 w-4 text-amber-400 shrink-0 mt-0.5" />
            <div>
              <p className="text-xs font-medium text-amber-600 dark:text-amber-300 mb-1">Agent Verdict</p>
              <p className="text-sm text-amber-900 dark:text-amber-100">{judgeReasoning}</p>
            </div>
          </div>
        </div>
      )}

      {judgeError && (
        <div className="mb-4 p-3 bg-red-500/10 border border-red-500/30 rounded-md">
          <p className="text-xs text-red-600 dark:text-red-400">{judgeError}</p>
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
          variant="secondary"
          onClick={handleAskAgent}
          disabled={judgeLoading || judgeReasoning !== null}
          className="text-xs"
          aria-label="Ask the AI agent to evaluate this tool action"
        >
          {judgeLoading ? (
            <>
              <Loader2 className="h-3 w-3 animate-spin mr-1" />
              Evaluating...
            </>
          ) : judgeReasoning !== null ? (
            'Evaluated'
          ) : (
            'Ask agent'
          )}
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
