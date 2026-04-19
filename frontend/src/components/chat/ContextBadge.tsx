import { useChatStore } from '@/stores/chatStore'
import { formatTokenCount } from '@/lib/formatters'
import { BrainCircuit } from 'lucide-react'

export function ContextBadge() {
  const sessionInputTokens = useChatStore(s => s.sessionInputTokens)
  const sessionOutputTokens = useChatStore(s => s.sessionOutputTokens)
  const sessionModel = useChatStore(s => s.sessionModel)
  const sessionFamily = useChatStore(s => s.sessionFamily)

  const hasTokens = sessionInputTokens > 0 || sessionOutputTokens > 0
  const hasModel = sessionModel && sessionModel !== ''
  const hasFamily = sessionFamily && sessionFamily !== ''

  // Minimal state: no model and no tokens
  if (!hasTokens && !hasModel) {
    return (
      <span className="text-xs whitespace-nowrap text-muted-foreground/50 flex items-center gap-1">
        <BrainCircuit className="h-3 w-3" />
        <span>No activity yet</span>
      </span>
    )
  }

  const tooltipParts = [
    `Model: ${sessionModel || 'unknown'}`,
    `Family: ${sessionFamily || 'unknown'}`,
    `Session input: ${sessionInputTokens.toLocaleString()} tokens`,
    `Session output: ${sessionOutputTokens.toLocaleString()} tokens`,
  ]

  return (
    <span
      className="text-xs whitespace-nowrap text-muted-foreground flex items-center gap-1"
      title={tooltipParts.join('\n')}
    >
      <BrainCircuit className="h-3 w-3" />
      {hasModel && (
        <span className="font-medium">{sessionModel}</span>
      )}
      {hasModel && hasFamily && (
        <span className="text-muted-foreground/70">({sessionFamily})</span>
      )}
      {hasTokens && (
        <>
          <span>:</span>
          <span>{formatTokenCount(sessionInputTokens)} in</span>
          <span>/</span>
          <span>{formatTokenCount(sessionOutputTokens)} out</span>
        </>
      )}
    </span>
  )
}
