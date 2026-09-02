import { Sparkles, Play } from 'lucide-react'
import { useMessageSender } from '@/hooks/useMessageSender'
import { useResearchStore } from '@/stores/researchStore'
import { buildNextStepPrompt } from './researchActions'

/**
 * "Next step" card — renders the backend's single recommended next research
 * action (t3's GetResearchNextStep) with a one-click Execute button. Execute
 * dispatches the matching `research-*` skill through the normal message sender
 * (which auto-creates a session and mirrors the optimistic message) — the user
 * never types a raw `/skill` prompt.
 */
export function ResearchNextStep() {
  const nextStep = useResearchStore((s) => s.nextStep)
  const { send } = useMessageSender()

  if (!nextStep) {
    return (
      <div
        data-testid="research-next-step"
        className="shrink-0 rounded-md border border-dashed border-border px-3 py-2 text-xs text-muted-foreground/70"
      >
        No next step available yet
      </div>
    )
  }

  const prompt = buildNextStepPrompt(nextStep)

  return (
    <div
      data-testid="research-next-step"
      className="flex shrink-0 flex-col gap-1.5 rounded-md border border-border bg-secondary/20 px-3 py-2"
    >
      <div className="flex items-center gap-1.5">
        <Sparkles className="size-3.5 shrink-0 text-highlight" />
        <span className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
          Next step
        </span>
        <span className="ml-auto shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-foreground">
          {nextStep.skill}
        </span>
      </div>

      <p className="text-xs leading-relaxed text-foreground/90">{nextStep.reason}</p>

      {nextStep.target && (
        <div className="text-[11px] text-muted-foreground">
          Target hypothesis:{' '}
          <span className="font-mono text-[10px] text-foreground">
            {nextStep.target}
          </span>
        </div>
      )}

      <button
        type="button"
        onClick={() => void send(prompt, [nextStep.skill])}
        className="inline-flex items-center justify-center gap-1.5 self-start rounded-md bg-primary px-2.5 py-1 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90"
      >
        <Play className="size-3.5" />
        Execute
      </button>
    </div>
  )
}
