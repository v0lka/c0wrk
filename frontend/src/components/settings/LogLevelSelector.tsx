import { useEffect, useState, useCallback } from 'react'
import { ScrollText } from 'lucide-react'
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
      {/* Radio semantics and the primitive's own compact, content-width track —
          no `fullWidth`/`flex-1`, which stretched the segments and was the
          only chrome difference from Security → Autonomy mode. String labels
          stay the segments' static visible text inside the primitive's
          <span>, so the buttonTitleInvariant scan never sees a bare <button>. */}
      <SegmentedControl<LogLevel>
        semantic="radio"
        ariaLabel="Log level"
        items={logLevelOptions.map((option) => ({
          value: option.value,
          label: option.label,
        }))}
        value={logLevel}
        onValueChange={(level) => void handleLogLevelChange(level)}
      />
    </div>
  )
}
