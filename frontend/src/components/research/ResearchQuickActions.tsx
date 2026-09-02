import {
  Plus,
  FlaskConical,
  ClipboardCheck,
  Split,
  FileCheck2,
  type LucideIcon,
} from 'lucide-react'
import { useMessageSender } from '@/hooks/useMessageSender'
import { QUICK_ACTIONS } from './researchActions'

/** Icon per quick-action key (stable; the key is part of the constant list). */
const ACTION_ICONS: Record<string, LucideIcon> = {
  hypothesis: Plus,
  experiment: FlaskConical,
  'record-result': ClipboardCheck,
  decision: Split,
  synthesize: FileCheck2,
}

/**
 * Quick-actions row — one button per research lifecycle gesture, each
 * dispatching its matching `research-*` skill (with a constant prompt) through
 * the message sender. This is the "start doing" surface: no raw typing.
 */
export function ResearchQuickActions() {
  const { send } = useMessageSender()

  return (
    <div
      data-testid="research-quick-actions"
      className="flex shrink-0 flex-wrap items-center gap-1"
    >
      {QUICK_ACTIONS.map((action) => {
        const Icon = ACTION_ICONS[action.key] ?? Plus
        return (
          <button
            key={action.key}
            type="button"
            data-testid="research-quick-action"
            data-skill={action.skill}
            onClick={() => void send(action.prompt, [action.skill])}
            title={`${action.label} (${action.skill})`}
            className="inline-flex items-center gap-1 rounded-md border border-border bg-background px-2 py-1 text-[11px] text-foreground transition-colors hover:bg-muted"
          >
            <Icon className="size-3.5 text-muted-foreground" />
            {action.label}
          </button>
        )
      })}
    </div>
  )
}
