import { useState, useEffect } from 'react'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { formatTokenCount } from '@/lib/formatters'
import { BrainCircuit } from 'lucide-react'
import { GetConfig } from '../../../wailsjs/go/desktop/App'
import type { backend } from '../../../wailsjs/go/models'

/** Extract the model name for the active provider from config. */
function resolveConfiguredModel(llm: backend.ConfigLLMResponse): string {
  const provider = llm.active_provider
  switch (provider) {
    case 'anthropic': return llm.anthropic?.model ?? ''
    case 'gemini': return llm.gemini?.model ?? ''
    case 'lmstudio': return llm.lmstudio?.model ?? ''
    case 'openai_compatible': return llm.openai_compatible?.model ?? ''
    case 'chatgpt': return llm.chatgpt?.model ?? ''
    default: return ''
  }
}

export function ContextBadge() {
  const sessionInputTokens = useChatStore(s => s.sessionInputTokens)
  const sessionOutputTokens = useChatStore(s => s.sessionOutputTokens)
  const sessionModel = useChatStore(s => s.sessionModel)
  const sessionFamily = useChatStore(s => s.sessionFamily)
  const activeSessionId = useSessionStore(s => s.activeSessionId)

  const [configuredModel, setConfiguredModel] = useState('')

  useEffect(() => {
    GetConfig().then(cfg => {
      if (cfg?.llm) {
        setConfiguredModel(resolveConfiguredModel(cfg.llm))
      }
    }).catch(() => { /* config not available yet */ })
  }, [])

  // Session model (from token events) takes priority over configured model
  const displayModel = sessionModel || configuredModel
  const hasTokens = sessionInputTokens > 0 || sessionOutputTokens > 0
  const hasModel = displayModel !== ''
  const hasFamily = sessionFamily && sessionFamily !== ''
  const hasSession = !!activeSessionId

  // Show "No activity yet" only when there's no model and no session
  if (!hasModel && !hasTokens && !hasSession) {
    return (
      <span className="text-xs whitespace-nowrap text-muted-foreground/50 flex items-center gap-1">
        <BrainCircuit className="h-3 w-3" />
        <span>No activity yet</span>
      </span>
    )
  }

  const tooltipParts = [
    `Model: ${displayModel || 'unknown'}`,
    ...(hasFamily ? [`Family: ${sessionFamily}`] : []),
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
        <span className="font-medium">{displayModel}</span>
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
