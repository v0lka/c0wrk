import { useEffect, useState } from 'react'
import { AppLayout } from '@/components/layout/AppLayout'
import { useThemeEffect } from '@/stores/uiStore'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useWails } from '@/hooks/useWails'
import { AlertCircle, X } from 'lucide-react'
import { CodebaseMemoryBanner } from '@/components/CodebaseMemoryBanner'
import { RtkBanner } from '@/components/RtkBanner'

interface StartupError {
  message: string
  error: string
}

function App() {
  useThemeEffect()
  const { runtime } = useWails()
  const [startupError, setStartupError] = useState<StartupError | null>(null)

  // Listen for startup errors from the backend
  useEffect(() => {
    if (!runtime) return

    const unsubscribe = runtime.EventsOn('startup_error', (data: unknown) => {
      const errorData = data as StartupError
      setStartupError(errorData)
    })

    return unsubscribe
  }, [runtime])

  const dismissStartupError = () => {
    setStartupError(null)
  }

  return (
    <TooltipProvider>
      <div className="fixed top-0 left-0 right-0 z-50 flex flex-col pointer-events-none">
        <CodebaseMemoryBanner />
        <RtkBanner />
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
    </TooltipProvider>
  )
}

export default App
