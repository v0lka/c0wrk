import { Badge } from '@/components/ui/badge'
import { bareModel } from '@/lib/modelId'
import { cn } from '@/lib/utils'

interface ModelBadgeProps {
  /** The model selector to render. The bare name (first "/" stripped — the
   *  split shared with the backend) is the visible text; the full selector
   *  verbatim is the tooltip, where the provider prefix is exactly what
   *  matters. */
  model: string
  /** Extra classes for the hosting context (e.g. the status bar's
   *  `max-w-full` clamp). */
  className?: string
}

/**
 * The model pill shared by the status bar's context block and the delegate
 * headers (SubAgentBlock / PlanStepBlock reach it through the chat-local
 * wrapper that owns the differs-from-session visibility rule).
 *
 * The visible text is the bare model name — the status-bar convention
 * (`session_tokens.model` arrives bare, so a bare input renders verbatim);
 * the `title` tooltip carries the full value, so a composite
 * `provider/model` pin surfaces its provider on hover without spending
 * horizontal space.
 */
export function ModelBadge({ model, className }: ModelBadgeProps) {
  // A value that strips to nothing (e.g. "provider/") would render an empty
  // pill — the launch funnel never emits one, but the guard keeps the pill
  // total.
  const displayName = bareModel(model)
  if (!displayName) return null

  return (
    <Badge
      variant="outline"
      className={cn('h-5 min-w-0 shrink px-1.5 text-xs truncate', className)}
      title={model}
    >
      {displayName}
    </Badge>
  )
}
