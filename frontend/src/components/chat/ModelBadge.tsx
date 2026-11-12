import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { Badge } from '@/components/ui/badge'
import { subagentModelDiffers } from '@/lib/subagentModelBadge'

interface ModelBadgeProps {
  /** The delegate's forced model string — rendered verbatim as text and title. */
  model: string
  /**
   * The conductor's session model. When omitted the badge reads the active
   * session's TokenInfo from chatStore itself — the same source as the status
   * bar's model badge — so it stays reactive inside the parent's memoized
   * header without re-rendering the parent.
   */
  sessionModel?: string
}

/**
 * The delegate's forced-model pin, styled like the status bar's model badge.
 * Visible only when the pinned model differs from the conductor's session
 * model (bare-name comparison — see {@link subagentModelDiffers}): an equal
 * pin carries no information, so the badge renders nothing.
 */
export function ModelBadge({ model, sessionModel }: ModelBadgeProps) {
  // Plan groups are rebuilt per session switch, so the displayed steps always
  // belong to the active session. Both selectors return primitives (stable —
  // no allocation).
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const storeSessionModel = useChatStore(s =>
    activeSessionId ? s.sessionTokens[activeSessionId]?.model : undefined,
  )
  // An explicitly provided prop (including an empty string) is authoritative;
  // only an omitted prop falls back to the store.
  const conductorModel = sessionModel !== undefined ? sessionModel : storeSessionModel

  if (!subagentModelDiffers(model, conductorModel)) return null

  return (
    <Badge
      variant="outline"
      className="h-5 min-w-0 shrink px-1.5 text-xs truncate"
      title={model}
    >
      {model}
    </Badge>
  )
}
