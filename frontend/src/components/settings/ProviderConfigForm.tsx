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
  /** Per-provider TLS pin (ADR-054): '' = standard CA verification. */
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
  /**
   * An effective HTTP proxy (enabled + a URL) is configured. The
   * per-provider TLS pin does not apply to proxied connections (proxy wins,
   * ADR-054), so the whole TLS section is disabled and an explanation is
   * shown. Defaults to false so callers that do not track proxy state keep
   * the controls usable — the backend guards the fingerprint RPC anyway.
   */
  proxyActive?: boolean
}

export function ProviderConfigForm({
  activeProvider,
  config,
  apiKeyDirty,
  hasRequiredCredentials,
  modelsLoading,
  onConfigChange,
  onApply,
  proxyActive = false,
}: ProviderConfigFormProps) {
  const showBaseUrl = isOpenAICompatibleProvider(activeProvider)
  const showApiKey = true
  // Only compatible providers store a pin: the fixed ones talk to vendor
  // endpoints with public certificates, where pinning is pointless.
  const showTLSSection = showBaseUrl

  // The pin itself is the switch (ADR-054) — an empty field means standard
  // verification — so there is deliberately no checkbox mirroring it. The
  // only local state here is the in-flight/error state of the Get button.
  const [fpLoading, setFpLoading] = useState(false)
  const [fpError, setFpError] = useState<string | null>(null)

  // Unconditional with respect to the configured pin: no fingerprint is
  // sent, and the result overwrites whatever the field holds. Pressing Get
  // always answers "what is this endpoint serving right now?".
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
      setFpError(err instanceof Error ? err.message : String(err))
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

      {/* TLS verification override — compatible providers only (ADR-054).
          The field and the Get button are always present: the pin is the
          switch, so an empty field already means "standard verification"
          and a separate toggle would only hide the Get button behind an
          extra click. Disabled wholesale while a proxy is active, because
          the pin never applies to proxied connections. */}
      {showTLSSection && (
        <div className="flex flex-col gap-2">
          <label className="text-xs text-muted-foreground">
            Certificate fingerprint (SPKI, base64)
          </label>
          <div className="flex items-center gap-2">
            <Input
              placeholder="base64(SHA-256(SPKI DER)) pin"
              value={config?.tls_fingerprint ?? ''}
              onChange={(e) => onConfigChange({ tls_fingerprint: e.target.value })}
              disabled={proxyActive}
              className="h-9 text-sm flex-1 font-mono"
            />
            <Button
              size="sm"
              variant="outline"
              onClick={handleGetFingerprint}
              disabled={fpLoading || !config?.base_url || proxyActive}
              title={
                proxyActive
                  ? 'Unavailable while an HTTP proxy is enabled'
                  : config?.base_url
                    ? 'Connect and read the fingerprint the server currently presents'
                    : 'Set a base URL first'
              }
            >
              {fpLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : 'Get'}
            </Button>
          </div>
          {proxyActive ? (
            <span className="text-[11px] text-muted-foreground">
              Not available while an HTTP proxy is enabled — the fingerprint pin
              does not apply to proxied connections. Disable the proxy
              (Settings → General → HTTP Proxy), or add this host to the proxy
              bypass list, to use certificate pinning. A saved pin is kept and
              takes effect again once the proxy is off.
            </span>
          ) : (
            <span className="text-[11px] text-muted-foreground">
              Empty = standard certificate verification. A pinned fingerprint
              accepts only this key and survives certificate renewal. Get reads
              whatever the server presents right now.
            </span>
          )}
          {fpError && <span className="text-xs text-destructive">{fpError}</span>}
        </div>
      )}
    </>
  )
}
