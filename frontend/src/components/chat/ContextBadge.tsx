import { useChatStore } from '@/stores/chatStore'
import { cn } from '@/lib/utils'
import { formatTokenCount } from '@/lib/formatters'

export function ContextBadge() {
  const contextFill = useChatStore(s => s.contextFill)

  if (!contextFill) return null

  const hasTokens = contextFill.sessionInputTokens > 0 || contextFill.sessionOutputTokens > 0

  const getStatusColor = (status: string): string => {
    switch (status) {
      case 'ok':
        return 'text-muted-foreground'
      case 'compact':
        return 'text-foreground'
      case 'warning':
        return 'text-amber-500'
      case 'emergency':
      case 'reject':
        return 'text-red-500'
      default:
        return 'text-muted-foreground'
    }
  }

  const tooltipParts = [
    `Fill: ${Math.round(contextFill.fillPercent)}%`,
    `${contextFill.usedTokens.toLocaleString()} / ${contextFill.maxTokens.toLocaleString()} context tokens`,
  ]
  if (hasTokens) {
    tooltipParts.push(
      `Session input: ${contextFill.sessionInputTokens.toLocaleString()} tokens`,
      `Session output: ${contextFill.sessionOutputTokens.toLocaleString()} tokens`,
    )
  }

  return (
    <span
      className={cn(
        'text-xs whitespace-nowrap flex items-center gap-0',
        getStatusColor(contextFill.status)
      )}
      title={tooltipParts.join('\n')}
    >
      Context {Math.round(contextFill.fillPercent)}%
      {hasTokens && (
        <>
          <span className="mx-1 opacity-40">·</span>
          <span className="text-muted-foreground/70">
            {formatTokenCount(contextFill.sessionInputTokens)}
            {' / '}
            {formatTokenCount(contextFill.sessionOutputTokens)}
          </span>
        </>
      )}
    </span>
  )
}
