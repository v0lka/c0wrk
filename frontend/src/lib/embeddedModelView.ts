// Pure state → surface derivation for the status-bar indicator of the embedded
// local model (`components/layout/EmbeddedModelStatus.tsx`).
//
// React-free by design (the same split as lib/gitGraphRender and
// lib/embeddedTuningDisplay): the component owns the subscription, the memo and
// the markup; this module owns the decision of WHICH surface the bar shows and
// the exact words of its tooltip, so both are unit-testable without a renderer.
//
// The block's contract lives here too — it returns null when there is nothing
// to say, and the component renders no separator in that case, so a hidden
// indicator never leaves a stray divider in the bar. "Nothing to say" is
// exactly: not installed with no pending failure, and installed-but-stopped (an
// idle install is not an event; the Settings block is where it is acted on).
//
// The idle countdown is deliberately NOT derived here: `idle_remaining_seconds`
// is a snapshot field that only refreshes on a transition, so a ticking number
// computed from it would be wrong within a second. The tooltip states the
// policy instead.
//
// `status.available` is deliberately NOT part of the contract either: it is
// false only before startup (the agent directory is unset), in which case the
// same snapshot also reports nothing installed — so the flag carries no surface
// information of its own, and folding it in would let an "unconstructable
// subsystem" hide a real error that snapshot reports. The surface follows the
// install/supervision fields only (pinned by embeddedModelView.test.ts).

import { formatBytes } from '@/lib/formatters'
import { bareModel } from '@/lib/modelId'
import {
  EMBEDDED_COMPONENT_ORDER,
  embeddedComponentLabel,
  embeddedStageLabel,
} from '@/lib/embeddedLLMLabels'
import type { EmbeddedLLMStatus } from '@/api/embedded'
import type { EmbeddedLLMProgressByComponent } from '@/stores/embeddedLLMStore'
import type { EmbeddedLLMComponent, EmbeddedLLMInstallProgressData } from '@/types/events'

/** The artifact whose transfer is currently in flight, in install order — the
 *  first reported component that has not reached `done`. Null before the first
 *  progress event and while the install finalizes (manifest + config sink). */
export function activeProgress(
  progress: EmbeddedLLMProgressByComponent,
): { component: EmbeddedLLMComponent; entry: EmbeddedLLMInstallProgressData } | null {
  for (const component of EMBEDDED_COMPONENT_ORDER) {
    const entry = progress[component]
    if (entry && entry.stage !== 'done') return { component, entry }
  }
  return null
}

/** One renderable surface of the block. `title` is the full tooltip; the
 *  visible text is deliberately shorter than it (the stage and the byte counts
 *  live in the tooltip — the bar has no room for them). */
export type EmbeddedModelView =
  | {
      kind: 'install'
      /** Visible artifact name, or null while nothing has reported yet. */
      label: string | null
      /** Null for a byte-less stage — an indeterminate pulse, never a fake 0%. */
      percent: number | null
      title: string
    }
  | { kind: 'loading'; title: string }
  | { kind: 'loaded'; name: string; title: string }
  | { kind: 'error'; message: string; title: string }

/** Human-readable model name of the resident install. */
export function modelName(status: EmbeddedLLMStatus): string {
  return status.model_name || bareModel(status.model_id) || 'Embedded model'
}

/** The resident indicator's tooltip: identity, endpoint, pid and the residency
 *  policy (the idle countdown itself is not rendered — see the file header). */
export function loadedTitle(status: EmbeddedLLMStatus, name: string): string {
  const parts: string[] = [`Embedded model resident — ${name}`]
  const identity = [status.packing, status.backend].filter((v) => v !== '').join('/')
  if (identity) parts.push(identity)
  if (status.context_size > 0) parts.push(`context ${status.context_size}`)
  if (status.base_url) parts.push(`serving on ${status.base_url}`)
  if (status.pid > 0) parts.push(`pid ${status.pid}`)
  parts.push(
    status.auto_unload_enabled
      ? `auto-unloads after ${status.auto_unload_minutes} min idle`
      : 'stays resident until unloaded',
  )
  return parts.join(' · ')
}

/** Pure state → surface derivation. Returns null when the block has nothing to
 *  say (and must therefore render no separator either). */
export function deriveView(
  status: EmbeddedLLMStatus | null,
  installing: boolean,
  progress: EmbeddedLLMProgressByComponent,
): EmbeddedModelView | null {
  // A live install run outranks every snapshot: the backend refuses a load
  // while one is in flight, and `installing` is raised by the progress events
  // themselves, which can precede a stale snapshot refresh.
  if (installing) {
    const active = activeProgress(progress)
    if (!active) {
      return {
        kind: 'install',
        label: null,
        percent: null,
        title: 'Installing the embedded model…',
      }
    }
    const { component, entry } = active
    const known = entry.bytes_total > 0
    const percent = known
      ? Math.min(100, Math.max(0, Math.round((entry.bytes_done / entry.bytes_total) * 100)))
      : null
    const bytes = known ? `${formatBytes(entry.bytes_done)} / ${formatBytes(entry.bytes_total)}` : null
    const label = embeddedComponentLabel(component)
    const stage = embeddedStageLabel(entry.stage)
    return {
      kind: 'install',
      label,
      percent,
      title: `Installing the embedded model — ${label}: ${stage}${bytes ? ` (${bytes})` : ''}`,
    }
  }

  if (status === null) return null
  if (status.loading) {
    return {
      kind: 'loading',
      title: `Loading ${modelName(status)} into memory — a multi-gigabyte weight load takes minutes`,
    }
  }
  if (status.loaded) {
    const name = modelName(status)
    return { kind: 'loaded', name, title: loadedTitle(status, name) }
  }

  const message = status.error
  if (status.state === 'error' || message !== '') {
    const text = message !== '' ? message : 'The embedded model failed.'
    return {
      kind: 'error',
      message: text,
      title: `${text} — open Settings → LLM → Embedded LLM for details`,
    }
  }

  // Not installed, or installed and stopped: nothing the bar needs to say.
  return null
}
