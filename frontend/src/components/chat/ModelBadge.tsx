import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { ModelBadge as StatusModelBadge } from '@/components/ModelBadge'
import { subagentModelDiffers } from '@/lib/subagentModelBadge'

interface ModelBadgeProps {
  /** The delegate's forced model string — rendered by the shared status-bar
   *  pill: the bare model name as the visible text, the full `provider/model`
   *  composite as the tooltip. */
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
 * The delegate's forced-model pin, rendered by the status bar's shared model
 * pill. Visible only when the pinned model differs from the conductor's
 * session model (bare-name comparison — see {@link subagentModelDiffers}): an
 * equal pin carries no information, so the badge renders nothing.
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

  // The shared pill strips the bare name for the text and keeps the full
  // composite for the tooltip; a pin that strips to nothing renders no pill.
  return <StatusModelBadge model={model} />
}
