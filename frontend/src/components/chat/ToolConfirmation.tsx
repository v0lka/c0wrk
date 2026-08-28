import { useRef, useState, useCallback } from 'react'
import { AlertTriangle, Check, X, Loader2, Info } from 'lucide-react'
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

// Widest key portion ("  " + `"key": `) still worth aligning continuations to;
// beyond this the hanging indent could exceed the card width, so wrapping
// falls back to the line's own indentation instead.
const MAX_HANG_COLUMN = 32

/**
 * One pretty-printed argument line, soft-wrapped with a hanging indent.
 *
 * Long values (e.g. bash_exec/posh_exec command strings) wrap under the start
 * of their value instead of being clipped at the card edge with a horizontal
 * scrollbar: continuation lines are indented (`padding-left` + negative
 * `text-indent` in `ch`, exact for the monospace font) to the column where the
 * value begins, so the JSON structure stays readable.
 */
function ArgLine({ line }: { line: string }) {
  const indent = /^[ ]*/.exec(line)?.[0].length ?? 0
  const sep = line.indexOf('": ')
  const hang =
    sep !== -1 && sep + 3 - indent <= MAX_HANG_COLUMN ? sep + 3 : indent
  return (
    <span
      className="block whitespace-pre-wrap"
      style={{
        paddingLeft: `${hang}ch`,
        textIndent: `-${hang}ch`,
        overflowWrap: 'anywhere',
      }}
    >
      {line.length > 0 ? line : '\u00A0'}
    </span>
  )
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
  // Human-readable reason for WHY this call needs confirmation (computed by the
  // backend at the security gate: symlink traversal, judge flag, or the tool's
  // default mutating-action policy). Distinct from `judgeReasoning`, which is
  // the on-demand "Ask Agent" verdict the user requests from the card.
  const reason = typeof metadata?.reasoning === 'string' ? metadata.reasoning.trim() : ''
  // True when the strict automatic judge (Smart Approve) already evaluated this
  // call; the advisory "Ask Agent" button is hidden so the call is not judged
  // a second time.
  const disableJudge = metadata?.disable_judge === true
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
    setActivityStatus(sessionId, isConfirm ? `Running tool: ${tool}...` : null)
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
      <div className="rounded-md border border-success/30 bg-success/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <Check className="h-3.5 w-3.5 shrink-0 text-success" />
          <span>Tool Confirmation</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Confirmed: {tool}</p>
      </div>
    )
  }
  if (resolved === 'denied') {
    return (
      <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <X className="h-3.5 w-3.5 shrink-0 text-destructive" />
          <span>Tool Confirmation</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Denied: {tool}</p>
      </div>
    )
  }
  // Resolved without a recorded decision — stale prompt reconciled on reload
  // (the executor waiting for the response is gone). Show a neutral settled
  // card instead of the active Allow/Deny affordance.
  if (metadata?.resolved === true) {
    return (
      <div className="rounded-md border border-border bg-background/50 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span>Tool Confirmation</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Dismissed: {tool}</p>
      </div>
    )
  }

  const formatJson = (data: string | undefined) => {
    if (!data) return null
    try { return JSON.stringify(JSON.parse(data), null, 2) } catch { return data }
  }

  return (
    <div className="rounded-md border border-warning/30 bg-warning/5 px-3 py-2">
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-warning" />
        <span>Tool Confirmation</span>
      </div>
      {reason && (
        <div className="mt-1.5 p-2 bg-info/10 border border-info/30 rounded-md">
          <div className="flex items-start gap-1.5">
            <Info className="h-3.5 w-3.5 text-info shrink-0 mt-0.5" />
            <div className="min-w-0">
              <p className="text-xs font-medium text-info mb-0.5">Why approval is needed</p>
              <p className="text-xs text-foreground whitespace-pre-wrap break-words">{reason}</p>
            </div>
          </div>
        </div>
      )}
      {judgeReasoning && (
        <div className="mt-1.5 p-2 bg-warning/10 border border-warning/30 rounded-md">
          <div className="flex items-start gap-1.5">
            <AlertTriangle className="h-3.5 w-3.5 text-warning shrink-0 mt-0.5" />
            <div className="min-w-0">
              <p className="text-xs font-medium text-warning mb-0.5">Agent Verdict</p>
              <p className="text-xs text-foreground">{judgeReasoning}</p>
            </div>
          </div>
        </div>
      )}
      {judgeError && (
        <div className="mt-1.5 p-2 bg-destructive/10 border border-destructive/30 rounded-md">
          <p className="text-xs text-destructive">{judgeError}</p>
        </div>
      )}
      <div className="mt-1.5 space-y-1.5">
        <p className="text-xs text-muted-foreground"><span className="text-muted-foreground/60">Tool:</span> <span className="font-medium text-foreground">{tool || 'Unknown'}</span></p>
        {args && (
          <div className="min-w-0">
            <p className="text-xs text-muted-foreground/60 mb-1">Input:</p>
            <pre className="w-full min-w-0 max-w-full max-h-64 overflow-y-auto custom-scrollbar rounded border border-border bg-background/50 p-2 font-mono text-xs">
              <code>
                {(formatJson(args) ?? '').split('\n').map((line, index) => (
                  <ArgLine key={index} line={line} />
                ))}
              </code>
            </pre>
          </div>
        )}
      </div>
      <div className="pt-3 flex flex-wrap gap-2">
        <Button size="sm" onClick={handleResponseAllowOnce} className="text-xs">Allow Once</Button>
        {!disableJudge && (
          <Button size="sm" variant="secondary" onClick={handleAskAgent} disabled={judgeLoading || judgeReasoning !== null} className="text-xs">
            {judgeLoading ? <><Loader2 className="h-3 w-3 animate-spin mr-1" />Evaluating...</> : judgeReasoning !== null ? 'Evaluated' : 'Ask Agent'}
          </Button>
        )}
        <Button size="sm" variant="outline" onClick={handleResponseDeny} className="text-xs">Deny</Button>
      </div>
    </div>
  )
}
