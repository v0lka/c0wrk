import { bareModel, isCompositeModelId } from './modelId'

/**
 * Visibility rule for the delegate model badge (SubAgentBlock / PlanStepBlock
 * header pills, rendered by ModelBadge).
 *
 * The badge surfaces a delegate that runs on a model DIFFERENT from the
 * conductor's session model; when the pinned model is the same model the pin
 * carries no information, so the badge is hidden. When BOTH sides carry a
 * provider prefix the full composite ids compare verbatim — a cross-provider
 * pin sharing the session's bare model name (`azure/gpt-4` on a session
 * running `openai/gpt-4`) IS a difference (different routing, cost, latency),
 * so the badge stays visible. When either side is bare the comparison falls
 * back to bare names (composite provider prefixes stripped via
 * {@link bareModel}, the first-"/" split shared with the backend), so a pin
 * of `openrouter/qwen3-27b` matches a session running bare `qwen3-27b` and
 * stays hidden — the legacy `session_tokens.model` format and
 * provider-echoed ids that already contain "/" both land here.
 *
 * An absent or empty session model counts as different: the conductor's model
 * is unknown (e.g. no session_tokens event yet), so the badge stays visible.
 * An absent or empty pinned model renders nothing (parents guard on it too).
 */
export function subagentModelDiffers(
  model: string | undefined | null,
  sessionModel: string | undefined | null,
): boolean {
  if (!model) return false
  if (!sessionModel) return true
  if (isCompositeModelId(model) && isCompositeModelId(sessionModel)) return model !== sessionModel
  return bareModel(model) !== bareModel(sessionModel)
}
