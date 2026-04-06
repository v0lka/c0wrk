import { useEffect } from 'react'
import { Info } from 'lucide-react'
import { useUIStore } from '@/stores/uiStore'
import { Button } from '@/components/ui/button'
import { GetLogLevel, SetLogLevel } from '../../../wailsjs/go/main/App'
import { logger } from '@/lib/logger'

type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'

interface LogLevelOption {
  value: LogLevel
  label: string
}

const logLevelOptions: LogLevelOption[] = [
  { value: 'DEBUG', label: 'Debug' },
  { value: 'INFO', label: 'Info' },
  { value: 'WARN', label: 'Warn' },
  { value: 'ERROR', label: 'Error' },
]

export function LogLevelSelector() {
  const logLevel = useUIStore((s) => s.logLevel)
  const setLogLevel = useUIStore((s) => s.setLogLevel)

  useEffect(() => {
    GetLogLevel()
      .then((level: string) => {
        if (level && ['DEBUG', 'INFO', 'WARN', 'ERROR'].includes(level)) {
          setLogLevel(level as LogLevel)
        }
      })
      .catch((err) => logger.error('Failed to load log level:', err))
  }, [setLogLevel])

  const handleLogLevelChange = async (level: LogLevel) => {
    try {
      await SetLogLevel(level)
      setLogLevel(level)
    } catch (error) {
      logger.error('Failed to set log level:', error)
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium">Log Level</span>
      </div>
      <div className="flex gap-1 p-1 bg-muted rounded-lg">
        {logLevelOptions.map((option) => (
          <Button
            key={option.value}
            variant={logLevel === option.value ? 'secondary' : 'ghost'}
            size="sm"
            className={`flex-1 gap-2 justify-center transition-all duration-200 ${
              logLevel === option.value
                ? 'bg-background shadow-sm text-foreground'
                : 'text-muted-foreground hover:text-foreground'
            }`}
            onClick={() => handleLogLevelChange(option.value)}
          >
            <span className="text-xs">{option.label}</span>
          </Button>
        ))}
      </div>
      <div className="flex items-start gap-2 text-xs text-muted-foreground">
        <Info className="h-3.5 w-3.5 flex-shrink-0 mt-0.5" />
        <p>
          Changes apply to new sessions only.
        </p>
      </div>
    </div>
  )
}
