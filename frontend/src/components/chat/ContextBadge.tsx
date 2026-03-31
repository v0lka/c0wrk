import { useChatStore } from '@/stores/chatStore'
import { cn } from '@/lib/utils'

export function ContextBadge() {
  const contextFill = useChatStore(s => s.contextFill)

  if (!contextFill) return null

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

  return (
    <span
      className={cn(
        'text-xs whitespace-nowrap',
        getStatusColor(contextFill.status)
      )}
      title={`${contextFill.usedTokens.toLocaleString()} / ${contextFill.maxTokens.toLocaleString()} tokens`}
    >
      Context {Math.round(contextFill.fillPercent)}%
    </span>
  )
}
