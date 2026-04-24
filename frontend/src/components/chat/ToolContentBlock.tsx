import React, { useState } from 'react'

const MAX_PREVIEW = 200

export function formatValue(v: unknown): string {
  if (v === null || v === undefined) return String(v)
  if (typeof v === 'string') return v
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

export function parseArgs(args: string, parsedArgs?: Record<string, unknown>): string {
  try {
    const obj = parsedArgs ?? JSON.parse(args)
    if (obj && typeof obj === 'object' && !Array.isArray(obj)) {
      return Object.entries(obj).map(([k, v]) => `- ${k}: ${formatValue(v)}`).join('\n')
    }
  } catch { /* fall through */ }
  return args
}

export function formatResultLen(len: number): string {
  if (len >= 1000) return (len / 1000).toFixed(1).replace(/\.0$/, '') + 'K chars'
  return len + ' chars'
}

interface ToolContentBlockProps {
  args: string
  result?: string
  resultLen?: number
  borderClass?: string
}

/** Shared args/result display with show-more toggle, used by ToolBlock and MemoryBlock. */
export const ToolContentBlock = React.memo(function ToolContentBlock({
  args, result, resultLen, borderClass = 'border-border',
}: ToolContentBlockProps) {
  const [showFull, setShowFull] = useState(false)

  const isArgsLong = args.length > MAX_PREVIEW || args.includes('\n')
  const displayArgs = !showFull && isArgsLong ? args.slice(0, MAX_PREVIEW) + '...' : args
  const isResultLong = !!result && (result.length > MAX_PREVIEW || result.includes('\n'))
  const displayResult = result && !showFull && isResultLong ? result.slice(0, MAX_PREVIEW) + '...' : result
  const hasLongContent = isArgsLong || isResultLong

  return (
    <div className={`mt-2 border-l-2 ${borderClass} bg-muted/30 rounded p-3 space-y-3 min-w-0`}>
      {args && (
        <div>
          <span className="text-xs text-muted-foreground font-medium">Arguments</span>
          <pre className="mt-1 font-mono text-xs text-muted-foreground whitespace-pre-wrap break-all">{displayArgs}</pre>
        </div>
      )}
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
          <pre className="mt-1 font-mono text-xs text-muted-foreground whitespace-pre-wrap break-all">{displayResult}</pre>
        </div>
      )}
      {hasLongContent && (
        <button
          onClick={(e) => { e.stopPropagation(); setShowFull(!showFull) }}
          className="text-xs text-muted-foreground hover:text-foreground hover:bg-accent/50 rounded px-1 py-0.5 mt-1 transition-colors"
        >
          {showFull ? 'Show less' : 'Show more'}
        </button>
      )}
    </div>
  )
})
