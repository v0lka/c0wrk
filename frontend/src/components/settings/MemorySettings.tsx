import { useState, useEffect, useCallback, useRef } from 'react'
import { Database, Brain, Info } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { GetConfig, UpdateMemorySettings } from '../../../wailsjs/go/main/App'
import { main } from '../../../wailsjs/go/models'

interface EpisodicConfig {
  retention_days: number
  retrieval_limit: number
}

interface MemoryConfig {
  episodic: EpisodicConfig
}

export function MemorySettings() {
  const [config, setConfig] = useState<MemoryConfig>({
    episodic: { retention_days: 30, retrieval_limit: 10 },
  })
  const [isLoading, setIsLoading] = useState(true)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    const loadConfig = async () => {
      try {
        const result = await GetConfig()
        const memoryConfig = result?.memory
        if (memoryConfig) {
          setConfig({
            episodic: {
              retention_days: memoryConfig.episodic?.retention_days ?? 30,
              retrieval_limit: memoryConfig.episodic?.retrieval_limit ?? 10,
            },
          })
        }
      } catch {
        // Keep defaults if fetch fails
      } finally {
        setIsLoading(false)
      }
    }
    loadConfig()
  }, [])

  const saveConfig = useCallback(async (newConfig: MemoryConfig) => {
    try {
      const request = new main.MemorySettingsRequest({
        episodic: newConfig.episodic,
      })
      await UpdateMemorySettings(request)
    } catch {
      // Handle error silently
    }
  }, [])

  const handleEpisodicChange = (field: keyof EpisodicConfig, value: number) => {
    const newConfig: MemoryConfig = {
      ...config,
      episodic: {
        ...config.episodic,
        [field]: value,
      },
    }
    setConfig(newConfig)

    if (debounceRef.current) {
      clearTimeout(debounceRef.current)
    }
    debounceRef.current = setTimeout(() => {
      saveConfig(newConfig)
    }, 300)
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <span className="text-sm text-muted-foreground">Loading memory settings...</span>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-6">
      {/* Episodic Memory Section */}
      <div className="flex flex-col gap-3">
        <div className="flex items-center gap-2">
          <Database className="h-4 w-4 text-muted-foreground" />
          <span className="text-sm font-medium">Episodic Memory</span>
        </div>
        <p className="text-xs text-muted-foreground">
          Configure how past interactions are retained and retrieved.
        </p>

        <div className="flex flex-col gap-3 mt-2">
          <div className="flex flex-col gap-1.5">
            <label className="text-sm font-medium">Retention Days</label>
            <Input
              type="number"
              min={1}
              value={config.episodic.retention_days}
              onChange={(e) => handleEpisodicChange('retention_days', parseInt(e.target.value) || 0)}
              className="h-8"
            />
            <p className="text-xs text-muted-foreground">
              Number of days to keep interaction history.
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <label className="text-sm font-medium">Retrieval Limit</label>
            <Input
              type="number"
              min={1}
              value={config.episodic.retrieval_limit}
              onChange={(e) => handleEpisodicChange('retrieval_limit', parseInt(e.target.value) || 0)}
              className="h-8"
            />
            <p className="text-xs text-muted-foreground">
              Maximum number of past interactions to retrieve.
            </p>
          </div>
        </div>
      </div>

      {/* Semantic Memory Section */}
      <div className="flex flex-col gap-3">
        <div className="flex items-center gap-2">
          <Brain className="h-4 w-4 text-muted-foreground" />
          <span className="text-sm font-medium">Semantic Memory</span>
        </div>
        <p className="text-xs text-muted-foreground">
          Uses a built-in local embedding model (all-MiniLM-L6-v2). No configuration needed.
        </p>
      </div>

      <div className="flex items-start gap-2 text-xs text-muted-foreground">
        <Info className="h-3.5 w-3.5 flex-shrink-0 mt-0.5" />
        <p>Memory settings changes take effect on app restart.</p>
      </div>
    </div>
  )
}
