import { bareModel } from './modelId'

/**
 * Visibility rule for the delegate model badge (SubAgentBlock / PlanStepBlock
 * header pills, rendered by ModelBadge).
 *
 * The badge surfaces a delegate that runs on a model DIFFERENT from the
 * conductor's session model; when the pinned model is the same model the pin
 * carries no information, so the badge is hidden. Both sides are compared as
 * bare names (composite provider prefixes stripped via {@link bareModel}, the
 * first-" / " split shared with the backend), so a pin of `openrouter/qwen3-27b`
 * matches a session running bare `qwen3-27b` and stays hidden.
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
  return bareModel(model) !== bareModel(sessionModel)
}
