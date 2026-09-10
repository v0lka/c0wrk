import { useMemo } from 'react'
import {
  Plus,
  FlaskConical,
  ClipboardCheck,
  Split,
  FileCheck2,
  type LucideIcon,
} from 'lucide-react'
import { useMessageSender } from '@/hooks/useMessageSender'
import { useResearchStore, selectActiveProject, selectActiveHypothesisId } from '@/stores/researchStore'
import { cn } from '@/lib/utils'
import {
  QUICK_ACTIONS,
  buildExperimentPrompt,
  buildRecordResultPrompt,
  buildDecisionPrompt,
} from './researchActions'
import { isTerminal } from './researchDagRender'
import type { HypothesisNode } from '@/types/models'

/** Icon per quick-action key (stable; the key is part of the constant list). */
const ACTION_ICONS: Record<string, LucideIcon> = {
  experiment: FlaskConical,
  'record-result': ClipboardCheck,
  decision: Split,
  synthesize: FileCheck2,
}

const ACTION_BUTTON_CLASS =
  'inline-flex items-center gap-1 rounded-md border border-border bg-background px-2 py-1 text-[11px] text-foreground transition-colors hover:bg-muted'

/** A quick action's derived runtime state: whether the button is enabled,
 *  why it is not (the disabled tooltip), and the prompt a click dispatches
 *  (scoped to the dashboard's current hypothesis where the action targets
 *  one). */
interface ActionState {
  enabled: boolean
  disabledReason: string
  prompt: string
}

/**
 * Quick-actions row — one button per research lifecycle gesture, each
 * dispatching its matching `research-*` skill (with a constant prompt)
 * through the message sender. This is the "start doing" surface: no raw
 * typing.
 *
 * Every button's enablement is derived from the dashboard's state so no
 * workflow-invalid gesture is ever offered: Run experiment requires the
 * current card (see ResearchHypothesisPicker) to be open or in-progress,
 * Record result requires it to be in-progress (an experiment ran), Decision
 * requires at least one terminal hypothesis (something to decide about),
 * and Synthesize/Update report requires at least one hypothesis. The
 * hypothesis-targeted prompts are scoped to the current card.
 *
 * Shift modifier: holding Shift while clicking dispatches into a brand-new
 * session instead of the active one. "Synthesize" flips to "Update report"
 * once a report exists.
 */
export function ResearchQuickActions() {
  const { send } = useMessageSender()
  const project = useResearchStore(selectActiveProject)
  const currentId = useResearchStore(selectActiveHypothesisId)
  const hasReport = project?.has_report ?? false

  // The dashboard's current card, resolved against the active research's
  // graph ('' from the selector = no resolvable card).
  const currentNode = useMemo<HypothesisNode | null>(
    () =>
      currentId !== ''
        ? project?.graph.nodes.find((n) => n.id === currentId) ?? null
        : null,
    [project, currentId],
  )

  const terminalCount = useMemo(
    () => (project?.graph.nodes ?? []).filter((n) => isTerminal(n.status)).length,
    [project],
  )
  const total = project?.metrics.total ?? 0

  // [22]a: send() renders sendMessage failures in-chat itself, but RETHROWS
  // when the auto-created session fails (the documented splash race). The
  // rejection is surfaced on the research panel's own error banner (the
  // research store) — deliberately NOT a global toast, which could fire
  // while the user is typing into the chat input.
  const dispatch = (prompt: string, skill: string, newSession: boolean) =>
    // Promise.resolve: tolerate a sender that returns void (defensive — the
    // real send is async, but mocks/type skew must not crash the click).
    Promise.resolve(
      send(prompt, [skill], undefined, undefined, { newSession }),
    ).catch((err) => {
      useResearchStore
        .getState()
        .setError(
          `Failed to dispatch ${skill}: ${
            err instanceof Error ? err.message : 'unknown error'
          }`,
        )
    })

  // Derive each action's enablement + prompt from the current card and the
  // project's graph state.
  const actionState = (key: string, basePrompt: string): ActionState => {
    switch (key) {
      case 'experiment': {
        const status = currentNode?.status
        const runnable = status === 'open' || status === 'in-progress'
        return {
          enabled: runnable,
          disabledReason:
            currentNode === null
              ? 'No current hypothesis selected'
              : `The current hypothesis is ${status} — experiments run on open or in-progress cards`,
          prompt: currentNode ? buildExperimentPrompt(currentNode) : basePrompt,
        }
      }
      case 'record-result': {
        const recordable = currentNode?.status === 'in-progress'
        return {
          enabled: recordable,
          disabledReason:
            currentNode === null
              ? 'No current hypothesis selected'
              : `The current hypothesis is ${currentNode.status} — results are recorded for in-progress cards`,
          prompt: currentNode ? buildRecordResultPrompt(currentNode) : basePrompt,
        }
      }
      case 'decision': {
        return {
          enabled: terminalCount > 0,
          disabledReason: 'No terminal hypotheses yet — nothing to decide about',
          // Scoped to the current card when one is selected; the project
          // review prompt when the front is empty (all terminal).
          prompt: currentNode ? buildDecisionPrompt(currentNode) : basePrompt,
        }
      }
      default: {
        // synthesize — a project-level gesture, never hypothesis-scoped.
        return {
          enabled: total > 0,
          disabledReason: 'No hypotheses yet — nothing to synthesize',
          prompt: basePrompt,
        }
      }
    }
  }

  return (
    <div
      data-testid="research-quick-actions"
      className="flex shrink-0 flex-wrap items-center gap-1"
    >
      {QUICK_ACTIONS.map((action) => {
        const Icon = ACTION_ICONS[action.key] ?? Plus

        // "Synthesize" becomes "Update report" once the report exists.
        const isUpdate = action.key === 'synthesize' && hasReport
        const label = isUpdate ? 'Update report' : action.label
        const basePrompt = isUpdate ? action.updatePrompt : action.prompt

        const { enabled, disabledReason, prompt } = actionState(
          action.key,
          basePrompt ?? action.prompt,
        )

        return (
          <button
            key={action.key}
            type="button"
            data-testid="research-quick-action"
            data-skill={action.skill}
            disabled={!enabled}
            onClick={(e) => {
              if (!enabled) return
              dispatch(prompt, action.skill, e.shiftKey)
            }}
            title={
              enabled
                ? `${label} (${action.skill}) — Shift = new session`
                : disabledReason
            }
            className={cn(
              ACTION_BUTTON_CLASS,
              'disabled:cursor-default disabled:opacity-50 disabled:hover:bg-background',
            )}
          >
            <Icon className="size-3.5 text-muted-foreground" />
            {label}
          </button>
        )
      })}
    </div>
  )
}
