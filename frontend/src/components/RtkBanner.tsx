import { useState, useEffect, useCallback } from 'react'
import { checkRtk, installRtk } from '@/api/mcp'
import { AlertTriangle, X, Download } from 'lucide-react'
import { Button } from '@/components/ui/button'

const DISMISS_KEY = 'c0wrk-rtk-dismissed'

export function RtkBanner() {
  const [show, setShow] = useState(false)
  const [installing, setInstalling] = useState(false)

  useEffect(() => {
    if (sessionStorage.getItem(DISMISS_KEY)) return
    checkRtk()
      .then((status) => {
        if (!status.installed) setShow(true)
      })
      .catch(() => { /* ignore */ })
  }, [])

  const handleInstall = useCallback(async () => {
    setInstalling(true)
    try {
      await installRtk()
      setShow(false)
    } catch {
      // error handled by API layer
    } finally {
      setInstalling(false)
    }
  }, [])

  const handleDismiss = useCallback(() => {
    sessionStorage.setItem(DISMISS_KEY, '1')
    setShow(false)
  }, [])

  if (!show) return null

  return (
    <div className="flex items-center gap-2 border-b border-warning/30 bg-warning/10 px-3 py-1.5 text-xs text-warning">
      <AlertTriangle className="size-3.5 shrink-0" />
      <span className="flex-1">Runtime Toolkit is not installed. Install it for enhanced tool capabilities.</span>
      <Button variant="ghost" size="xs" onClick={handleInstall} disabled={installing} className="gap-1">
        <Download className="size-3" />
        {installing ? 'Installing...' : 'Install'}
      </Button>
      <button onClick={handleDismiss} className="text-warning/60 hover:text-warning" aria-label="Dismiss">
        <X className="size-3.5" />
      </button>
    </div>
  )
}
