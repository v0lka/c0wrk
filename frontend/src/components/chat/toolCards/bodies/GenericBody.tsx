import React, { useState } from 'react'
import type { ToolBodyProps } from '../toolCardRegistry'

function formatValue(v: unknown): string {
  if (v === null || v === undefined) return String(v)
  if (typeof v === 'string') return v
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

export const GenericBody = React.memo(function GenericBody({ args, parsedArgs, result, resultLen }: ToolBodyProps) {
  const [showFull, setShowFull] = useState(false)
  const MAX_PREVIEW = 200

  let formattedArgs = args
  try {
    const obj = parsedArgs ?? JSON.parse(args)
    if (obj && typeof obj === 'object' && !Array.isArray(obj)) {
      formattedArgs = Object.entries(obj).map(([k, v]) => `- ${k}: ${formatValue(v)}`).join('\n')
    }
  } catch { /* use raw */ }

  const isArgsLong = formattedArgs.length > MAX_PREVIEW || formattedArgs.includes('\n')
  const displayArgs = !showFull && isArgsLong ? formattedArgs.slice(0, MAX_PREVIEW) + '...' : formattedArgs
  const isResultLong = !!result && (result.length > MAX_PREVIEW || result.includes('\n'))
  const displayResult = result && !showFull && isResultLong ? result.slice(0, MAX_PREVIEW) + '...' : result

  return (
    <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 space-y-3 min-w-0">
      {formattedArgs && (
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
                {resultLen >= 1000 ? (resultLen / 1000).toFixed(1).replace(/\.0$/, '') + 'K chars' : resultLen + ' chars'}
              </span>
            )}
          </div>
          <pre className="mt-1 font-mono text-xs text-muted-foreground whitespace-pre-wrap break-all">{displayResult}</pre>
        </div>
      )}
      {(isArgsLong || isResultLong) && (
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
