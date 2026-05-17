import React from 'react'
import type { ToolBodyProps } from '../toolCardRegistry'
import { TruncatedContent } from '../shared/TruncatedContent'

export const FileReadBody = React.memo(function FileReadBody({ result, status, parsedArgs, args }: ToolBodyProps) {
  if (status === 'running') {
    return (
      <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0">
        <span className="text-xs text-muted-foreground italic">Reading...</span>
      </div>
    )
  }

  if (!result) return null

  const parsed = parsedArgs ?? tryParse(args)
  const startLine = parsed?.start_line as number | undefined
  const endLine = parsed?.end_line as number | undefined

  return (
    <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0">
      {startLine && (
        <span className="text-xs text-muted-foreground/50 bg-muted/50 px-1.5 py-0.5 rounded mb-2 inline-block">
          Lines {startLine}{endLine ? `-${endLine}` : '+'}
        </span>
      )}
      <TruncatedContent content={result} maxLines={30} />
    </div>
  )
})

function tryParse(s: string): Record<string, unknown> | undefined {
  try { return JSON.parse(s) } catch { return undefined }
}
