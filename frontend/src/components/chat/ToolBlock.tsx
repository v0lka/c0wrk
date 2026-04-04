import { useState } from 'react'
import { Check, X, Loader2, Clock, ChevronDown, ChevronRight, Wrench } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'

interface ToolBlockProps {
  toolName: string
  args: string
  result?: string
  resultLen?: number
  status: 'running' | 'success' | 'error' | 'awaiting_confirmation'
}

function formatResultLen(len: number): string {
  if (len >= 1000) {
    return (len / 1000).toFixed(1).replace(/\.0$/, '') + 'K chars'
  }
  return len + ' chars'
}

export function ToolBlock({ toolName, args, result, resultLen, status }: ToolBlockProps) {
  const [isOpen, setIsOpen] = useState(false)
  const [showFullArgs, setShowFullArgs] = useState(false)
  const [showFullResult, setShowFullResult] = useState(false)

  const MAX_PREVIEW = 200

  const isArgsLong = args.length > MAX_PREVIEW || args.includes('\n')
  const displayArgs = (!showFullArgs && isArgsLong) ? args.slice(0, MAX_PREVIEW) + '...' : args

  const isResultLong = !!result && (result.length > MAX_PREVIEW || result.includes('\n'))
  const displayResult = result && (!showFullResult && isResultLong) ? result.slice(0, MAX_PREVIEW) + '...' : result

  const StatusIcon = status === 'success' ? Check 
    : status === 'error' ? X 
    : status === 'awaiting_confirmation' ? Clock
    : Loader2

  const statusClass = status === 'success'
    ? 'text-emerald-500'
    : status === 'error'
      ? 'text-red-500'
      : status === 'awaiting_confirmation'
        ? 'text-amber-500'
        : 'text-muted-foreground animate-spin'

  return (
    <Collapsible open={isOpen} onOpenChange={setIsOpen}>
      <CollapsibleTrigger className="flex items-center gap-1.5 text-muted-foreground hover:text-foreground transition-colors">
        {isOpen ? (
          <ChevronDown className="h-3.5 w-3.5" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5" />
        )}
        <StatusIcon className={`h-3.5 w-3.5 ${statusClass}`} />
        <Wrench className="h-3.5 w-3.5" />
        <span className="text-sm">Tool called: {toolName}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 space-y-3 min-w-0">
          {/* Arguments */}
          <div>
            <span className="text-xs text-muted-foreground font-medium">Arguments</span>
            <pre className="mt-1 font-mono text-xs text-muted-foreground whitespace-pre-wrap break-all">
              {displayArgs}
            </pre>
            {isArgsLong && (
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  setShowFullArgs(!showFullArgs)
                }}
                className="text-xs text-muted-foreground hover:text-foreground hover:bg-accent/50 active:bg-accent/70 rounded px-1 py-0.5 mt-1 transition-colors"
              >
                {showFullArgs ? 'Show less' : 'Show more'}
              </button>
            )}
          </div>

          {/* Result */}
          {result !== undefined && (
            <div>
              <div className="flex items-center gap-2">
                <span className="text-xs text-muted-foreground font-medium">Result</span>
                {resultLen !== undefined && resultLen > 500 && (
                  <span className="text-xs text-muted-foreground/50 bg-muted/50 px-1.5 py-0.5 rounded">
                    {formatResultLen(resultLen)}
                  </span>
                )}
              </div>
              <pre className="mt-1 font-mono text-xs text-muted-foreground whitespace-pre-wrap break-all">
                {displayResult}
              </pre>
              {isResultLong && (
                <button
                  onClick={(e) => {
                    e.stopPropagation()
                    setShowFullResult(!showFullResult)
                  }}
                  className="text-xs text-muted-foreground hover:text-foreground hover:bg-accent/50 active:bg-accent/70 rounded px-1 py-0.5 mt-1 transition-colors"
                >
                  {showFullResult ? 'Show less' : 'Show more'}
                </button>
              )}
            </div>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}
