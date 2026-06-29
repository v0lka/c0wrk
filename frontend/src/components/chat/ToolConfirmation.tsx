import { useRef, useState, useCallback } from 'react'
import { AlertTriangle, Check, X, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { emit } from '@/api/runtime'
import { useChatStore } from '@/stores/chatStore'
import { useToolJudgeEvents } from '@/hooks/events/useToolJudgeEvents'
import type { DisplayItem } from '@/types/messages'
import { getToolConfirmResolution, toolConfirmResolved } from '@/types/messages'

type ToolConfirmItem = Extract<DisplayItem, { kind: 'tool_confirm' }>

interface ToolConfirmationProps {
  item: ToolConfirmItem
}

export function ToolConfirmation({ item }: ToolConfirmationProps) {
  const { sessionId, metadata } = item.message
  const resolved = getToolConfirmResolution(metadata)
  const [judgeReasoning, setJudgeReasoning] = useState<string | null>(null)
  const [judgeLoading, setJudgeLoading] = useState(false)
  const [judgeError, setJudgeError] = useState<string | null>(null)

  const updateMessage = useChatStore(s => s.updateMessage)
  const setActivityStatus = useChatStore(s => s.setActivityStatus)

  const tool = typeof metadata?.tool === 'string' ? metadata.tool : undefined
  const args = typeof metadata?.args === 'string' ? metadata.args : undefined
  const confirmId = typeof metadata?.confirm_id === 'string' ? metadata.confirm_id : undefined
  const toolMsgId = typeof metadata?.tool_msg_id === 'string' ? metadata.tool_msg_id : undefined
  const judgeTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useToolJudgeEvents(sessionId, confirmId, {
    onResponse: (reasoning, error) => {
      if (judgeTimeoutRef.current) { clearTimeout(judgeTimeoutRef.current); judgeTimeoutRef.current = null }
      setJudgeLoading(false)
      if (error) setJudgeError(error)
      if (reasoning) setJudgeReasoning(reasoning)
    },
  })

  const handleResponse = useCallback((decision: 'allow_once' | 'deny') => {
    const isConfirm = decision === 'allow_once'
    emit('tool_confirm_response', { confirm_id: confirmId, decision })
    if (toolMsgId) {
      updateMessage(sessionId, toolMsgId, { metadata: { awaiting_confirmation: false } })
    }
    updateMessage(sessionId, item.message.id, { metadata: toolConfirmResolved(isConfirm ? 'confirmed' : 'denied') })
    setActivityStatus(isConfirm ? `Running tool: ${tool}...` : null)
  }, [confirmId, toolMsgId, sessionId, updateMessage, setActivityStatus, item.message.id, tool])

  const handleAskAgent = () => {
    if (!confirmId) return
    setJudgeLoading(true)
    setJudgeError(null)
    if (judgeTimeoutRef.current) clearTimeout(judgeTimeoutRef.current)
    judgeTimeoutRef.current = setTimeout(() => {
      setJudgeLoading(false)
      setJudgeError('Judge evaluation timed out')
      judgeTimeoutRef.current = null
    }, 30_000)
    emit('tool_judge_request', { confirm_id: confirmId })
  }

  const handleResponseAllowOnce = useCallback(() => handleResponse('allow_once'), [handleResponse])
  const handleResponseDeny = useCallback(() => handleResponse('deny'), [handleResponse])

  if (resolved === 'confirmed') {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <Check className="h-3.5 w-3.5 text-success" /><span className="text-sm">Confirmed: {tool}</span>
      </div>
    )
  }
  if (resolved === 'denied') {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <X className="h-3.5 w-3.5 text-destructive" /><span className="text-sm">Denied: {tool}</span>
      </div>
    )
  }

  const formatJson = (data: string | undefined) => {
    if (!data) return null
    try { return JSON.stringify(JSON.parse(data), null, 2) } catch { return data }
  }

  return (
    <div className="border-2 border-warning/50 rounded-lg p-4 bg-warning/5 max-w-full overflow-hidden">
      <div className="flex items-center gap-2 mb-3">
        <AlertTriangle className="h-4 w-4 text-warning" />
        <span className="text-sm font-medium">Tool Confirmation Required</span>
      </div>
      {judgeReasoning && (
        <div className="mb-4 p-3 bg-warning/10 border border-warning/30 rounded-md">
          <div className="flex items-start gap-2">
            <AlertTriangle className="h-4 w-4 text-warning shrink-0 mt-0.5" />
            <div>
              <p className="text-xs font-medium text-warning mb-1">Agent Verdict</p>
              <p className="text-sm text-foreground">{judgeReasoning}</p>
            </div>
          </div>
        </div>
      )}
      {judgeError && (
        <div className="mb-4 p-3 bg-destructive/10 border border-destructive/30 rounded-md">
          <p className="text-xs text-destructive">{judgeError}</p>
        </div>
      )}
      <div className="mb-4 space-y-2">
        <p className="text-sm"><span className="text-muted-foreground">Tool:</span> <span className="font-medium">{tool || 'Unknown'}</span></p>
        {args && (
          <div className="min-w-0 overflow-hidden">
            <p className="text-xs text-muted-foreground mb-1">Input:</p>
            <pre className="p-2 bg-background/50 rounded text-xs font-mono overflow-x-auto custom-scrollbar border border-border max-w-full min-w-0">
              <code>{formatJson(args)}</code>
            </pre>
          </div>
        )}
      </div>
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={handleResponseAllowOnce} className="text-xs">Allow Once</Button>
        <Button size="sm" variant="secondary" onClick={handleAskAgent} disabled={judgeLoading || judgeReasoning !== null} className="text-xs">
          {judgeLoading ? <><Loader2 className="h-3 w-3 animate-spin mr-1" />Evaluating...</> : judgeReasoning !== null ? 'Evaluated' : 'Ask Agent'}
        </Button>
        <Button size="sm" variant="outline" onClick={handleResponseDeny} className="text-xs">Deny</Button>
      </div>
    </div>
  )
}
