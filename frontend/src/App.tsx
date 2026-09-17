import { useEffect, useState, useCallback } from 'react'
import { AppLayout } from '@/components/layout/AppLayout'
import { TooltipProvider } from '@/components/ui/tooltip'
import { subscribe } from '@/api/runtime'
import { listProjects } from '@/api/projects'
import { AlertCircle, X } from 'lucide-react'
import { SettingsModal } from '@/components/settings/SettingsModal'
import { WorkDirsModal } from '@/components/chat/WorkDirsModal'
import { CreateProjectDialog } from '@/components/project/CreateProjectDialog'
import { ToolInstallSplash } from '@/components/ToolInstallSplash'
import { UpdateToast } from '@/components/UpdateToast'
import { GitConfigRiskToast } from '@/components/GitConfigRiskToast'
import { ExitConfirmDialog } from '@/components/ExitConfirmDialog'
import { useUpdateChecker } from '@/hooks/useUpdateChecker'
import { useExitGuard } from '@/hooks/useExitGuard'
import { useGitFocusRefresh } from '@/hooks/useGitFocusRefresh'
import { useVectorIndexStatus } from '@/hooks/useVectorIndexStatus'
import { useProjectLoader } from '@/hooks/useProjectLoader'
import { useSessionLoader } from '@/hooks/useSessionLoader'
import { useSessionEvents } from '@/hooks/useSessionEvents'
import { useBackgroundSessionWatcher } from '@/hooks/useBackgroundSessionWatcher'
import { useWindowTitle } from '@/hooks/useWindowTitle'
import { useActiveSessionsRefresh } from '@/stores/activeSessionsStore'
import { initSoundUnlock } from '@/lib/sound'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSettingsStore } from '@/stores/settingsStore'
import { useWorkDirsStore } from '@/stores/workDirsStore'
import { useThemeStore } from '@/stores/themeStore'
import { ResearchEventBridge } from '@/components/research/ResearchEventBridge'
import type { ToolManagerToolInfo, ToolManagerProgressData } from '@/types/events'
import { isStartupError, isRuntimeError, isToolManagerStartData, isToolManagerProgressData } from '@/types/events'
import type { StartupError, RuntimeError } from '@/types/events'

type AppPhase = 'splash' | 'waiting_ready' | 'main'

/**
 * Create Project dialog mounted at the App root (outside the Sidebar) so it
 * renders even when the sidebar is collapsed. The sidebar early-returns a
 * minimal collapsed view and does not mount SidebarHeader, so the dialog must
 * not live there — otherwise the CODE-first startup auto-open
 * (useProjectLoader) would set createDialogOpen=true with no dialog rendered,
 * leaving the user stuck. See code-review Issue #1.
 */
function CreateProjectDialogAlwaysMounted() {
  const createDialogOpen = useProjectStore((s) => s.createDialogOpen)
  const setCreateProjectDialogOpen = useProjectStore((s) => s.setCreateProjectDialogOpen)
  return <CreateProjectDialog open={createDialogOpen} onOpenChange={setCreateProjectDialogOpen} />
}

function App() {
  const [runtimeError, setRuntimeError] = useState<RuntimeError | null>(null)
  const [startupError, setStartupError] = useState<StartupError | null>(null)
  const [phase, setPhase] = useState<AppPhase>('splash')
  const [splashTools, setSplashTools] = useState<readonly ToolManagerToolInfo[] | null>(null)
  const [progressMap, setProgressMap] = useState<Map<string, ToolManagerProgressData>>(new Map())
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const activeProjectId = useProjectStore(s => s.activeProjectId)

  // Register the persistent gesture/visibility audio-unlock listeners ONCE at
  // app start. This must NOT live in useSoundEvents: that hook only runs when a
  // session is active, yet the webview can leave the AudioContext suspended
  // with no active session (e.g. immediately after a reload) and the only way
  // to wake it is a gesture/visibility event. initSoundUnlock is idempotent
  // (guarded internally), so a StrictMode double-mount still installs exactly
  // one set of listeners.
  useEffect(() => {
    initSoundUnlock()
  }, [])

  // Clear stale work-directory data on project/session switch so the modal
  // doesn't briefly show entries from a previous context before loadAll runs.
  useEffect(() => {
    useWorkDirsStore.getState().clear()
  }, [activeProjectId, activeSessionId])

  // Wire loaders — always run regardless of phase.
  useProjectLoader()
  useSessionLoader()
  // Seed/refresh the vector-index status (status bar pill + search panel) —
  // the push events alone leave it stale on startup (see the hook).
  useVectorIndexStatus()
  useSessionEvents(activeSessionId)
  useBackgroundSessionWatcher()
  // Authoritative live-sessions snapshot loader. Mounted at the App root — NOT
  // in the sidebar header (ActiveSessionsIndicator) — because the header
  // unmounts when the sidebar is collapsed, which would freeze the snapshot
  // exactly when it is needed most: announcing completions of sessions the
  // current chatStore never followed (a webview reload empties the live maps)
  // or that live in a different project. `activeSessionsStore.sessions` stays
  // fresh app-wide; the indicator is now a pure consumer of that store.
  useActiveSessionsRefresh()
  useUpdateChecker()
  // Native window title (c0wrk - Project: Session) — mounted at the root so
  // the title tracks the active context in every app phase.
  useWindowTitle()
  // Fetch-on-focus (git auto-fetch, window-focus trigger): the hook arms
  // its listener only after runtime readiness and defers every gate to the
  // backend (RequestGitRemoteRefresh / autoFetchOnce), so mounting it here
  // unconditionally is safe in every phase.
  useGitFocusRefresh()
  // Close-guard subscription — mounted once at the root so app-phase
  // transitions never create an event gap; ExitConfirmDialog (rendered in
  // every phase branch) is a pure view over the store this hook writes.
  useExitGuard()

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

  // ── Theme catalog: load custom themes once the backend is reachable ──
  // Same startup pattern as the listProjects safety net above: try the RPC
  // on mount; if the backend is not ready yet the call fails and backend:ready
  // (subscribed above) re-triggers it when the backend comes up. The store
  // applies any custom theme from its persisted cache already (pre-paint in
  // main.tsx); this fetch only refreshes the descriptor catalog (and rolls
  // the active theme back to Default Dark if it disappeared on disk).
  useEffect(() => {
    const off = subscribe('backend:ready', () => {
      void useThemeStore.getState().loadThemes()
    })
    void useThemeStore.getState().loadThemes()
    return off
  }, [])

  // Listen for runtime errors from the backend (e.g. git missing for CODE mode,
  // or a project switch that could not acquire the backend switch lock)
  useEffect(() => {
    return subscribe('runtime_error', (data: unknown) => {
      if (!isRuntimeError(data)) return
      setRuntimeError(data)
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
    return (
      <>
        <ToolInstallSplash tools={splashTools} progressMap={progressMap} />
        {/* Mounted in every phase: a webview reload (wake recovery) can land
            back in splash while sessions keep running in the backend, and the
            close guard's confirmation modal must still be renderable then. */}
        <ExitConfirmDialog />
      </>
    )
  }

  // Phase: splash but no tool_manager:start received yet — show minimal spinner.
  if (phase === 'splash') {
    return (
      <>
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-background">
          <div className="size-8 animate-spin rounded-full border-2 border-primary border-t-transparent" />
        </div>
        <ExitConfirmDialog />
      </>
    )
  }

  // Phase: waiting_ready — tools done, waiting for backend.
  if (phase === 'waiting_ready') {
    return (
      <>
        <div className="fixed inset-0 z-50 flex flex-col items-center justify-center gap-4 bg-background">
          <div className="size-8 animate-spin rounded-full border-2 border-primary border-t-transparent" />
          <p className="text-sm text-muted-foreground">Starting c0wrk…</p>
        </div>
        <ExitConfirmDialog />
      </>
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
      <CreateProjectDialogAlwaysMounted />
      <UpdateToast />
      <GitConfigRiskToast />
      {/* Research store sync (status + incremental graph updates), mounted
          once here — so ResearchPanel and ResearchWorkspace stay pure views
          and never double-mount the watchdog/full-refresh paths. */}
      <ResearchEventBridge />
      <ExitConfirmDialog />
    </TooltipProvider>
  )
}

export default App
