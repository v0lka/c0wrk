import React, { useState } from 'react'
import { Check, X, Loader2, Clock, ChevronDown, ChevronRight, Wrench } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'

interface ToolBlockProps {
  toolName: string
  args: string
  parsedArgs?: Record<string, unknown>
  result?: string
  resultLen?: number
  status: 'running' | 'success' | 'error' | 'awaiting_confirmation'
  source?: string
}

function formatResultLen(len: number): string {
  if (len >= 1000) {
    return (len / 1000).toFixed(1).replace(/\.0$/, '') + 'K chars'
  }
  return len + ' chars'
}

export const ToolBlock = React.memo(function ToolBlock({ toolName, args, parsedArgs, result, resultLen, status, source }: ToolBlockProps) {
  const [isOpen, setIsOpen] = useState(false)
  const [showFull, setShowFull] = useState(false)

  const MAX_PREVIEW = 200

  // Parse args into key-value pairs if possible
  const argEntries: [string, unknown][] | null = (() => {
    try {
      const obj = parsedArgs ?? JSON.parse(args)
      if (obj && typeof obj === 'object' && !Array.isArray(obj)) {
        return Object.entries(obj)
      }
    } catch { /* fall through */ }
    return null
  })()

  const formatValue = (v: unknown): string => {
    if (v === null || v === undefined) return String(v)
    if (typeof v === 'string') return v
    if (typeof v === 'object') return JSON.stringify(v)
    return String(v)
  }

  const formattedArgs = argEntries
    ? argEntries.map(([k, v]) => `- ${k}: ${formatValue(v)}`).join('\n')
    : args

  const isArgsLong = formattedArgs.length > MAX_PREVIEW || formattedArgs.includes('\n')
  const displayArgs = (!showFull && isArgsLong) ? formattedArgs.slice(0, MAX_PREVIEW) + '...' : formattedArgs

  const isResultLong = !!result && (result.length > MAX_PREVIEW || result.includes('\n'))
  const displayResult = result && (!showFull && isResultLong) ? result.slice(0, MAX_PREVIEW) + '...' : result

  const hasLongContent = isArgsLong || isResultLong

  const StatusIcon = status === 'success' ? Check 
    : status === 'error' ? X 
    : status === 'awaiting_confirmation' ? Clock
    : Loader2

  const statusClass = status === 'success'
    ? 'text-success'
    : status === 'error'
      ? 'text-destructive'
      : status === 'awaiting_confirmation'
        ? 'text-warning'
        : 'text-muted-foreground animate-spin'

  return (
    <Collapsible open={isOpen} onOpenChange={setIsOpen} className="group">
      <CollapsibleTrigger className="flex items-center gap-1.5 text-muted-foreground hover:text-foreground transition-colors">
        <span className="opacity-0 group-hover:opacity-100 transition-opacity inline-flex">
          {isOpen ? (
            <ChevronDown className="h-3.5 w-3.5" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5" />
          )}
        </span>
        <StatusIcon className={`h-3.5 w-3.5 ${statusClass}`} />
        <Wrench className="h-3.5 w-3.5" />
        <span className="text-sm">Tool called: <span className="text-muted-foreground/60">{toolName}</span></span>
        {source !== undefined && source !== '' && source !== 'core' && (
          <span className="text-[10px] font-medium bg-muted-foreground/15 text-foreground px-1.5 py-0.5 rounded">
            MCP
          </span>
        )}
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 space-y-3 min-w-0">
          {/* Arguments */}
          <div>
            <span className="text-xs text-muted-foreground font-medium">Arguments</span>
            <pre className="mt-1 font-mono text-xs text-muted-foreground whitespace-pre-wrap break-all">
              {displayArgs}
            </pre>
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
            </div>
          )}

          {hasLongContent && (
            <button
              onClick={(e) => {
                e.stopPropagation()
                setShowFull(!showFull)
              }}
              className="text-xs text-muted-foreground hover:text-foreground hover:bg-accent/50 active:bg-accent/70 rounded px-1 py-0.5 mt-1 transition-colors"
            >
              {showFull ? 'Show less' : 'Show more'}
            </button>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
})
