import { useChatStore } from '@/stores/chatStore'
import { formatTokenCount } from '@/lib/formatters'
import { BrainCircuit } from 'lucide-react'

export function ContextBadge() {
  const sessionInputTokens = useChatStore(s => s.sessionInputTokens)
  const sessionOutputTokens = useChatStore(s => s.sessionOutputTokens)
  const sessionModel = useChatStore(s => s.sessionModel)
  const sessionTier = useChatStore(s => s.sessionTier)

  const hasTokens = sessionInputTokens > 0 || sessionOutputTokens > 0
  if (!hasTokens) return null

  const tooltipParts = [
    `Model: ${sessionModel || 'unknown'}`,
    `Tier: ${sessionTier || 'unknown'}`,
    `Session input: ${sessionInputTokens.toLocaleString()} tokens`,
    `Session output: ${sessionOutputTokens.toLocaleString()} tokens`,
  ]

  // Build display: <icon> <model> (tier): in <input> / out <output>
  const hasModel = sessionModel && sessionModel !== ''
  const hasTier = sessionTier && sessionTier !== ''

  return (
    <span
      className="text-xs whitespace-nowrap text-muted-foreground flex items-center gap-1"
      title={tooltipParts.join('\n')}
    >
      <BrainCircuit className="h-3 w-3" />
      {hasModel && (
        <span className="font-medium">{sessionModel}</span>
      )}
      {hasModel && hasTier && (
        <span className="text-muted-foreground/70">({sessionTier})</span>
      )}
      <span>:</span>
      <span>{formatTokenCount(sessionInputTokens)} in</span>
      <span>/</span>
      <span>{formatTokenCount(sessionOutputTokens)} out</span>
    </span>
  )
}
