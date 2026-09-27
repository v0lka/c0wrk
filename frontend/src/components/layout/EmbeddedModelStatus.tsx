// Status-bar indicator for the embedded local model (Bonsai 2 27B).
//
// The always-visible counterpart of the Settings block
// (`components/settings/EmbeddedLLMSettings.tsx`): a multi-gigabyte install and
// a multi-minute weight load both outlive the settings dialog, and a resident
// model occupies gigabytes of RAM the user should be able to see at a glance
// (see specs/domains/embedded-llm.md § Status-bar indicator).
//
// Like `ProcessMemoryStatus`, this block owns its LEADING separator and returns
// null — separator included — whenever it has nothing to say. Which surface it
// shows, and the exact words of each tooltip, are a pure derivation in
// lib/embeddedModelView (unit-tested there); the bar itself is
// EmbeddedModelProgressBar. This file keeps the subscription, the memo and the
// markup only.
//
// Zoom-safety: fixed layout-px sizes and percentages only — no viewport unit,
// no `*-screen` utility, no pointer-anchored placement.

import { useEffect, useMemo } from 'react'
import { AlertCircle, Cpu, Download, Loader2 } from 'lucide-react'
import { Separator } from '@/components/ui/separator'
import { cn } from '@/lib/utils'
import { deriveView } from '@/lib/embeddedModelView'
import { EmbeddedModelProgressBar } from './EmbeddedModelProgressBar'
import { subscribe } from '@/api/runtime'
import {
  refreshEmbeddedLLMStatus,
  subscribeEmbeddedLLMEvents,
  useEmbeddedLLMInstalling,
  useEmbeddedLLMProgress,
  useEmbeddedLLMStatus,
} from '@/stores/embeddedLLMStore'
import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'

export function EmbeddedModelStatus() {
  const status = useEmbeddedLLMStatus()
  const installing = useEmbeddedLLMInstalling()
  const progress = useEmbeddedLLMProgress()
  // The active session's cached token info carries the (gated) throughput
  // metric the resident surface may append. Both selectors return a stable
  // reference — the active id (primitive) or the store's own TokenInfo object
  // / undefined — never an allocation.
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const tokens = useChatStore(s => (activeSessionId ? s.sessionTokens[activeSessionId] : undefined))

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
  // store reference, so this rebuilds only when the snapshot, a progress
  // payload or the active session's token info actually changed.
  const view = useMemo(
    () => deriveView(status, installing, progress, tokens),
    [status, installing, progress, tokens],
  )

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
            <EmbeddedModelProgressBar
              percent={view.percent}
              label={`Embedded model install: ${view.label ?? 'preparing'}`}
            />
            {view.percent !== null && (
              <span className="w-8 shrink-0 text-right tabular-nums">{view.percent}%</span>
            )}
          </>
        )}

        {view.kind === 'loading' && (
          <>
            <Loader2 className="size-3 shrink-0 animate-spin text-info" aria-hidden="true" />
            <span className="shrink-0">Loading model…</span>
            <EmbeddedModelProgressBar percent={null} label="Embedded model load" />
          </>
        )}

        {view.kind === 'loaded' && (
          <>
            <Cpu className="size-3 shrink-0" aria-hidden="true" />
            <span className="min-w-0 max-w-[160px] truncate">{view.name}</span>
            {view.tokPerSec !== null && (
              // Text-only, fixed-size layout px — zoom-safe by construction
              // (see the zoom-safety note in the file header). Gated by the
              // pure derivation: absent metric, foreign-model session or
              // fewer than three samples render nothing here at all.
              <span className="shrink-0 tabular-nums">· {view.tokPerSec} tok/s</span>
            )}
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
