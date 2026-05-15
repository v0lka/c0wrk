import { useState, useEffect, useCallback, useRef } from 'react'
import { Input } from '@/components/ui/input'
import { getConfig, updateProxySettings } from '@/api/config'
import { logger } from '@/lib/logger'

interface ProxyConfig {
  enabled: boolean
  url: string
  bypass_list: string[]
  tls_cert_dir: string
}

const DEFAULT_CONFIG: ProxyConfig = {
  enabled: false,
  url: '',
  bypass_list: ['localhost', '127.0.0.1'],
  tls_cert_dir: '',
}

export function ProxySettings() {
  const [config, setConfig] = useState<ProxyConfig>(DEFAULT_CONFIG)
  const [isLoading, setIsLoading] = useState(true)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [bypassText, setBypassText] = useState('')
  const saveTimeoutRef = useRef<NodeJS.Timeout | null>(null)

  useEffect(() => {
    const load = async () => {
      try {
        const result = await getConfig()
        if (result?.proxy) {
          setConfig(result.proxy)
          setBypassText(result.proxy.bypass_list.join(', '))
        }
      } catch (err) {
        logger.error('Failed to load proxy config:', err)
      } finally {
        setIsLoading(false)
      }
    }
    load()
  }, [])

  const saveSettings = useCallback(async (newConfig: ProxyConfig) => {
    try {
      await updateProxySettings({
        enabled: newConfig.enabled,
        url: newConfig.url,
        bypass_list: newConfig.bypass_list,
        tls_cert_dir: newConfig.tls_cert_dir,
      })
      setSaveError(null)
    } catch (err) {
      logger.error('Failed to save proxy settings:', err)
      setSaveError('Failed to save proxy settings')
    }
  }, [])

  const debouncedSave = useCallback((newConfig: ProxyConfig) => {
    if (saveTimeoutRef.current) clearTimeout(saveTimeoutRef.current)
    saveTimeoutRef.current = setTimeout(() => saveSettings(newConfig), 800)
  }, [saveSettings])

  useEffect(() => () => { if (saveTimeoutRef.current) clearTimeout(saveTimeoutRef.current) }, [])

  const handleEnabledChange = (checked: boolean) => {
    const newConfig = { ...config, enabled: checked }
    setConfig(newConfig)
    debouncedSave(newConfig)
  }

  const handleUrlChange = (value: string) => {
    const newConfig = { ...config, url: value }
    setConfig(newConfig)
    debouncedSave(newConfig)
  }

  const handleBypassChange = (value: string) => {
    setBypassText(value)
    const list = value.split(',').map(s => s.trim()).filter(Boolean)
    const newConfig = { ...config, bypass_list: list }
    setConfig(newConfig)
    debouncedSave(newConfig)
  }

  const handleCertDirChange = (value: string) => {
    const newConfig = { ...config, tls_cert_dir: value }
    setConfig(newConfig)
    debouncedSave(newConfig)
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <span className="text-sm text-muted-foreground">Loading proxy settings...</span>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center gap-3">
        <label className="relative inline-flex items-center cursor-pointer">
          <input
            type="checkbox"
            checked={config.enabled}
            onChange={(e) => handleEnabledChange(e.target.checked)}
            className="sr-only peer"
          />
          <div className="w-9 h-5 bg-muted rounded-full peer peer-checked:bg-primary transition-colors after:content-[''] after:absolute after:top-0.5 after:start-[2px] after:bg-background after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:after:translate-x-full" />
        </label>
        <span className="text-sm font-medium">Use proxy</span>
      </div>

      <div className="flex flex-col gap-2">
        <label className="text-xs text-muted-foreground">Proxy URL</label>
        <Input
          placeholder="http://user:password@proxy.example.com:8080"
          value={config.url}
          onChange={(e) => handleUrlChange(e.target.value)}
          disabled={!config.enabled}
          className="h-9 text-sm"
        />
        <p className="text-xs text-muted-foreground">
          Format: scheme://[user:password@]host:port (http, https, or socks5)
        </p>
      </div>

      <div className="flex flex-col gap-2">
        <label className="text-xs text-muted-foreground">Bypass List</label>
        <Input
          placeholder="localhost, 127.0.0.1, *.internal.corp"
          value={bypassText}
          onChange={(e) => handleBypassChange(e.target.value)}
          disabled={!config.enabled}
          className="h-9 text-sm"
        />
        <p className="text-xs text-muted-foreground">
          Comma-separated hosts that bypass the proxy. Supports wildcards (*.domain.com).
        </p>
      </div>

      <div className="flex flex-col gap-2">
        <label className="text-xs text-muted-foreground">TLS Certificate Directory</label>
        <Input
          placeholder="/path/to/certs"
          value={config.tls_cert_dir}
          onChange={(e) => handleCertDirChange(e.target.value)}
          disabled={!config.enabled}
          className="h-9 text-sm"
        />
        <p className="text-xs text-muted-foreground">
          Directory containing .pem/.crt files to trust (added to system CA pool).
        </p>
      </div>

      {saveError && <p className="text-sm text-destructive mt-2">{saveError}</p>}
    </div>
  )
}
