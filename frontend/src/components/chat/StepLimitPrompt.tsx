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
    setActivityStatus(response === 'deny' ? null : 'Continuing execution...')
  }, [requestId, sessionId, updateMessage, setActivityStatus, item.message.id])

  const handleAllowOnce = useCallback(() => handleResponse('allow_once'), [handleResponse])
  const handleAllowAlways = useCallback(() => handleResponse('allow_always'), [handleResponse])
  const handleDeny = useCallback(() => handleResponse('deny'), [handleResponse])

  if (resolved === 'allow_once') {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <Check className="h-3.5 w-3.5 text-success" /><span className="text-sm">Allowed once — continuing execution</span>
      </div>
    )
  }
  if (resolved === 'allow_always') {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <InfinityIcon className="h-3.5 w-3.5 text-info" /><span className="text-sm">Allowed always — unlimited execution</span>
      </div>
    )
  }
  if (resolved === 'deny') {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <X className="h-3.5 w-3.5 text-destructive" /><span className="text-sm">Denied — execution stopped</span>
      </div>
    )
  }

  const title = reason ? 'Circuit Breaker Triggered' : 'Tool Call Limit Reached'
  const description = reason
    ? reason
    : `Agent has reached its tool call limit (step ${currentStep} of ${maxSteps}). Allow it to continue?`

  return (
    <div className="border-2 border-warning/50 rounded-lg p-4 bg-warning/5 max-w-full overflow-hidden">
      <div className="flex items-center gap-2 mb-3">
        <AlertOctagon className="h-4 w-4 text-warning" />
        <span className="text-sm font-medium">{title}</span>
      </div>
      <div className="mb-4">
        <p className="text-sm">{description}</p>
        {reason && <p className="text-sm text-muted-foreground mt-1">Allow the agent to continue?</p>}
      </div>
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={handleAllowOnce} className="text-xs">Allow Once</Button>
        <Button size="sm" variant="secondary" onClick={handleAllowAlways} className="text-xs">Allow Always</Button>
        <Button size="sm" variant="outline" onClick={handleDeny} className="text-xs">Deny</Button>
      </div>
    </div>
  )
}
