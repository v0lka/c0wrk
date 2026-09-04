import { useMemo, useRef } from 'react'
import {
  Plus,
  FlaskConical,
  ClipboardCheck,
  Split,
  FileCheck2,
  ChevronDown,
  type LucideIcon,
} from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'
import { useMessageSender } from '@/hooks/useMessageSender'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { cn } from '@/lib/utils'
import {
  QUICK_ACTIONS,
  buildExperimentPrompt,
} from './researchActions'
import type { HypothesisNode } from '@/types/models'

/** Icon per quick-action key (stable; the key is part of the constant list). */
const ACTION_ICONS: Record<string, LucideIcon> = {
  hypothesis: Plus,
  experiment: FlaskConical,
  'record-result': ClipboardCheck,
  decision: Split,
  synthesize: FileCheck2,
}

const ACTION_BUTTON_CLASS =
  'inline-flex items-center gap-1 rounded-md border border-border bg-background px-2 py-1 text-[11px] text-foreground transition-colors hover:bg-muted'

/**
 * Quick-actions row — one button per research lifecycle gesture, each
 * dispatching its matching `research-*` skill (with a constant prompt) through
 * the message sender. This is the "start doing" surface: no raw typing.
 *
 * Shift modifier: holding Shift while clicking dispatches into a brand-new
 * session instead of the active one. "Run experiment" is a dropdown listing
 * the active-front hypotheses; picking one scopes the dispatched prompt to
 * that hypothesis. "Synthesize" flips to "Update report" once a report exists.
 */
export function ResearchQuickActions() {
  const { send } = useMessageSender()
  const project = useResearchStore(selectActiveProject)
  const hasReport = project?.has_report ?? false

  // The active-front hypotheses offered by the Run-experiment dropdown:
  // unresolved ids (stale graph vs metrics) are dropped.
  const experimentTargets = useMemo<HypothesisNode[]>(() => {
    const front = project?.metrics.active_front ?? []
    const nodes = project?.graph.nodes ?? []
    return front
      .map((id) => nodes.find((n) => n.id === id))
      .filter((n): n is HypothesisNode => n !== undefined)
  }, [project])

  // Radix's onSelect event does not reliably carry the native modifiers, so
  // the Shift state is captured on the pointer/keyboard press that precedes
  // the selection and consumed by the select handler.
  const shiftRef = useRef(false)

  const dispatch = (prompt: string, skill: string, newSession: boolean) =>
    void send(prompt, [skill], undefined, undefined, { newSession })

  return (
    <div
      data-testid="research-quick-actions"
      className="flex shrink-0 flex-wrap items-center gap-1"
    >
      {QUICK_ACTIONS.map((action) => {
        const Icon = ACTION_ICONS[action.key] ?? Plus

        if (action.key === 'experiment') {
          return (
            <DropdownMenu key={action.key}>
              <DropdownMenuTrigger
                data-testid="research-quick-action"
                data-skill={action.skill}
                disabled={experimentTargets.length === 0}
                title={
                  experimentTargets.length === 0
                    ? 'No active hypotheses to experiment on'
                    : `${action.label} (${action.skill}) — pick a hypothesis; Shift = new session`
                }
                className={cn(ACTION_BUTTON_CLASS, 'disabled:cursor-default disabled:opacity-50')}
              >
                <Icon className="size-3.5 text-muted-foreground" />
                {action.label}
                <ChevronDown className="size-3 text-muted-foreground" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start">
                {experimentTargets.map((node) => (
                  <DropdownMenuItem
                    key={node.id}
                    onMouseDown={(e) => {
                      shiftRef.current = e.shiftKey
                    }}
                    onKeyDown={(e) => {
                      shiftRef.current = e.shiftKey
                    }}
                    onSelect={() => {
                      dispatch(buildExperimentPrompt(node), action.skill, shiftRef.current)
                      shiftRef.current = false
                    }}
                  >
                    <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                      {node.id}
                    </span>
                    <span className="max-w-[220px] truncate">{node.title}</span>
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          )
        }

        // "Synthesize" becomes "Update report" once the report exists.
        const isUpdate = action.key === 'synthesize' && hasReport
        const label = isUpdate ? 'Update report' : action.label
        const prompt = isUpdate ? action.updatePrompt : action.prompt

        return (
          <button
            key={action.key}
            type="button"
            data-testid="research-quick-action"
            data-skill={action.skill}
            onClick={(e) => dispatch(prompt ?? action.prompt, action.skill, e.shiftKey)}
            title={`${label} (${action.skill}) — Shift = new session`}
            className={ACTION_BUTTON_CLASS}
          >
            <Icon className="size-3.5 text-muted-foreground" />
            {label}
          </button>
        )
      })}
    </div>
  )
}
