import { useState, useEffect, useCallback } from 'react'
import { AlertTriangle, X } from 'lucide-react'
import { CheckCodebaseMemoryMCP } from '../../wailsjs/go/desktop/App'
import { useSettingsStore } from '@/stores/settingsStore'
import { useWails } from '@/hooks/useWails'

interface CodeMemoryStatus {
  installed: boolean
  path: string
}

const DISMISSED_KEY = 'codememory-banner-dismissed'

export function CodebaseMemoryBanner() {
  const { runtime } = useWails()
  const [status, setStatus] = useState<CodeMemoryStatus | null>(null)
  const [dismissed, setDismissed] = useState(() => {
    // Use sessionStorage so it reappears on next app launch
    return sessionStorage.getItem(DISMISSED_KEY) === 'true'
  })
  const openSettings = useSettingsStore(s => s.openSettings)

  // Check MCP status on mount
  useEffect(() => {
    const checkStatus = async () => {
      try {
        const result = await CheckCodebaseMemoryMCP()
        setStatus({ installed: result.installed, path: result.path })
      } catch {
        // Silently fail - backend might not be ready
        setStatus({ installed: false, path: '' })
      }
    }
    checkStatus()
  }, [])

  // Listen for codememory:status events from backend
  useEffect(() => {
    if (!runtime) return

    const unsubscribe = runtime.EventsOn('codememory:status', (data: unknown) => {
      const statusData = data as CodeMemoryStatus
      setStatus(statusData)
    })

    return unsubscribe
  }, [runtime])

  const handleDismiss = useCallback(() => {
    setDismissed(true)
    sessionStorage.setItem(DISMISSED_KEY, 'true')
  }, [])

  const handleOpenSettings = useCallback(() => {
    openSettings('mcp')
  }, [openSettings])

  // Don't show if installed, status not loaded yet, or dismissed
  if (!status || status.installed || dismissed) {
    return null
  }

  return (
    <div className="bg-amber-500/95 text-amber-950 p-3 shadow-lg pointer-events-auto">
      <div className="max-w-4xl mx-auto flex items-start gap-3">
        <AlertTriangle className="h-5 w-5 mt-0.5 flex-shrink-0" />
        <div className="flex-1">
          <p className="font-semibold">Codebase Memory MCP is not installed</p>
          <p className="text-sm opacity-90">
            Without this extension, working with code will be significantly less efficient.{' '}
            <button
              onClick={handleOpenSettings}
              className="underline hover:no-underline font-medium"
            >
              Install it via MCP settings
            </button>{' '}
            in configuration.
          </p>
        </div>
        <button
          onClick={handleDismiss}
          className="p-1 hover:bg-amber-950/10 active:bg-amber-950/20 rounded transition-colors"
          aria-label="Dismiss"
        >
          <X className="h-4 w-4" />
        </button>
      </div>
    </div>
  )
}
