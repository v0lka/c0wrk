import { useState, useEffect } from 'react'
import { Cpu, Info } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useSessionAPI } from '@/hooks/useSession'

export function ModelSelector() {
  const api = useSessionAPI()
  const [modelName, setModelName] = useState<string>('Loading...')

  useEffect(() => {
    const promise = api.getConfig?.()
    if (!promise) {
      setModelName('Not configured')
      return
    }
    promise.then((config) => {
      // Extract active model from typed config
      const llm = config?.llm
      const activeProvider = llm?.active_provider
      let model = 'Not configured'
      if (activeProvider && llm) {
        const providerConfig = llm[activeProvider as keyof typeof llm]
        if (providerConfig && typeof providerConfig === 'object' && 'model' in providerConfig) {
          model = (providerConfig as { model: string }).model || 'Not configured'
        }
      }
      setModelName(model)
    }).catch(() => {
      setModelName('Not configured')
    })
  }, [api])

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium">Model</span>
      </div>
      
      <Button 
        variant="outline" 
        className="w-full justify-start gap-2"
        disabled
      >
        <Cpu className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm truncate">{modelName}</span>
      </Button>

      <div className="flex items-start gap-2 text-xs text-muted-foreground">
        <Info className="h-3.5 w-3.5 flex-shrink-0 mt-0.5" />
        <p>
          Model selection is managed through the configuration file.
        </p>
      </div>
    </div>
  )
}
