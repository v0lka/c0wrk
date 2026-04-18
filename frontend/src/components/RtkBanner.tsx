import { useState, useEffect, useCallback } from 'react'
import { AlertTriangle, X } from 'lucide-react'
import { CheckRtk } from '../../wailsjs/go/desktop/App'
import { useSettingsStore } from '@/stores/settingsStore'
import { useWails } from '@/hooks/useWails'

interface RtkStatus {
  installed: boolean
  path: string
  version: string
}

function isRtkStatus(data: unknown): data is RtkStatus {
  return (
    typeof data === 'object' &&
    data !== null &&
    'installed' in data &&
    typeof (data as Record<string, unknown>).installed === 'boolean'
  )
}

const DISMISSED_KEY = 'rtk-banner-dismissed'

export function RtkBanner() {
  const { runtime } = useWails()
  const [status, setStatus] = useState<RtkStatus | null>(null)
  const [dismissed, setDismissed] = useState(() => {
    // Use sessionStorage so it reappears on next app launch
    return sessionStorage.getItem(DISMISSED_KEY) === 'true'
  })
  const openSettings = useSettingsStore(s => s.openSettings)

  // Check RTK status on mount
  useEffect(() => {
    const checkStatus = async () => {
      try {
        const result = await CheckRtk()
        setStatus({ installed: result.installed, path: result.path, version: result.version })
      } catch {
        // Silently fail - backend might not be ready
        setStatus({ installed: false, path: '', version: '' })
      }
    }
    checkStatus()
  }, [])

  // Listen for rtk:status events from backend
  useEffect(() => {
    if (!runtime) return

    const unsubscribe = runtime.EventsOn('rtk:status', (data: unknown) => {
      if (isRtkStatus(data)) {
        setStatus(data)
      }
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
          <p className="font-semibold">RTK is not installed</p>
          <p className="text-sm opacity-90">
            Without RTK, command output will use more tokens.{' '}
            <button
              onClick={handleOpenSettings}
              className="underline hover:no-underline font-medium"
            >
              Install it via MCP settings
            </button>{' '}
            for optimized output and 60-90% token savings.
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
