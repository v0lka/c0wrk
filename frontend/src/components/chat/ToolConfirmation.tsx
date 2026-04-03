import { AlertTriangle, Info } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useWails } from '@/hooks/useWails'

interface ToolConfirmationProps {
  metadata?: unknown
}

export function ToolConfirmation({ metadata }: ToolConfirmationProps) {
  const { runtime } = useWails()
  
  const meta = metadata as Record<string, unknown> | undefined
  const tool = meta?.tool as string | undefined
  const args = meta?.args as string | undefined
  const confirmId = meta?.confirm_id as string | undefined
  const reasoning = meta?.reasoning as string | undefined

  const handleResponse = (decision: 'allow_once' | 'deny') => {
    if (!runtime) return
    
    runtime.EventsEmit('tool_confirm_response', {
      confirm_id: confirmId,
      decision,
    })
  }

  // Format JSON for display
  const formatJson = (data: string | undefined) => {
    if (!data) return null
    try {
      const parsed = JSON.parse(data)
      return JSON.stringify(parsed, null, 2)
    } catch {
      return data
    }
  }

  return (
        <div className="border-2 border-amber-500/50 rounded-lg p-4 bg-amber-500/5 max-w-full overflow-hidden">
          {/* Header */}
          <div className="flex items-center gap-2 mb-3">
            <AlertTriangle className="h-4 w-4 text-amber-500" />
            <span className="text-sm font-medium">Tool Confirmation Required</span>
          </div>

          {/* Judge reasoning */}
          {reasoning && (
            <div className="mb-4 p-3 bg-blue-500/10 border border-blue-500/30 rounded-md">
              <div className="flex items-start gap-2">
                <Info className="h-4 w-4 text-blue-400 shrink-0 mt-0.5" />
                <p className="text-sm text-blue-100">{reasoning}</p>
              </div>
            </div>
          )}

          {/* Tool info */}
          <div className="mb-4 space-y-2">
            <p className="text-sm">
              <span className="text-muted-foreground">Tool:</span>{' '}
              <span className="font-medium">{tool || 'Unknown'}</span>
            </p>
            {args && (
              <div className="min-w-0 overflow-hidden">
                <p className="text-xs text-muted-foreground mb-1">Input:</p>
                <pre className="p-2 bg-background/50 rounded text-xs font-mono overflow-x-auto border border-border max-w-full min-w-0">
                  <code>{formatJson(args)}</code>
                </pre>
              </div>
            )}
          </div>

          {/* Action buttons */}
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              variant="default"
              onClick={() => handleResponse('allow_once')}
              className="text-xs"
              aria-label="Allow this tool action once"
            >
              Allow Once
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => handleResponse('deny')}
              className="text-xs"
              aria-label="Deny this tool action"
            >
              Deny
            </Button>
          </div>
        </div>
  )
}
