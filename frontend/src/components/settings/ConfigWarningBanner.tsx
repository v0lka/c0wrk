import { useState, useEffect } from 'react'
import { AlertTriangle, Info } from 'lucide-react'
import { GetConfig } from '../../../wailsjs/go/desktop/App'

interface ConfigWarningBannerProps {
  className?: string
  refreshKey?: number // Used to trigger a refresh when changed
}

export function ConfigWarningBanner({ className = '', refreshKey = 0 }: ConfigWarningBannerProps) {
  const [migrated, setMigrated] = useState(false)
  const [migrationMsg, setMigrationMsg] = useState('')
  const [errors, setErrors] = useState<string[]>([])
  const [loaded, setLoaded] = useState(false)

  useEffect(() => {
    const loadConfigState = async () => {
      try {
        const result = await GetConfig()
        setLoaded(result?.loaded ?? false)
        setMigrated(result?.config_migrated ?? false)
        setMigrationMsg(result?.config_migration_msg ?? '')
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

  // Show migration success message
  if (migrated && migrationMsg && errors.length === 0) {
    return (
      <div className={`flex items-start gap-2 p-3 rounded-md bg-blue-500/10 border border-blue-500/20 text-sm ${className}`}>
        <Info className="h-4 w-4 text-blue-500 flex-shrink-0 mt-0.5" />
        <div>
          <span className="font-medium text-blue-500">Config migrated: </span>
          <span className="text-muted-foreground">{migrationMsg}</span>
        </div>
      </div>
    )
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
                <li key={index}>{error}</li>
              ))}
            </ul>
          </div>
        </div>
        {migrationMsg && (
          <div className="text-xs text-muted-foreground ml-6">
            {migrationMsg}
          </div>
        )}
      </div>
    )
  }

  // No warnings
  return null
}
