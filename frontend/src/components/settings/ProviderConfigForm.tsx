import { useState } from 'react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Loader2 } from 'lucide-react'
import { isOpenAICompatibleProvider } from '@/lib/llm-providers'
import { getProviderTLSCertificate } from '@/api/config'
import { logger } from '@/lib/logger'

interface ProviderConfig {
  api_key: string
  base_url: string
  /** Enabled models (present on the shared ProviderConfig shape the
   *  accordion passes through; unused by this form). */
  models?: string[]
  /** Per-provider TLS pin override (ADR-050): '' = standard verification. */
  tls_fingerprint: string
}

interface ProviderConfigFormProps {
  activeProvider: string
  config: ProviderConfig
  apiKeyDirty: boolean
  hasRequiredCredentials: boolean
  modelsLoading: boolean
  onConfigChange: (updates: Partial<ProviderConfig>) => void
  onApply: () => void
}

export function ProviderConfigForm({
  activeProvider,
  config,
  apiKeyDirty,
  hasRequiredCredentials,
  modelsLoading,
  onConfigChange,
  onApply,
}: ProviderConfigFormProps) {
  const showBaseUrl = isOpenAICompatibleProvider(activeProvider)
  const showApiKey = true
  const showTLSSection = showBaseUrl

  const [fpLoading, setFpLoading] = useState(false)
  const [fpError, setFpError] = useState<string | null>(null)

  // Local visibility state for the fingerprint input section (ADR-050): the
  // checkbox is "Custom TLS fingerprint" and its checked state is DERIVED
  // from the persisted pin (tls_fingerprint !== '') on provider switch, but
  // stays local afterwards so collapsing the input (uncheck) does not
  // silently clear the pin until the user actually saves. Unchecking emits
  // an explicit tls_fingerprint: '' (Go-side nil = keep, '' = clear).
  // Reset when the provider changes so each accordion section initializes
  // its visibility from that provider's own pin.
  const [fpSectionOpen, setFpSectionOpen] = useState(() => (config?.tls_fingerprint ?? '') !== '')
  const [lastProviderRef, setLastProviderRef] = useState(activeProvider)
  if (lastProviderRef !== activeProvider) {
    setLastProviderRef(activeProvider)
    setFpSectionOpen((config?.tls_fingerprint ?? '') !== '')
  }

  const handleGetFingerprint = async () => {
    setFpLoading(true)
    setFpError(null)
    try {
      const resp = await getProviderTLSCertificate({
        provider: activeProvider,
        base_url: config.base_url || undefined,
      })
      onConfigChange({ tls_fingerprint: resp.fingerprint })
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setFpError(msg)
      logger.error('Get fingerprint failed:', err)
    } finally {
      setFpLoading(false)
    }
  }

  return (
    <>
      {/* Base URL - for OpenAI Compatible */}
      {showBaseUrl && (
        <div className="flex flex-col gap-2">
          <label className="text-xs text-muted-foreground">Base URL</label>
          <div className="flex items-center gap-2">
            <Input
              placeholder="http://localhost:1234"
              value={config?.base_url ?? ''}
              onChange={(e) => onConfigChange({ base_url: e.target.value })}
              className="h-9 text-sm flex-1"
            />
          </div>
        </div>
      )}

      {/* API Key */}
      {showApiKey && (
        <div className="flex flex-col gap-2">
          <label className="text-xs text-muted-foreground">API Key</label>
          <div className="flex items-center gap-2">
            <Input
              type={(() => {
                const val = config?.api_key === '***configured***' ? '' : (config?.api_key ?? '')
                return val.startsWith('${') ? 'text' : 'password'
              })()}
              placeholder="Enter API key"
              value={config?.api_key === '***configured***' ? '' : (config?.api_key ?? '')}
              onChange={(e) => onConfigChange({ api_key: e.target.value })}
              className="h-9 text-sm flex-1"
            />
            {config?.api_key === '***configured***' && (
              <Badge variant="outline" className="text-xs">
                Configured
              </Badge>
            )}
            {apiKeyDirty && hasRequiredCredentials && (
              <Button size="sm" onClick={onApply} disabled={modelsLoading}>
                {modelsLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : 'Fetch models'}
              </Button>
            )}
          </div>
        </div>
      )}

      {/* TLS verification override — compatible providers only (ADR-050) */}
      {showTLSSection && (
        <div className="flex flex-col gap-2">
          <label className="flex cursor-pointer items-center gap-2">
            <input
              type="checkbox"
              checked={fpSectionOpen}
              onChange={(e) => {
                setFpSectionOpen(e.target.checked)
                // Unchecking must clear the pin: '' is the explicit-clear
                // sentinel (backend nil = keep), so the "override on, pin
                // empty" state is unreachable after a save.
                if (!e.target.checked) onConfigChange({ tls_fingerprint: '' })
              }}
              className="h-3.5 w-3.5"
            />
            <span className="text-xs text-muted-foreground">Custom TLS fingerprint</span>
          </label>

          {fpSectionOpen && (
            <div className="flex flex-col gap-1.5">
              <label className="text-xs text-muted-foreground">
                Certificate fingerprint (SPKI, base64)
              </label>
              <div className="flex items-center gap-2">
                <Input
                  placeholder="base64(SHA-256(SPKI DER)) pin"
                  value={config?.tls_fingerprint ?? ''}
                  onChange={(e) => onConfigChange({ tls_fingerprint: e.target.value })}
                  className="h-9 text-sm flex-1 font-mono"
                />
                <Button
                  size="sm"
                  variant="outline"
                  onClick={handleGetFingerprint}
                  disabled={fpLoading || !config?.base_url}
                  title={
                    config?.base_url
                      ? 'Connect and fetch the fingerprint the server currently presents'
                      : 'Set a base URL first'
                  }
                >
                  {fpLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : 'Get'}
                </Button>
              </div>
              <span className="text-[11px] text-muted-foreground">
                Empty pin = standard certificate verification (override off); a
                pinned fingerprint accepts only this key and survives
                certificate renewal.
              </span>
              {fpError && <span className="text-xs text-destructive">{fpError}</span>}
            </div>
          )}
        </div>
      )}
    </>
  )
}
