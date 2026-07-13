import { useCallback } from 'react'
import { AlertOctagon, Check, X, Infinity as InfinityIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { emit } from '@/api/runtime'
import { useChatStore } from '@/stores/chatStore'
import type { DisplayItem } from '@/types/messages'
import { getStepLimitResolution, stepLimitResolved } from '@/types/messages'
import type { StepLimitDecision } from '@/types/messages'

type StepLimitItem = Extract<DisplayItem, { kind: 'step_limit' }>

export function StepLimitPrompt({ item }: { item: StepLimitItem }) {
  const { sessionId, metadata } = item.message
  const updateMessage = useChatStore(s => s.updateMessage)
  const setActivityStatus = useChatStore(s => s.setActivityStatus)

  const requestId = typeof metadata?.request_id === 'string' ? metadata.request_id : undefined
  const currentStep = typeof metadata?.current_step === 'number' ? metadata.current_step : 0
  const maxSteps = typeof metadata?.max_steps === 'number' ? metadata.max_steps : 0
  const reason = typeof metadata?.reason === 'string' ? metadata.reason : ''
  const resolved = getStepLimitResolution(metadata)

  const handleResponse = useCallback((response: StepLimitDecision) => {
    emit('step_limit_response', { request_id: requestId, response })
    updateMessage(sessionId, item.message.id, { metadata: stepLimitResolved(response) })
    setActivityStatus(sessionId, response === 'deny' ? null : 'Continuing execution...')
  }, [requestId, sessionId, updateMessage, setActivityStatus, item.message.id])

  const handleAllowOnce = useCallback(() => handleResponse('allow_once'), [handleResponse])
  const handleAllowMore = useCallback(() => handleResponse('allow_more'), [handleResponse])
  const handleAllowAlways = useCallback(() => handleResponse('allow_always'), [handleResponse])
  const handleDeny = useCallback(() => handleResponse('deny'), [handleResponse])

  if (resolved === 'allow_once') {
    return (
      <div className="rounded-md border border-success/30 bg-success/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <Check className="h-3.5 w-3.5 shrink-0 text-success" />
          <span>Step Limit</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Allowed once — continuing execution</p>
      </div>
    )
  }
  if (resolved === 'allow_more') {
    return (
      <div className="rounded-md border border-success/30 bg-success/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <Check className="h-3.5 w-3.5 shrink-0 text-success" />
          <span>Step Limit</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Allowed more — continuing execution</p>
      </div>
    )
  }
  if (resolved === 'allow_always') {
    return (
      <div className="rounded-md border border-info/30 bg-info/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <InfinityIcon className="h-3.5 w-3.5 shrink-0 text-info" />
          <span>Step Limit</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Allowed always — unlimited execution</p>
      </div>
    )
  }
  if (resolved === 'deny') {
    return (
      <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <X className="h-3.5 w-3.5 shrink-0 text-destructive" />
          <span>Step Limit</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Denied — execution stopped</p>
      </div>
    )
  }
  // Resolved without a recorded decision — stale prompt reconciled on reload.
  if (metadata?.resolved === true) {
    return (
      <div className="rounded-md border border-border bg-background/50 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <AlertOctagon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span>Step Limit</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">Dismissed</p>
      </div>
    )
  }

  const title = reason ? 'Circuit Breaker Triggered' : 'Tool Call Limit Reached'
  const description = reason
    ? reason
    : `Agent has reached its tool call limit (step ${currentStep} of ${maxSteps}). Allow it to continue?`

  return (
    <div className="rounded-md border border-warning/30 bg-warning/5 px-3 py-2">
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <AlertOctagon className="h-3.5 w-3.5 shrink-0 text-warning" />
        <span>Step Limit</span>
      </div>
      <div className="mt-1.5">
        <p className="text-xs text-foreground">{title}</p>
        <p className="text-xs text-muted-foreground mt-0.5">{description}</p>
        {reason && <p className="text-xs text-muted-foreground mt-0.5">Allow the agent to continue?</p>}
      </div>
      <div className="mt-2 flex flex-wrap gap-2">
        <Button size="sm" onClick={handleAllowOnce} className="text-xs">Allow Once</Button>
        {!reason && maxSteps > 0 && (
          <Button size="sm" onClick={handleAllowMore} className="text-xs">Allow More</Button>
        )}
        <Button size="sm" variant="secondary" onClick={handleAllowAlways} className="text-xs">Allow Always</Button>
        <Button size="sm" variant="outline" onClick={handleDeny} className="text-xs">Deny</Button>
      </div>
    </div>
  )
}
