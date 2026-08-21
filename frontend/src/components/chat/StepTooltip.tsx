import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip'
import { TooltipMarkdown } from './TooltipMarkdown'

interface StepTooltipProps {
  children: React.ReactNode
  description: string
  enabled?: boolean
}

/**
 * Unified tooltip component for step descriptions.
 * Handles viewport-aware positioning with scrollable content.
 */
export function StepTooltip({ children, description, enabled = true }: StepTooltipProps) {
  if (!enabled || !description) {
    return <>{children}</>
  }

  return (
    <Tooltip>
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
