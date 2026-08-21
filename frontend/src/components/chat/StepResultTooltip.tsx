import { useState, useCallback } from 'react'
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip'
import { TooltipMarkdown } from './TooltipMarkdown'
import { getStepOutput } from '@/api/blackboard'
import { useSessionStore } from '@/stores/sessionStore'
import type { BlackboardStepResult } from '@/types/models'

interface StepResultTooltipProps {
  stepId: string
  result: BlackboardStepResult
  children: React.ReactNode
}

function buildMarkdown(body: string, error?: string): string {
  return error ? `${body}\n\n**Error:** ${error}` : body
}

/**
 * Tooltip for a blackboard step result. Shows the summary immediately and
 * lazily fetches the step's full output on first hover, preferring it over the
 * summary once loaded. The full output is never part of the list payload — it
 * is requested on demand via GetStepOutput.
 */
export function StepResultTooltip({ stepId, result, children }: StepResultTooltipProps) {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const [output, setOutput] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const loadOutput = useCallback(async () => {
    if (output !== null || loading || !activeSessionId) return
    setLoading(true)
    try {
      setOutput(await getStepOutput(activeSessionId, stepId))
    } catch {
      setOutput('')
    } finally {
      setLoading(false)
    }
  }, [activeSessionId, stepId, output, loading])

  const body = output || result.summary
  const description = buildMarkdown(body, result.error)

  return (
    <Tooltip onOpenChange={(open) => { if (open) void loadOutput() }}>
      <TooltipTrigger asChild>{children}</TooltipTrigger>
      <TooltipContent
        align="start"
        sideOffset={8}
        collisionPadding={32}
        avoidCollisions={true}
        updatePositionStrategy="always"
        className="max-w-md w-auto max-h-[min(70vh,calc(var(--radix-tooltip-content-available-height)-32px))] overflow-y-auto custom-scrollbar p-3"
      >
        <TooltipMarkdown content={description} />
      </TooltipContent>
    </Tooltip>
  )
}
