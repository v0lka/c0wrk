import { Check } from 'lucide-react'
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import type { BranchBase } from '@/types/models'

interface BaseSelectorRowProps {
  base: BranchBase
  selected: boolean
  isCurrent: boolean
  onSelect: (ref: string) => void
}

const typeBadgeClass: Record<string, string> = {
  local: 'text-primary',
  remote: 'text-info',
  tag: 'text-warning',
  commit: 'text-muted-foreground',
}

const typeLabel: Record<string, string> = {
  local: 'branch',
  remote: 'remote',
  tag: 'tag',
  commit: 'commit',
}

/**
 * A single selectable row in BaseSelector. Shows the ref label, an
 * optional commit subject (Detail), a type badge, and a check mark
 * when selected. The current branch is annotated "(current)".
 */
export function BaseSelectorRow({
  base,
  selected,
  isCurrent,
  onSelect,
}: BaseSelectorRowProps) {
  return (
    <button
      type="button"
      onClick={() => onSelect(base.ref)}
      className={cn(
        'flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs transition-colors',
        'focus:outline-none focus:ring-1 focus:ring-ring',
        selected
          ? 'bg-primary/10 text-primary'
          : 'hover:bg-muted text-foreground',
      )}
    >
      <span
        className={cn(
          'shrink-0 text-[10px] uppercase tracking-wide',
          typeBadgeClass[base.type] ?? 'text-muted-foreground',
        )}
      >
        {typeLabel[base.type] ?? base.type}
      </span>
      <span className="flex min-w-0 flex-1 items-center gap-1">
        <span className="shrink-0 max-w-[40%] truncate font-mono">
          {base.label}
          {isCurrent && (
            <span className="ml-1 text-muted-foreground">(current)</span>
          )}
        </span>
        {base.detail && (
          <Tooltip delayDuration={300}>
            <TooltipTrigger asChild>
              <span className="min-w-0 flex-1 truncate text-muted-foreground">
                {base.detail}
              </span>
            </TooltipTrigger>
            <TooltipContent
              side="top"
              align="start"
              sideOffset={4}
              collisionPadding={16}
              className="max-w-sm"
            >
              {base.detail}
            </TooltipContent>
          </Tooltip>
        )}
      </span>
      {selected && <Check className="size-3.5 shrink-0" />}
    </button>
  )
}
