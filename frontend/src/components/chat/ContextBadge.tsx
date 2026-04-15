import { useChatStore } from '@/stores/chatStore'
import { formatTokenCount } from '@/lib/formatters'
import { BrainCircuit } from 'lucide-react'

export function ContextBadge() {
  const sessionInputTokens = useChatStore(s => s.sessionInputTokens)
  const sessionOutputTokens = useChatStore(s => s.sessionOutputTokens)
  const sessionModel = useChatStore(s => s.sessionModel)
  const sessionFamily = useChatStore(s => s.sessionFamily)

  const hasTokens = sessionInputTokens > 0 || sessionOutputTokens > 0
  if (!hasTokens) return null

  const tooltipParts = [
    `Model: ${sessionModel || 'unknown'}`,
    `Family: ${sessionFamily || 'unknown'}`,
    `Session input: ${sessionInputTokens.toLocaleString()} tokens`,
    `Session output: ${sessionOutputTokens.toLocaleString()} tokens`,
  ]

  // Build display: <icon> <model> (family): in <input> / out <output>
  const hasModel = sessionModel && sessionModel !== ''
  const hasFamily = sessionFamily && sessionFamily !== ''

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
      <span>:</span>
      <span>{formatTokenCount(sessionInputTokens)} in</span>
      <span>/</span>
      <span>{formatTokenCount(sessionOutputTokens)} out</span>
    </span>
  )
}
