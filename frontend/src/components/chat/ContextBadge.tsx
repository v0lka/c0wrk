import { useChatStore } from '@/stores/chatStore'
import { formatTokenCount } from '@/lib/formatters'

export function ContextBadge() {
  const sessionInputTokens = useChatStore(s => s.sessionInputTokens)
  const sessionOutputTokens = useChatStore(s => s.sessionOutputTokens)

  const hasTokens = sessionInputTokens > 0 || sessionOutputTokens > 0
  if (!hasTokens) return null

  const tooltipParts = [
    `Session input: ${sessionInputTokens.toLocaleString()} tokens`,
    `Session output: ${sessionOutputTokens.toLocaleString()} tokens`,
  ]

  return (
    <span
      className="text-xs whitespace-nowrap text-muted-foreground"
      title={tooltipParts.join('\n')}
    >
      {formatTokenCount(sessionInputTokens)}
      {' in / '}
      {formatTokenCount(sessionOutputTokens)}
      {' out'}
    </span>
  )
}
