import { useEffect, useState, useCallback } from 'react'
import { AppLayout } from '@/components/layout/AppLayout'
import { TooltipProvider } from '@/components/ui/tooltip'
import { subscribe } from '@/api/runtime'
import { listProjects } from '@/api/projects'
import { AlertCircle, X } from 'lucide-react'
import { SettingsModal } from '@/components/settings/SettingsModal'
import { WorkDirsModal } from '@/components/chat/WorkDirsModal'
import { ToolInstallSplash } from '@/components/ToolInstallSplash'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { useProjectLoader } from '@/hooks/useProjectLoader'
import { useSessionLoader } from '@/hooks/useSessionLoader'
import { useSessionEvents } from '@/hooks/useSessionEvents'
import { useBackgroundSessionWatcher } from '@/hooks/useBackgroundSessionWatcher'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSettingsStore } from '@/stores/settingsStore'
import { useWorkDirsStore } from '@/stores/workDirsStore'
import type { ToolManagerToolInfo, ToolManagerProgressData } from '@/types/events'
import { isStartupError, isRuntimeError, isVectorIndexPayload, isToolManagerStartData, isToolManagerProgressData } from '@/types/events'
import type { StartupError, RuntimeError } from '@/types/events'

type AppPhase = 'splash' | 'waiting_ready' | 'main'

function App() {
  const [runtimeError, setRuntimeError] = useState<RuntimeError | null>(null)
  const [startupError, setStartupError] = useState<StartupError | null>(null)
  const [phase, setPhase] = useState<AppPhase>('splash')
  const [splashTools, setSplashTools] = useState<readonly ToolManagerToolInfo[] | null>(null)
  const [progressMap, setProgressMap] = useState<Map<string, ToolManagerProgressData>>(new Map())
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const activeProjectId = useProjectStore(s => s.activeProjectId)

  // Clear stale work-directory data on project/session switch so the modal
  // doesn't briefly show entries from a previous context before loadAll runs.
  useEffect(() => {
    useWorkDirsStore.getState().clear()
  }, [activeProjectId, activeSessionId])

  // Wire loaders — always run regardless of phase.
  useProjectLoader()
  useSessionLoader()
  useSessionEvents(activeSessionId)
  useBackgroundSessionWatcher()

  // ── Tool manager lifecycle ────────────────────────────────────────────

  // Listen for tool_manager:start → show splash with tool list.
  useEffect(() => {
    return subscribe('tool_manager:start', (data: unknown) => {
      if (!isToolManagerStartData(data)) return
      setSplashTools(data.tools)
      setPhase('splash')
    })
  }, [])

  // Listen for tool_manager:progress — subscribed at App level so it's
  // active before ToolInstallSplash mounts, preventing lost events.
  // Also auto-derives the tool list if tool_manager:start was lost for any
  // reason (e.g. window-visibility race with Wails event delivery).
  useEffect(() => {
    return subscribe('tool_manager:progress', (data: unknown) => {
      if (!isToolManagerProgressData(data)) return
      setProgressMap(prev => {
        const next = new Map(prev)
        next.set(data.tool, data)
        return next
      })
      // If we got progress but tool_manager:start was lost, auto-populate
      // the tool list from the progress map so the splash renders.
      setSplashTools(prev => {
        // If we already have the proper list from tool_manager:start
        // (tools have real version strings), keep it.
        if (prev !== null && prev.length > 0 && prev[0]?.version !== '') return prev
        // Build incrementally from progress events; deduplicate by name.
        const existing = new Set((prev ?? []).map(t => t.name))
        if (existing.has(data.tool)) return (prev ?? []) as readonly ToolManagerToolInfo[]
        return [...(prev ?? []), { name: data.tool, version: '' }] as readonly ToolManagerToolInfo[]
      })
    })
  }, [])

  // Listen for tool_manager:done → transition to waiting_ready.
  // Guard against out-of-order events: if backend:ready already transitioned
  // to main, don't revert to waiting_ready.
  useEffect(() => {
    return subscribe('tool_manager:done', () => {
      setPhase(prev => prev === 'splash' ? 'waiting_ready' : prev)
    })
  }, [])

  // Listen for startup errors from the backend
  useEffect(() => {
    return subscribe('startup_error', (data: unknown) => {
      if (!isStartupError(data)) return
      // When LLM is not configured on first start, open the settings dialog on
      // the LLM tab instead of surfacing the top startup-error banner — the
      // dialog itself guides the user to fix the configuration.
      if (data.error_code === 'missing_default_model') {
        const settingsState = useSettingsStore.getState()
        if (!settingsState.open) {
          settingsState.openSettings('llm')
        }
        return
      }
      setStartupError(data)
    })
  }, [])

  // ── backend:ready — transition to main ────────────────────────────────

  useEffect(() => {
    return subscribe('backend:ready', () => {
      setPhase(prev => {
        // splash → main: no tools needed at all
        // waiting_ready → main: normal transition after tools done
        if (prev === 'splash' || prev === 'waiting_ready') return 'main'
        return prev
      })
    })
  }, [])

  // ── Safety net: try RPC on mount to detect already-ready backend ─────
  // If the backend completed startup before the frontend subscribed to
  // events, backend:ready would have been missed. An RPC that succeeds
  // proves the backend is wired; transition to main immediately.
  // Guarded: only acts when no tools are being installed (splashTools
  // is null), to avoid transitioning past the tool-install splash.
  useEffect(() => {
    let cancelled = false
    listProjects().then(() => {
      if (cancelled) return
      setSplashTools(prev => {
        // Only transition if we didn't receive tool_manager:start
        // (i.e. no tools are being installed).
        if (prev === null) {
          setPhase(p => (p === 'splash' || p === 'waiting_ready') ? 'main' : p)
        }
        return prev
      })
    }).catch(() => {
      // Backend not ready yet — the backend:ready event will handle it.
    })
    return () => { cancelled = true }
  }, [])

  // Listen for runtime errors from the backend (e.g. git missing for CODE mode)
  useEffect(() => {
    return subscribe('runtime_error', (data: unknown) => {
      if (!isRuntimeError(data)) return
      setRuntimeError(data)
    })
  }, [])

  // Listen for vector index status (non-session-scoped)
  useEffect(() => {
    return subscribe('vector_index:status', (data: unknown) => {
      if (!isVectorIndexPayload(data)) return
      useVectorIndexStore.getState().setStatus(data)
    })
  }, [])

  const dismissRuntimeError = useCallback(() => {
    setRuntimeError(null)
  }, [])

  const dismissStartupError = useCallback(() => {
    setStartupError(null)
  }, [])

  // ── Render ─────────────────────────────────────────────────────────────

  // Phase: splash (tool install in progress).
  if (phase === 'splash' && splashTools !== null) {
    return <ToolInstallSplash tools={splashTools} progressMap={progressMap} />
  }

  // Phase: splash but no tool_manager:start received yet — show minimal spinner.
  if (phase === 'splash') {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-background">
        <div className="size-8 animate-spin rounded-full border-2 border-primary border-t-transparent" />
      </div>
    )
  }

  // Phase: waiting_ready — tools done, waiting for backend.
  if (phase === 'waiting_ready') {
    return (
      <div className="fixed inset-0 z-50 flex flex-col items-center justify-center gap-4 bg-background">
        <div className="size-8 animate-spin rounded-full border-2 border-primary border-t-transparent" />
        <p className="text-sm text-muted-foreground">Starting c0wrk…</p>
      </div>
    )
  }

  // Phase: main — normal app rendering.
  return (
    <TooltipProvider>
      <div className="fixed top-0 left-0 right-0 z-50 flex flex-col pointer-events-none">
        {startupError && (
          <div className="bg-destructive/95 text-destructive-foreground p-4 shadow-lg pointer-events-auto">
            <div className="max-w-4xl mx-auto flex items-start gap-3">
              <AlertCircle className="h-5 w-5 mt-0.5 flex-shrink-0" />
              <div className="flex-1">
                <p className="font-semibold">Startup Error</p>
                <p className="text-sm opacity-90">{startupError.message}</p>
                <p className="text-xs opacity-75 mt-1 font-mono">{startupError.error}</p>
              </div>
              <button
                onClick={dismissStartupError}
                className="p-1 hover:bg-destructive-foreground/10 active:bg-destructive-foreground/20 rounded transition-colors"
                aria-label="Dismiss"
              >
                <X className="h-4 w-4" />
              </button>
            </div>
          </div>
        )}
        {runtimeError && (
          <div className="bg-destructive/90 text-destructive-foreground px-4 py-3 shadow-lg pointer-events-auto animate-in fade-in duration-300">
            <div className="max-w-4xl mx-auto flex items-center gap-3">
              <AlertCircle className="h-5 w-5 flex-shrink-0" />
              <p className="flex-1 text-sm font-medium">{runtimeError.message}</p>
              <button
                onClick={dismissRuntimeError}
                className="p-1 hover:bg-destructive-foreground/10 active:bg-destructive-foreground/20 rounded transition-colors"
                aria-label="Dismiss"
              >
                <X className="h-4 w-4" />
              </button>
            </div>
          </div>
        )}
      </div>
      <AppLayout />
      <SettingsModal />
      <WorkDirsModal />
    </TooltipProvider>
  )
}

export default App
