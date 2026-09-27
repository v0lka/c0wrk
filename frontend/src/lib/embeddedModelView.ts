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
// The resident surface MAY additionally show the active session's median
// output-token throughput ("name · N tok/s"). The metric is GATED here, not in
// the component: only when the session's tracked model is the resident embedded
// model (normalized comparison, composite selectors reduced via bareModel) and
// the median rests on at least three per-call samples. Gated off, the loaded
// surface renders exactly as it did before the metric existed.
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
import type { TokenInfo } from '@/types/models'

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
  | {
      kind: 'loaded'
      name: string
      /** Visible median output-token throughput ("N tok/s" is appended by the
       *  component as "name · N tok/s"). Null when the metric is gated off —
       *  the surface then renders exactly as it did before the metric existed. */
      tokPerSec: string | null
      /** Per-call sample count behind the median (tooltip wording); 0 when
       *  tokPerSec is null. */
      tokSamples: number
      title: string
    }
  | { kind: 'error'; message: string; title: string }

/** Human-readable model name of the resident install. */
export function modelName(status: EmbeddedLLMStatus): string {
  return status.model_name || bareModel(status.model_id) || 'Embedded model'
}

/** True when the session's tracked model is the resident embedded model — the
 *  throughput metric describes THIS model, so a session running anything else
 *  (a remote API model, another local server) must not paint its rate here.
 *  The session side may arrive as a composite "provider/name" selector (the
 *  backend's canonical form, e.g. "embedded/Bonsai 2 27B"); bareModel strips
 *  the provider prefix before the normalized (trim + case-insensitive)
 *  comparison against the resident model's display name. */
export function isEmbeddedSessionModel(
  tokens: Pick<TokenInfo, 'model'> | null | undefined,
  status: EmbeddedLLMStatus,
): boolean {
  if (!tokens || tokens.model === '') return false
  const session = bareModel(tokens.model).trim().toLowerCase()
  if (session === '') return false
  return session === modelName(status).trim().toLowerCase()
}

/** The gated throughput metric for the resident surface: the session's median
 *  end-to-end output-token rate when it belongs to THIS model and rests on at
 *  least three per-call samples (below that a median is noise); null
 *  otherwise. A non-positive or absent median is treated as "no metric". */
export function sessionThroughput(
  status: EmbeddedLLMStatus,
  tokens: TokenInfo | null | undefined,
): { tokPerSec: number; samples: number } | null {
  if (!isEmbeddedSessionModel(tokens, status)) return null
  const median = tokens?.median_output_tok_s
  const samples = tokens?.tok_s_samples
  if (typeof median !== 'number' || !(median > 0)) return null
  if (typeof samples !== 'number' || samples < 3) return null
  return { tokPerSec: median, samples }
}

/** One decimal at most, trailing ".0" dropped — bar text, not a lab readout. */
function formatTokPerSec(v: number): string {
  return `${Math.round(v * 10) / 10}`
}

/** The resident indicator's tooltip: identity, endpoint, pid, the residency
 *  policy (the idle countdown itself is not rendered — see the file header)
 *  and, when the session's gated metric is available, what the visible rate
 *  actually measures. */
export function loadedTitle(
  status: EmbeddedLLMStatus,
  name: string,
  throughput?: { tokPerSec: number; samples: number } | null,
): string {
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
  if (throughput) {
    parts.push(`end-to-end per request · median of last ${throughput.samples} samples`)
  }
  return parts.join(' · ')
}

/** Pure state → surface derivation. Returns null when the block has nothing to
 *  say (and must therefore render no separator either).
 *
 *  `tokens` is the ACTIVE session's cached TokenInfo (chatStore.sessionTokens)
 *  or null — the only consumer of the throughput metric; when absent, gated
 *  off, or short of samples every surface renders exactly as it did before
 *  the metric existed. */
export function deriveView(
  status: EmbeddedLLMStatus | null,
  installing: boolean,
  progress: EmbeddedLLMProgressByComponent,
  tokens?: TokenInfo | null,
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
    const throughput = sessionThroughput(status, tokens)
    return {
      kind: 'loaded',
      name,
      tokPerSec: throughput !== null ? formatTokPerSec(throughput.tokPerSec) : null,
      tokSamples: throughput?.samples ?? 0,
      title: loadedTitle(status, name, throughput),
    }
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
