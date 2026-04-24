import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { formatTokenCount } from '@/lib/formatters'
import { BrainCircuit } from 'lucide-react'

export function ContextBadge() {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const tokenInfo = useChatStore(s =>
    activeSessionId ? s.sessionTokens[activeSessionId] : undefined
  )

  const model = tokenInfo?.model ?? ''
  const family = tokenInfo?.family ?? ''
  const inputTokens = tokenInfo?.total_input_tokens ?? 0
  const outputTokens = tokenInfo?.total_output_tokens ?? 0
  const hasTokens = inputTokens > 0 || outputTokens > 0
  const hasModel = model !== ''
  const hasFamily = family !== ''

  if (!hasModel && !hasTokens && !activeSessionId) {
    return (
      <span className="text-xs whitespace-nowrap text-muted-foreground/50 flex items-center gap-1">
        <BrainCircuit className="h-3 w-3" />
        <span>No activity yet</span>
      </span>
    )
  }

  const tooltipParts = [
    `Model: ${model || 'unknown'}`,
    ...(hasFamily ? [`Family: ${family}`] : []),
    `Session input: ${inputTokens.toLocaleString()} tokens`,
    `Session output: ${outputTokens.toLocaleString()} tokens`,
  ]

  return (
    <span
      className="text-xs whitespace-nowrap text-muted-foreground flex items-center gap-1"
      title={tooltipParts.join('\n')}
    >
      <BrainCircuit className="h-3 w-3" />
      {hasModel && <span className="font-medium">{model}</span>}
      {hasModel && hasFamily && (
        <span className="text-muted-foreground/70">({family})</span>
      )}
      {hasTokens && (
        <>
          <span>:</span>
          <span>{formatTokenCount(inputTokens)} in</span>
          <span>/</span>
          <span>{formatTokenCount(outputTokens)} out</span>
        </>
      )}
    </span>
  )
}
