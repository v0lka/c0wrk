// Service-model override section of the Settings "LLM" tab (ADR-130).
//
// One collapsed-by-default collapsible shell — the provider accordions' /
// EmbeddedLLMSettings header anatomy — listing every fixed one-shot service
// call `llm.service_models` can re-route onto a dedicated composite
// "provider/model": the tool-call judge (assisted/silent confirmations), the
// routing classifier, the ReAct reflector, and the seven ServiceKind calls
// (titles, commit messages, the two prompt-optimization passes, the
// compaction summarizer, goal suggestions, follow-up suggestions).
//
// Each row is a ModelPickerMenu over the DRAFT's enabled models (the same
// list the global default picker above uses, embedded models included) with
// an explicit "Default" entry (onSelect(null)) that clears the pin so the
// call falls back to the session default.
//
// GATED: LLMSettings mounts this block only while the experimental switch is
// on — mirroring the embedded section. The gate is honest end-to-end: the
// backend adapter (serviceModelsWithExperimentalGate) drops the persisted
// llm.service_models section from every BuilderConfig while the switch is
// off, so a block rendered then would advertise pins that resolve nothing.
// The pins themselves stay persisted and reappear when the switch returns.
//
// This component holds no state beyond the shell's expanded flag: the pins
// live in useLLMConfig's draft (value / onChange), so they save through the
// standard debounced full-form save and survive the dialog lifecycle.

import { useState } from 'react'
import { ArrowRightLeft, ChevronDown, ChevronRight } from 'lucide-react'
import { ModelPickerMenu } from '@/components/ui/ModelPickerMenu'
import type { ModelRef } from '@/lib/modelId'

/**
 * The fixed service keys and their presentation, in the backend's canonical
 * declaration order (backend/config/service_models.go serviceModelKeyList).
 * The set is closed on the Go side — an unknown key cannot reach this list
 * because it is dropped at load/save with a warning — so a new backend key
 * must be added here in the same change that extends the backend.
 */
const SERVICE_MODEL_ROWS: ReadonlyArray<{ key: string; label: string; hint: string }> = [
  {
    key: 'judge',
    label: 'Tool-call judge',
    hint: 'Strict verdicts for assisted/silent tool confirmations.',
  },
  {
    key: 'router',
    label: 'Routing classifier',
    hint: 'Message classification on every send.',
  },
  {
    key: 'reflector',
    label: 'Reflector',
    hint: 'Between-steps reflection of the ReAct loop.',
  },
  {
    key: 'title',
    label: 'Session titles',
    hint: 'One-shot title generation.',
  },
  {
    key: 'commit_message',
    label: 'Commit messages',
    hint: 'Git commit message generation.',
  },
  {
    key: 'optimize_extract',
    label: 'Prompt optimization — extract',
    hint: 'Extract pass of prompt optimization.',
  },
  {
    key: 'optimize_rewrite',
    label: 'Prompt optimization — rewrite',
    hint: 'Rewrite pass of prompt optimization.',
  },
  {
    key: 'compaction_summarize',
    label: 'Compaction summarizer',
    hint: 'Context-compaction summarization.',
  },
  {
    key: 'goal_suggest',
    label: 'Goal suggestions',
    hint: 'Goal-loop suggestion call.',
  },
  {
    key: 'prompt_suggest',
    label: 'Follow-up suggestions',
    hint: 'Proactive follow-up prompt suggestions.',
  },
]

export function ServiceModelsSettings({
  models,
  defaultModel,
  value,
  onChange,
}: {
  /** Enabled models across the draft's providers — the same list the global
   *  default picker consumes; the embedded entry appears whenever installed. */
  models: readonly ModelRef[]
  /** The draft's global default (composite id) — what "Default" resolves to. */
  defaultModel: string
  /** Current pins: service key → composite "provider/model"; missing key = default. */
  value: Record<string, string>
  /** Set (composite id) or clear (null) one pin. */
  onChange: (key: string, model: string | null) => void
}) {
  // The collapsible shell: collapsed by default like the embedded block — an
  // advanced, gated surface the operator opens deliberately.
  const [expanded, setExpanded] = useState(false)

  return (
    <div className="flex flex-col rounded-lg border" data-testid="service-models-settings">
      {/* Collapsible header — the provider accordions' header anatomy: a
          full-width button with the chevron + label on the left. */}
      <button
        type="button"
        title="Service models"
        aria-expanded={expanded}
        data-testid="service-models-toggle"
        className="flex w-full items-center justify-between px-4 py-3 text-sm font-medium hover:bg-muted/50"
        onClick={() => setExpanded((v) => !v)}
      >
        <span className="flex items-center gap-2">
          {expanded ? (
            <ChevronDown className="h-4 w-4" />
          ) : (
            <ChevronRight className="h-4 w-4" />
          )}
          <ArrowRightLeft className="h-4 w-4 text-muted-foreground" />
          Service models
        </span>
      </button>

      {expanded && (
        <div className="flex flex-col gap-3 border-t px-4 py-4">
          <p className="text-xs text-muted-foreground">
            Route individual one-shot calls to a dedicated model instead of the
            session default — e.g. a small fast local model for titles and
            commit messages, a strict one for the tool-call judge. Each picker
            lists the models enabled on the providers above; "Default" clears
            the override. Applied when this dialog saves and in every new
            session; a pin pointing at a removed provider or disabled model
            quietly falls back to the default.
          </p>

          {SERVICE_MODEL_ROWS.map(({ key, label, hint }) => (
            <div key={key} className="flex flex-col gap-1" data-testid={`service-model-row-${key}`}>
              <label className="text-xs font-medium">{label}</label>
              <ModelPickerMenu
                ariaLabel={`${label} model`}
                models={models}
                defaultModel={defaultModel}
                loaded
                value={value[key] ?? null}
                onSelect={(id) => onChange(key, id)}
                className="w-full h-9 text-sm px-3 max-w-none"
              />
              <p className="text-xs text-muted-foreground">{hint}</p>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
