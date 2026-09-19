import { useMemo, useState } from 'react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Loader2 } from 'lucide-react'
import { isOpenAICompatibleProvider } from '@/lib/llm-providers'
import { getProviderTLSCertificate } from '@/api/config'
import { logger } from '@/lib/logger'
import { useProxyDraftStore, pinGatedByProxy } from '@/stores/proxyDraftStore'

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
  // Only compatible providers store a pin: the fixed ones talk to vendor
  // endpoints with public certificates, where pinning is pointless.
  const showTLSSection = showBaseUrl

  // The pin itself is the switch (ADR-054) — an empty field means standard
  // verification — so there is deliberately no checkbox mirroring it. The
  // only local state here is the in-flight/error state of the Get button.
  const [fpLoading, setFpLoading] = useState(false)
  const [fpError, setFpError] = useState<string | null>(null)

  // Per-provider gate from the shared draft store: the section is disabled
  // only while the proxy DIALS for this provider's host — effective proxy
  // AND the host not on the bypass list. A bypassed host keeps its pin and
  // its Get button (the probe dials directly, and the pin applies). Selectors
  // return stable values; the gate itself is derived in useMemo because it
  // needs the draft bypass list (a fresh array must not be allocated inside
  // a Zustand selector).
  const proxyActive = useProxyDraftStore((s) => s.active === true)
  const bypassList = useProxyDraftStore((s) => s.bypassList)
  const proxyDials = useMemo(
    () => pinGatedByProxy(proxyActive, bypassList, config?.base_url),
    [proxyActive, bypassList, config?.base_url],
  )

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
          extra click. Disabled while the proxy dials for this provider's
          host (active AND not bypassed); a bypassed host dials directly, so
          its pin applies. */}
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
              disabled={proxyDials}
              className="h-9 text-sm flex-1 font-mono"
            />
            <Button
              size="sm"
              variant="outline"
              onClick={handleGetFingerprint}
              disabled={fpLoading || !config?.base_url || proxyDials}
              title={
                proxyDials
                  ? 'Unavailable while the proxy dials for this host'
                  : config?.base_url
                    ? 'Connect and read the fingerprint the server currently presents'
                    : 'Set a base URL first'
              }
            >
              {fpLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : 'Get'}
            </Button>
          </div>
          {proxyDials ? (
            <span className="text-[11px] text-muted-foreground">
              Not available while an HTTP proxy is enabled and this host is not
              on its bypass list — the fingerprint pin does not apply to
              proxied connections. Disable the proxy (Settings → General → HTTP
              Proxy) or add this host to the proxy bypass list, to use
              certificate pinning. A saved pin is kept and takes effect again
              once the proxy stops dialing for this host.
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
