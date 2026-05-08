import { useState, useEffect } from 'react'
import { AlertTriangle } from 'lucide-react'
import { getConfig } from '@/api/config'

interface ConfigWarningBannerProps {
  className?: string
  refreshKey?: number // Used to trigger a refresh when changed
}

export function ConfigWarningBanner({ className = '', refreshKey = 0 }: ConfigWarningBannerProps) {
  const [errors, setErrors] = useState<string[]>([])
  const [loaded, setLoaded] = useState(false)

  useEffect(() => {
    const loadConfigState = async () => {
      try {
        const result = await getConfig()
        setLoaded(result?.loaded ?? false)
        setErrors(result?.config_errors ?? [])
      } catch {
        // Silently fail - config not available yet
      }
    }
    loadConfigState()
  }, [refreshKey])

  if (!loaded) {
    return null
  }

  // Show errors if present
  if (errors.length > 0) {
    return (
      <div className={`flex flex-col gap-2 p-3 rounded-md bg-destructive/10 border border-destructive/20 text-sm ${className}`}>
        <div className="flex items-start gap-2">
          <AlertTriangle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
          <div className="flex flex-col gap-1">
            <span className="font-medium text-destructive">Configuration warning</span>
            <ul className="text-muted-foreground list-disc list-inside">
              {errors.map((error, index) => (
                <li key={`${index}-${error}`}>{error}</li>
              ))}
            </ul>
          </div>
        </div>
      </div>
    )
  }

  // No warnings
  return null
}
