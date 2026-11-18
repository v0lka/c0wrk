// Service-model override section of the Settings "LLM" tab (ADR-131).
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
// an explicit "Not set" entry (onSelect(null)) that clears the pin. The unset
// wording names each consumer's ACTUAL fallback — they are not all the same:
// the in-loop consumers (judge, router, reflector, compaction summarizer,
// goal suggestions) run inside a session and ride its current model ("Not set
// (same as session)"); the five app-level one-shot services (titles, commit
// messages, both prompt-optimization passes, follow-up suggestions) run
// OUTSIDE any session on the application-level builder and follow the global
// default model ("Not set (same as default)").
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
 *
 * The judge/router/reflector hints also state the pinned-reasoning semantics
 * (ADR-131): a router/reflector pin re-stamps the pin's own tier-Off spelling
 * onto every call (overrideWrap in core/model_overrides.go), so the session's
 * reasoning-effort setting is inert on the wire for those two; the judge's
 * reasoning-off is its fixed verdict policy with or without a pin (sp4rk
 * tools/judge.go judgeReasoningEffort), so its pin changes the model, not the
 * policy. Keep the wording in lockstep with those sources.
 */
const SERVICE_MODEL_ROWS: ReadonlyArray<{
  key: string
  label: string
  hint: string
  /** What an unset pin falls back to: the in-loop consumers ride the session's
   *  current model; the app-level one-shot services run outside any session on
   *  the application-level builder (backend/application.go app.builder) and
   *  follow the global default model. */
  fallback: 'session' | 'default'
}> = [
  {
    key: 'judge',
    label: 'Tool-call judge',
    hint: 'Strict verdicts for assisted/silent tool confirmations. Verdicts always run with reasoning off for whichever model serves them — a pin changes that model, not the reasoning policy.',
    fallback: 'session',
  },
  {
    key: 'router',
    label: 'Routing classifier',
    hint: 'Message classification on every send. A pinned model always runs with reasoning off for its own family — the session\'s reasoning-effort setting does not apply.',
    fallback: 'session',
  },
  {
    key: 'reflector',
    label: 'Reflector',
    hint: 'Between-steps reflection of the ReAct loop. A pinned model always runs with reasoning off for its own family — the session\'s reasoning-effort setting does not apply.',
    fallback: 'session',
  },
  {
    key: 'title',
    label: 'Session titles',
    hint: 'One-shot title generation.',
    fallback: 'default',
  },
  {
    key: 'commit_message',
    label: 'Commit messages',
    hint: 'Git commit message generation.',
    fallback: 'default',
  },
  {
    key: 'optimize_extract',
    label: 'Prompt optimization — extract',
    hint: 'Extract pass of prompt optimization.',
    fallback: 'default',
  },
  {
    key: 'optimize_rewrite',
    label: 'Prompt optimization — rewrite',
    hint: 'Rewrite pass of prompt optimization.',
    fallback: 'default',
  },
  {
    key: 'compaction_summarize',
    label: 'Compaction summarizer',
    hint: 'Context-compaction summarization.',
    fallback: 'session',
  },
  {
    key: 'goal_suggest',
    label: 'Goal suggestions',
    hint: 'Goal-loop suggestion call.',
    fallback: 'session',
  },
  {
    key: 'prompt_suggest',
    label: 'Follow-up suggestions',
    hint: 'Proactive follow-up prompt suggestions.',
    fallback: 'default',
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
  /** The draft's global default (composite id) — marks the default entry in
   *  each menu; unset rows are labeled by their fallback class, so the
   *  default-class rows read "Not set (same as default)". */
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
            Route individual one-shot calls to a dedicated model — e.g. a small
            fast local model for titles and commit messages, a strict one for
            the tool-call judge. Each picker lists the models enabled on the
            providers above; "Not set" clears the override, and what the call
            then uses depends on where it runs: calls made inside a running
            session (the judge, routing, reflection, compaction, goal
            suggestions) use that session's current model, while the calls made
            outside any session — titles, commit messages, prompt optimization,
            follow-up suggestions — use the global default model. Applied when
            this dialog saves and in every new session. A pin is validated
            against the provider NAME only: a pin naming a provider that is
            no longer configured falls back the same way (a warning is
            logged), while the model part is passed to that provider as-is —
            it is not checked against the provider's enabled models.
          </p>

          {SERVICE_MODEL_ROWS.map(({ key, label, hint, fallback }) => (
            <div key={key} className="flex flex-col gap-1" data-testid={`service-model-row-${key}`}>
              <label className="text-xs font-medium">{label}</label>
              <ModelPickerMenu
                ariaLabel={`${label} model`}
                models={models}
                defaultModel={defaultModel}
                loaded
                value={value[key] ?? null}
                onSelect={(id) => onChange(key, id)}
                unsetLabel={
                  fallback === 'default'
                    ? 'Not set (same as default)'
                    : 'Not set (same as session)'
                }
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
