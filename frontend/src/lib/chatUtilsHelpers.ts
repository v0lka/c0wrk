/**
 * Internal helpers for chatUtils.ts — not part of the public API.
 * Extracted to keep chatUtils.ts under 200 lines.
 */
import type { ChatMessageUI, DisplayItem } from '@/types/messages'
import { isArrayOf } from '@/types/guards'
import { isAgentMetricsData } from '@/types/events'

/** Build a composite tool key for correlating tool_call ↔ tool_result. */
export function makeToolKey(
  planStepId: string | undefined,
  step: number | string,
  callIdx?: number | string,
  retryAttempt?: number | string,
): string {
  return `${planStepId ?? ''}:${step}${callIdx !== undefined ? `:${callIdx}` : ''}${retryAttempt ? `:r${retryAttempt}` : ''}`
}

/**
 * Collapse consecutive thought DisplayItems into thought_group items.
 * A single isolated thought stays as-is.
 */
export function collapseThoughts(items: DisplayItem[]): DisplayItem[] {
  const result: DisplayItem[] = []
  let i = 0
  while (i < items.length) {
    const current = items[i]
    if (!current) break
    if (current.kind === 'thought') {
      const thoughts: Array<{ content: string; reasoning?: string }> = []
      const firstId = current.id
      while (i < items.length) {
        const item = items[i]
        if (!item || item.kind !== 'thought') break
        thoughts.push({ content: item.content, reasoning: item.reasoning })
        i++
      }
      if (thoughts.length === 1) {
        const prev = items[i - 1]!
        result.push(prev)
      } else {
        result.push({ kind: 'thought_group', id: `tg-${firstId}`, thoughts })
      }
    } else {
      result.push(current)
      i++
    }
  }
  return result
}

/**
 * Normalize a thought's text content for display.
 *
 * Reasoning models (DeepSeek/GLM) commonly emit empty text content on
 * tool-call steps, or the model may echo the "(proceeding)" placeholder back
 * into its own content. In both cases there is no meaningful visible content,
 * so the content block must be suppressed while `reasoning_content` keeps
 * rendering on its own in the collapsible "Reasoning" card.
 *
 * Returns the original content unchanged when it is meaningful, and '' when it
 * is empty or only the "(proceeding)" placeholder.
 */
export function normalizeThoughtContent(content: string): string {
  const trimmed = content.trim()
  const normalized = trimmed.toLowerCase()
  if (normalized === '' || normalized === '(proceeding)' || normalized === 'proceeding') {
    return ''
  }
  return content
}

/**
 * Suppress the visible `content` of a thought that duplicates the final
 * answer delivered as an assistant message.
 *
 * The executor emits a `Thought` event carrying the model's text `content`
 * for every step — including the finish step. ThoughtBlock renders that
 * `content` as visible Markdown. So when the agent writes its answer as text
 * `content` AND delivers the same text via `finish` (task_complete.output),
 * the identical text would otherwise render twice: once as a thought and
 * once as the canonical assistant answer. The same duplication arises in the
 * implicit text-only finish path, where the streamed content becomes both a
 * thought and an assistant_done message.
 *
 * For each thought whose trimmed content exactly matches the trimmed content
 * of a LATER assistant item in the same container, this clears the redundant
 * thought content (preserving any reasoning) so the answer appears only as
 * the assistant message. A thought left with neither content nor reasoning
 * is dropped entirely. Only a LATER assistant is considered, so a thought is
 * never suppressed by an answer from an earlier exchange.
 *
 * Applied before collapseThoughts so it operates on individual thought items.
 */
export function dedupThoughtVsAnswer(items: DisplayItem[]): DisplayItem[] {
  const result: DisplayItem[] = []
  for (let i = 0; i < items.length; i++) {
    const it = items[i]!
    if (it.kind === 'thought') {
      const thoughtContent = (it.content ?? '').trim()
      if (thoughtContent !== '') {
        let duplicatesAnswer = false
        for (let j = i + 1; j < items.length; j++) {
          const later = items[j]!
          if (later.kind === 'assistant' && (later.message.content ?? '').trim() === thoughtContent) {
            duplicatesAnswer = true
            break
          }
        }
        if (duplicatesAnswer) {
          // Keep the thought only if it still carries reasoning to show.
          if (it.reasoning && it.reasoning.trim() !== '') {
            result.push({ ...it, content: '' })
          }
          // Otherwise drop the now-empty thought entirely.
          continue
        }
      }
    }
    result.push(it)
  }
  return result
}

// -- History reconstruction helpers (used by chatMessageToUI) --

/** Reconstruct human-readable content from metadata to match live events. */
export function reconstructContent(role: string, rawContent: string, meta: Record<string, unknown> | undefined): string {
  if (!meta) return rawContent
  switch (role) {
    case 'routing': {
      const d = meta.domain as string | undefined, c = meta.complexity as string | undefined
      return (d || c) ? `Domain: ${d ?? ''} | Complexity: ${c ?? ''}` : rawContent
    }
    case 'tool_call': { const t = meta.tool as string | undefined; return t ? `${t}(${(meta.args as string) ?? ''})` : rawContent }
    case 'thought': {
      // Older persisted rows may have full JSON metadata as content when the actual content was empty.
      // Detect and discard so the UI doesn't render raw JSON.
      if (rawContent.startsWith('{') && rawContent.includes('"content"')) {
        try {
          const parsed = JSON.parse(rawContent) as { content?: string }
          return parsed.content ?? ''
        } catch { /* not JSON — pass through */ }
      }
      return rawContent
    }
    case 'thinking': return `Step ${(meta.step_num as number) ?? ''}...`
    case 'error': return (meta.error as string) || rawContent
    case 'plan_step_start': return (meta.description as string) || ''
    case 'plan_step_complete': case 'plan': return ''
    case 'retry': {
      const a = meta.attempt as number | undefined, m = meta.max_attempts as number | undefined
      return (a !== undefined && m !== undefined) ? `Retry attempt ${a}/${m}` : rawContent
    }
    case 'step_retry': {
      const a = meta.attempt as number | undefined, m = meta.max_attempts as number | undefined
      return (a !== undefined && m !== undefined) ? `Retrying step ${a}/${m}...` : rawContent
    }
    case 'subagent_launch': { const d = meta.description as string | undefined; return d ? `SubAgent: ${d}` : rawContent }
    case 'tool_confirm': { const t = meta.tool as string | undefined; return t ? `Confirm: ${t}` : rawContent }
    case 'ask_user': return (meta.question as string) || rawContent
    case 'task_cancelled': return 'Task was cancelled'
    case 'status': {
      // skills_activated events are persisted by the Go backend with an empty
      // content; the persister then writes the raw JSON payload
      // ({"skills":[...]}) as the message content (see backend/session/
      // event_persister.go: "skills_activated" → role "status"). Reconstruct
      // the same human-readable text the live useLifecycleEvents handler
      // produces ("Skills activated: …") so a reloaded session/project shows
      // an identical message instead of raw JSON. Type-guard the skills array
      // at this ingestion point so downstream rendering stays typed.
      if (isArrayOf(meta.skills, (s): s is string => typeof s === 'string')) {
        return `Skills activated: ${meta.skills.join(', ')}`
      }
      // tools_assigned events follow the same persistence path: the persister
      // writes the raw JSON payload ({"tools":[...]}) as content. Reconstruct
      // the live handler's "Tools assigned: …" text on reload.
      if (isArrayOf(meta.tools, (s): s is string => typeof s === 'string')) {
        return `Tools assigned: ${meta.tools.join(', ')}`
      }
      // agent_metrics rows carry the per-run quality report; they are store-
      // state, not chat content — history-load restores them into
      // planStore.sessionStats (see lastAgentMetricsFromHistory) and filters
      // them out of the chat, so raw JSON never renders. The empty string is
      // the fallback for any path that converts rows without filtering.
      if (isAgentMetricsData(meta)) {
        return ''
      }
      return (meta.content as string) || rawContent
    }
    case 'task_resumed': return rawContent
    case 'goal_status':
      // Persisted goal_status rows carry the raw snapshot JSON as content (the
      // persister writes metadata into content for non-assistant roles). The
      // chat renders the transition notice from the metadata in groupMessages,
      // so keep content empty here — raw JSON must never surface.
      return ''
    case 'task_failed_resumable':
      return (meta.message as string) || 'Plan execution failed. You can resume to retry from where it left off.'
    case 'step_limit': {
      const reason = meta.reason as string | undefined
      if (reason) return `Circuit breaker: ${reason}`
      const cur = meta.current_step as number | undefined, max = meta.max_steps as number | undefined
      return (cur !== undefined && max !== undefined) ? `Step limit reached: ${cur} of ${max}` : rawContent
    }
    default: return rawContent
  }
}

/** Build a semantic ID from history metadata for cross-referencing. */
export function buildHistoryId(
  dbId: number,
  role: string,
  meta: Record<string, unknown> | undefined,
  timestamp: number,
): string {
  if (!meta) return `history-${dbId}`

  switch (role) {
    case 'routing':
      return `routing-${timestamp}`
    case 'thinking':
    case 'step_done': {
      const stepNum = meta.step_num as number | undefined
      return stepNum !== undefined ? `step-${stepNum}` : `history-${dbId}`
    }
    case 'thought': {
      const stepNum = meta.step_num as number | undefined
      return `thought-${stepNum ?? 0}-${timestamp}`
    }
    case 'tool_call': {
      const toolCallId = meta.tool_call_id as string | undefined
      if (toolCallId) return `tool-${toolCallId}`
      const planStepId = meta.plan_step_id as string | undefined
      const step = meta.step as number | string | undefined
      const callIdx = meta.call_idx as number | string | undefined
      const retryAttempt = meta.retry_attempt as number | undefined
      const retrySuffix = retryAttempt ? `-r${retryAttempt}` : ''
      if (planStepId && step !== undefined) return `tool-${planStepId}-${step}${callIdx !== undefined ? `-${callIdx}` : ''}${retrySuffix}`
      if (step !== undefined) return `tool-${step}${callIdx !== undefined ? `-${callIdx}` : ''}${retrySuffix}`
      return `history-${dbId}`
    }
    case 'tool_result':
      return `history-${dbId}`
    case 'plan':
      return `plan-${timestamp}`
    case 'plan_step_start': {
      const stepId = meta.step_id as string | undefined
      return stepId ? `plan-step-start-${stepId}-${timestamp}` : `history-${dbId}`
    }
    case 'plan_step_complete': {
      const stepId = meta.step_id as string | undefined
      return stepId ? `plan-step-complete-${stepId}-${timestamp}` : `history-${dbId}`
    }
    case 'retry':
      return `retry-${timestamp}`
    case 'step_retry':
      return `step-retry-${timestamp}`
    case 'subagent_launch': {
      const stepId = meta.step_id as string | undefined
      return stepId ? `subagent-${stepId}-launch` : `history-${dbId}`
    }
    case 'subagent_complete': {
      const stepId = meta.step_id as string | undefined
      return stepId ? `subagent-${stepId}-complete` : `history-${dbId}`
    }
    case 'assistant':
      return `assistant-${timestamp}`
    case 'error':
      return `error-${timestamp}`
    case 'task_cancelled':
      return `cancelled-${timestamp}`
    case 'tool_confirm': {
      const confirmId = meta.confirm_id as string | undefined
      return confirmId ? `tool-confirm-${confirmId}` : `history-${dbId}`
    }
    case 'ask_user': {
      const requestId = meta.request_id as string | undefined
      return requestId ? `ask-user-${requestId}` : `history-${dbId}`
    }
    case 'status':
      return `status-${dbId}`
    case 'task_resumed':
      return `task-resumed-${timestamp}`
    case 'task_failed_resumable':
      return `resume-${timestamp}`
    case 'step_limit': {
      const requestId = meta.request_id as string | undefined
      return requestId ? `step-limit-${requestId}` : `history-${dbId}`
    }
    case 'plan_review': {
      const requestId = meta.request_id as string | undefined
      return requestId ? `plan-review-${requestId}` : `history-${dbId}`
    }
    case 'review_prompt': {
      // Derive the id from the backend-assigned prompt_id so the live card
      // (added right after SaveReviewPrompt) and the reloaded history share an
      // id — mergeHistoryMessages then dedupes them instead of dropping the
      // prompt on the next session switch.
      const promptId = meta.prompt_id as string | undefined
      return promptId ? `review-prompt-${promptId}` : `history-${dbId}`
    }
    case 'goal_proposal': {
      // The live card (handleGoalProposalEvent) and the pending-actions
      // reconciliation (reconcileRuntimeStatus) both use
      // `goal-proposal-${request_id}`. Without this case the reloaded history
      // record falls through to `history-${dbId}`, producing a second card with
      // a non-matching id that mergeHistoryMessages cannot dedupe — the stuck
      // "Proposed Goal" duplicate on background sessions.
      const requestId = meta.request_id as string | undefined
      return requestId ? `goal-proposal-${requestId}` : `history-${dbId}`
    }
    case 'goal_status': {
      // The live handler (handleGoalStatusEvent) emits the turn-transition
      // notice with the deterministic id `goal-status-${run}-${turn}-${status}`
      // (run = created_at, the per-run identity). Give the persisted snapshot
      // the same id so a live event landing during the history RPC dedupes
      // against the reloaded row instead of rendering twice, and so two goal
      // runs whose turn counts both reset to 1 don't collide.
      const turn = meta.turn as number | undefined
      const status = meta.status as string | undefined
      const run = meta.created_at as number | undefined
      return (typeof turn === 'number' && typeof status === 'string')
        ? `goal-status-${run ?? 'legacy'}-${turn}-${status}`
        : `history-${dbId}`
    }
    default:
      return `history-${dbId}`
  }
}

/**
 * Resolve a tool matching key — prefers tool_call_id, falls back to composite key.
 */
export function resolveToolKey(meta: Record<string, unknown>, planStepId?: string): string | undefined {
  const toolCallId = meta.tool_call_id as string | undefined
  if (toolCallId) return toolCallId
  const step = meta.step as number | string | undefined
  if (step === undefined) return undefined
  const callIdx = meta.call_idx as number | undefined
  const retryAttempt = meta.retry_attempt as number | undefined
  return makeToolKey(planStepId, step, callIdx, retryAttempt)
}

/** Extracts typed metadata from a ChatMessageUI, or undefined if missing. */
export function extractMeta(msg: ChatMessageUI): Record<string, unknown> | undefined {
  return (typeof msg.metadata === 'object' && msg.metadata !== null) ? msg.metadata : undefined
}
