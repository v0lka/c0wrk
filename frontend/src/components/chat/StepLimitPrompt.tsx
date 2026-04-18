import { useState } from 'react'
import { AlertOctagon, Check, X, Infinity as InfinityIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useWails } from '@/hooks/useWails'
import { useChatStore } from '@/stores/chatStore'

export interface StepLimitPromptMetadata {
  request_id?: string
  current_step?: number
  max_steps?: number
}

interface StepLimitPromptProps {
  sessionId: string
  metadata?: Record<string, unknown>
}

export function StepLimitPrompt({ sessionId, metadata }: StepLimitPromptProps) {
  const { runtime } = useWails()
  const [resolved, setResolved] = useState<'allow_once' | 'allow_always' | 'deny' | null>(null)

  const stepMeta = metadata as StepLimitPromptMetadata | undefined
  const requestId = stepMeta?.request_id
  const currentStep = stepMeta?.current_step ?? 0
  const maxSteps = stepMeta?.max_steps ?? 0

  const handleResponse = (response: 'allow_once' | 'allow_always' | 'deny') => {
    if (!runtime) return

    setResolved(response)

    runtime.EventsEmit('step_limit_response', {
      request_id: requestId,
      response,
    })

    // Atomically mark resolved in messages AND remove from pendingActions
    useChatStore.getState().resolveAction(sessionId, `step-limit-${requestId}`)

    // Update activity status
    if (response === 'deny') {
      useChatStore.getState().setActivityStatus(null)
    } else {
      useChatStore.getState().setActivityStatus('Continuing execution...')
    }
  }

  // Resolved state — compact line replaces the panel
  if (resolved === 'allow_once') {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <Check className="h-3.5 w-3.5 text-emerald-500" />
        <span className="text-sm">Allowed once — continuing execution</span>
      </div>
    )
  }

  if (resolved === 'allow_always') {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <InfinityIcon className="h-3.5 w-3.5 text-blue-500" />
        <span className="text-sm">Allowed always — unlimited execution</span>
      </div>
    )
  }

  if (resolved === 'deny') {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <X className="h-3.5 w-3.5 text-red-500" />
        <span className="text-sm">Denied — execution stopped</span>
      </div>
    )
  }

  return (
    <div className="border-2 border-orange-500/50 rounded-lg p-4 bg-orange-500/5 max-w-full overflow-hidden">
      {/* Header */}
      <div className="flex items-center gap-2 mb-3">
        <AlertOctagon className="h-4 w-4 text-orange-500" />
        <span className="text-sm font-medium">Tool Call Limit Reached</span>
      </div>

      {/* Message */}
      <div className="mb-4">
        <p className="text-sm">
          Agent has reached its tool call limit (step {currentStep} of {maxSteps}).
          Allow it to continue?
        </p>
      </div>

      {/* Action buttons */}
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          variant="default"
          onClick={() => handleResponse('allow_once')}
          className="text-xs"
          aria-label="Allow the agent to continue once"
        >
          Allow Once
        </Button>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => handleResponse('allow_always')}
          className="text-xs"
          aria-label="Allow the agent to continue without limits"
        >
          Allow Always
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={() => handleResponse('deny')}
          className="text-xs"
          aria-label="Stop the agent execution"
        >
          Deny
        </Button>
      </div>
    </div>
  )
}
