import { useEffect, useState, useCallback } from 'react'
import { AppLayout } from '@/components/layout/AppLayout'
import { TooltipProvider } from '@/components/ui/tooltip'
import { subscribe } from '@/api/runtime'
import { AlertCircle, X } from 'lucide-react'
import { CodebaseMemoryBanner } from '@/components/CodebaseMemoryBanner'
import { RtkBanner } from '@/components/RtkBanner'
import { SettingsModal } from '@/components/settings/SettingsModal'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { useProjectLoader } from '@/hooks/useProjectLoader'
import { useSessionLoader } from '@/hooks/useSessionLoader'
import { useSessionEvents } from '@/hooks/useSessionEvents'
import { useSessionStore } from '@/stores/sessionStore'
import { isStartupError, isVectorIndexPayload, type StartupError } from '@/types/events'

function App() {
  const [startupError, setStartupError] = useState<StartupError | null>(null)
  const activeSessionId = useSessionStore(s => s.activeSessionId)

  // Wire loaders
  useProjectLoader()
  useSessionLoader()
  useSessionEvents(activeSessionId)

  // Listen for startup errors from the backend
  useEffect(() => {
    return subscribe('startup_error', (data: unknown) => {
      if (!isStartupError(data)) return
      setStartupError(data)
    })
  }, [])

  // Listen for vector index status (non-session-scoped)
  useEffect(() => {
    return subscribe('vector_index:status', (data: unknown) => {
      if (!isVectorIndexPayload(data)) return
      useVectorIndexStore.getState().setStatus(data)
    })
  }, [])

  const dismissStartupError = useCallback(() => {
    setStartupError(null)
  }, [])

  return (
    <TooltipProvider>
      <div className="fixed top-0 left-0 right-0 z-50 flex flex-col pointer-events-none">
        <div className="pointer-events-auto">
          <CodebaseMemoryBanner />
        </div>
        <div className="pointer-events-auto">
          <RtkBanner />
        </div>
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
              >
                <X className="h-4 w-4" />
              </button>
            </div>
          </div>
        )}
      </div>
      <AppLayout />
      <SettingsModal />
    </TooltipProvider>
  )
}

export default App
