import { useState, useEffect } from 'react'
import { AlertTriangle } from 'lucide-react'
import { getConfig } from '@/api/config'
import type { ConfigResponse } from '@/types/models'
import { compositeDefaultModelHint } from '@/lib/configLoadHint'

interface ConfigWarningBannerProps {
  className?: string
  refreshKey?: number // Used to trigger a refresh when changed
}

export function ConfigWarningBanner({ className = '', refreshKey = 0 }: ConfigWarningBannerProps) {
  const [errors, setErrors] = useState<string[]>([])
  const [llm, setLlm] = useState<ConfigResponse['llm'] | null>(null)
  const [loaded, setLoaded] = useState(false)

  useEffect(() => {
    const loadConfigState = async () => {
      try {
        const result = await getConfig()
        setLoaded(result?.loaded ?? false)
        setErrors(result?.config_errors ?? [])
        setLlm(result?.llm ?? null)
      } catch {
        // Silently fail - config not available yet
      }
    }
    loadConfigState()
  }, [refreshKey])

  if (!loaded) {
    return null
  }

  // The mandatory-composite default_model error (ADR-122) gets an actionable
  // hint: the exact corrected line, provider-resolved from the effective
  // (fail-soft fallback) config when any configured provider serves the bare
  // model, the "<provider>/bare" pattern otherwise.
  const hint = compositeDefaultModelHint(errors, llm)

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
            {hint && (
              <div className="flex flex-col gap-0.5 pl-5">
                <span className="text-muted-foreground">
                  The default model must be a full{' '}
                  <code className="text-foreground">provider/model</code> selector — fix{' '}
                  <code className="text-foreground">~/.c0wrk/config.yaml</code>:
                </span>
                {hint.suggestions.length > 0 ? (
                  hint.suggestions.map((suggestion) => (
                    <code key={suggestion} className="text-foreground">
                      default_model: {suggestion}
                    </code>
                  ))
                ) : (
                  <code className="text-foreground">
                    default_model: &lt;provider&gt;/{hint.bareModel}
                  </code>
                )}
              </div>
            )}
          </div>
        </div>
      </div>
    )
  }

  // No warnings
  return null
}
