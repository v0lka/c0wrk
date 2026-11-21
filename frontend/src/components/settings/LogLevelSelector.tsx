import { useEffect, useState, useCallback } from 'react'
import { Info, ScrollText } from 'lucide-react'
import { SegmentedControl } from '@/components/ui/segmented-control'
import { getLogLevel, setLogLevel as apiSetLogLevel } from '@/api/config'
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
  const [logLevel, setLogLevel] = useState<LogLevel>('DEBUG')

  useEffect(() => {
    getLogLevel()
      .then((level: string) => {
        if (level && ['DEBUG', 'INFO', 'WARN', 'ERROR'].includes(level)) {
          setLogLevel(level as LogLevel)
        }
      })
      .catch((err) => logger.error('Failed to load log level:', err))
  }, [])

  const handleLogLevelChange = useCallback(async (level: LogLevel) => {
    try {
      await apiSetLogLevel(level)
      setLogLevel(level)
    } catch (error) {
      logger.error('Failed to set log level:', error)
    }
  }, [])

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <ScrollText className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm font-medium">Log Level</span>
      </div>
      {/* The shared app-wide segmented control (radio semantics, same chrome
          as Security → Autonomy mode). String labels are the segments' static
          visible text — the SegmentedControl renders them inside a <span>, so
          the buttonTitleInvariant scan never sees a bare <button> here. */}
      <SegmentedControl<LogLevel>
        semantic="radio"
        fullWidth
        ariaLabel="Log level"
        itemClassName="flex-1"
        items={logLevelOptions.map((option) => ({
          value: option.value,
          label: option.label,
        }))}
        value={logLevel}
        onValueChange={(level) => void handleLogLevelChange(level)}
      />
      <div className="flex items-start gap-2 text-xs text-muted-foreground">
        <Info className="h-3.5 w-3.5 flex-shrink-0 mt-0.5" />
        <p>
          Changes apply to new sessions only.
        </p>
      </div>
    </div>
  )
}
