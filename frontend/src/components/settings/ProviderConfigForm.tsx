import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Loader2 } from 'lucide-react'

interface ProviderConfig {
  api_key: string
  base_url: string
  model: string
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
  const showBaseUrl = activeProvider === 'lmstudio' || activeProvider === 'openai_compatible'
  const showApiKey = activeProvider !== 'lmstudio'

  return (
    <>
      {/* Base URL - for LM Studio and OpenAI Compatible */}
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
            {activeProvider === 'lmstudio' && apiKeyDirty && hasRequiredCredentials && (
              <Button size="sm" onClick={onApply} disabled={modelsLoading}>
                {modelsLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : 'Apply'}
              </Button>
            )}
          </div>
        </div>
      )}

      {/* API Key - for all except LM Studio */}
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
                {modelsLoading ? <Loader2 className="h-4 w-4 animate-spin" /> : 'Apply'}
              </Button>
            )}
          </div>
        </div>
      )}
    </>
  )
}
