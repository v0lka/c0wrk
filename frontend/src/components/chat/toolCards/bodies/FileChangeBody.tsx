import React from 'react'
import type { ToolBodyProps } from '../toolCardRegistry'
import { FileLink } from '../shared/FileLink'

export const FileChangeBody = React.memo(function FileChangeBody({ result, status, parsedArgs, args }: ToolBodyProps) {
  if (status === 'running') {
    return (
      <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0">
        <span className="text-xs text-muted-foreground italic">Writing...</span>
      </div>
    )
  }

  if (status === 'error' && result) {
    return (
      <div className="mt-2 border-l-2 border-destructive/50 bg-muted/30 rounded p-3 min-w-0">
        <pre className="font-mono text-xs text-destructive whitespace-pre-wrap break-all">{result}</pre>
      </div>
    )
  }

  // Check if result has diff markers
  const hasDiff = result && result.includes('@@')
  if (!hasDiff) {
    // No diff available — just show success
    const parsed = parsedArgs ?? tryParse(args)
    const path = (parsed?.path as string) || ''
    return (
      <div className="mt-2 border-l-2 border-success/30 bg-muted/30 rounded p-3 min-w-0 flex items-center gap-2">
        <span className="text-xs text-muted-foreground">File updated</span>
        {path && <FileLink path={path} className="text-xs" />}
      </div>
    )
  }

  // Render simple diff
  const lines = result!.split('\n')
  const diffLines = lines.filter(l => l.startsWith('+') || l.startsWith('-') || l.startsWith('@@'))
    .slice(0, 30)

  return (
    <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0">
      <pre className="font-mono text-xs whitespace-pre-wrap break-all">
        {diffLines.map((line, i) => {
          let cls = 'text-muted-foreground'
          if (line.startsWith('+') && !line.startsWith('+++')) cls = 'text-success'
          else if (line.startsWith('-') && !line.startsWith('---')) cls = 'text-destructive'
          else if (line.startsWith('@@')) cls = 'text-info'
          return <div key={i} className={cls}>{line}</div>
        })}
      </pre>
      {lines.length > 30 && (
        <span className="text-xs text-muted-foreground/50 mt-1 block">
          ...{lines.length - 30} more lines
        </span>
      )}
    </div>
  )
})

function tryParse(s: string): Record<string, unknown> | undefined {
  try { return JSON.parse(s) } catch { return undefined }
}
