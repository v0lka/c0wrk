// Status-bar indicator for the embedded local model (Bonsai 2 27B).
//
// The always-visible counterpart of the Settings block
// (`components/settings/EmbeddedLLMSettings.tsx`): a multi-gigabyte install and
// a multi-minute weight load both outlive the settings dialog, and a resident
// model occupies gigabytes of RAM the user should be able to see at a glance
// (see specs/domains/embedded-llm.md § Status-bar indicator).
//
// Like `ProcessMemoryStatus`, this block owns its LEADING separator and returns
// null — separator included — whenever it has nothing to say, so a hidden
// indicator never leaves a stray separator in the bar. "Nothing to say" is
// exactly: not installed with no pending failure, and installed-but-stopped
// (an idle install is not an event; the Settings block is where it is acted
// on).
//
// Surfaces, one per state:
//   installing → the ACTIVE artifact's own bar and percent (never an aggregate:
//                the components have wildly different sizes, so a summed
//                percent would jump backwards when the 6.7 GiB weights start
//                after the 100 MB runtime finished). A byte-less stage
//                (verifying / extracting / signing) renders an indeterminate
//                pulse, because a percentage there would be a lie.
//   loading    → an indeterminate bar: `LoadEmbeddedLLM` reports no fraction,
//                only the state.
//   loaded     → the resident indicator, which stays until the model is
//                unloaded — manually or by the idle timer.
//   error      → an explicit hint carrying the backend's message.
//
// The idle countdown is deliberately NOT rendered: `idle_remaining_seconds` is
// a snapshot field that only refreshes on a transition, so a ticking number
// derived from it would be wrong within a second. The tooltip states the
// policy instead.
//
// Zoom-safety: fixed layout-px sizes and percentages only — no viewport unit,
// no `*-screen` utility, no pointer-anchored placement.

import { useEffect, useMemo } from 'react'
import { AlertCircle, Cpu, Download, Loader2 } from 'lucide-react'
import { Separator } from '@/components/ui/separator'
import { cn } from '@/lib/utils'
import { formatBytes } from '@/lib/formatters'
import { bareModel } from '@/lib/modelId'
import {
  EMBEDDED_COMPONENT_ORDER,
  embeddedComponentLabel,
  embeddedStageLabel,
} from '@/lib/embeddedLLMLabels'
import { subscribe } from '@/api/runtime'
import {
  refreshEmbeddedLLMStatus,
  subscribeEmbeddedLLMEvents,
  useEmbeddedLLMInstalling,
  useEmbeddedLLMProgress,
  useEmbeddedLLMStatus,
  type EmbeddedLLMProgressByComponent,
  type EmbeddedLLMStatus,
} from '@/stores/embeddedLLMStore'
import type { EmbeddedLLMComponent, EmbeddedLLMInstallProgressData } from '@/types/events'

/** The artifact whose transfer is currently in flight, in install order — the
 *  first reported component that has not reached `done`. Null before the first
 *  progress event and while the install finalizes (manifest + config sink). */
function activeProgress(
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
type EmbeddedModelView =
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
function modelName(status: EmbeddedLLMStatus): string {
  return status.model_name || bareModel(status.model_id) || 'Embedded model'
}

function loadedTitle(status: EmbeddedLLMStatus, name: string): string {
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
function deriveView(
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

/** The status bar's own compact bar: 64 layout-px wide, so the block's width
 *  stays stable while the percent changes. */
function Bar({ percent, label }: { percent: number | null; label: string }) {
  return (
    <span
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      {...(percent === null ? {} : { 'aria-valuenow': percent })}
      data-testid="embedded-model-progress"
      className="h-1.5 w-16 shrink-0 overflow-hidden rounded-full bg-muted"
    >
      {percent === null ? (
        <span className="block h-full w-1/3 animate-pulse rounded-full bg-primary" />
      ) : (
        <span
          className="block h-full rounded-full bg-primary transition-all duration-150"
          style={{ width: `${percent}%` }}
        />
      )}
    </span>
  )
}

export function EmbeddedModelStatus() {
  const status = useEmbeddedLLMStatus()
  const installing = useEmbeddedLLMInstalling()
  const progress = useEmbeddedLLMProgress()

  // Lifecycle: the ONE shared, refcounted event subscription (so mounting
  // alongside the Settings block never applies an event twice) plus the
  // authoritative read. The startup snapshot event is emitted in desktop
  // startup phase 5 and can precede the first paint, so the read is retried on
  // `backend:ready` — the same ordering race `useExperimentalFeatures` guards.
  // Nothing here installs, loads, unloads or probes.
  useEffect(() => {
    const unsubscribe = subscribeEmbeddedLLMEvents()
    void refreshEmbeddedLLMStatus()
    const offReady = subscribe('backend:ready', () => {
      void refreshEmbeddedLLMStatus()
    })
    return () => {
      offReady()
      unsubscribe()
    }
  }, [])

  // Derived here, never stored: every input is a stable primitive or a direct
  // store reference, so this rebuilds only when the snapshot or a progress
  // payload actually changed.
  const view = useMemo(() => deriveView(status, installing, progress), [status, installing, progress])

  if (view === null) return null

  return (
    <>
      <Separator orientation="vertical" className="mx-1 h-4" />
      <span
        className={cn(
          'flex min-w-0 shrink-0 items-center gap-1 text-xs',
          view.kind === 'loaded' && 'text-success',
          view.kind === 'error' && 'text-destructive',
          (view.kind === 'install' || view.kind === 'loading') && 'text-muted-foreground',
        )}
        title={view.title}
        data-testid="embedded-model-status"
        data-state={view.kind}
      >
        {view.kind === 'install' && (
          <>
            <Download className="size-3 shrink-0 animate-pulse text-primary" aria-hidden="true" />
            <span className="shrink-0">{view.label ?? 'Installing…'}</span>
            <Bar percent={view.percent} label={`Embedded model install: ${view.label ?? 'preparing'}`} />
            {view.percent !== null && (
              <span className="w-8 shrink-0 text-right tabular-nums">{view.percent}%</span>
            )}
          </>
        )}

        {view.kind === 'loading' && (
          <>
            <Loader2 className="size-3 shrink-0 animate-spin text-info" aria-hidden="true" />
            <span className="shrink-0">Loading model…</span>
            <Bar percent={null} label="Embedded model load" />
          </>
        )}

        {view.kind === 'loaded' && (
          <>
            <Cpu className="size-3 shrink-0" aria-hidden="true" />
            <span className="min-w-0 max-w-[160px] truncate">{view.name}</span>
          </>
        )}

        {view.kind === 'error' && (
          <>
            <AlertCircle className="size-3 shrink-0" aria-hidden="true" />
            <span className="min-w-0 max-w-[200px] truncate">{view.message}</span>
          </>
        )}
      </span>
    </>
  )
}
