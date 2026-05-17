import React from 'react'
import type { ToolBodyProps } from '../toolCardRegistry'

export const MemoBody = React.memo(function MemoBody({ result, status, parsedArgs, args }: ToolBodyProps) {
  if (status === 'running') {
    return (
      <div className="mt-2 border-l-2 border-accent/30 bg-muted/30 rounded p-3 min-w-0">
        <span className="text-xs text-muted-foreground italic">Processing...</span>
      </div>
    )
  }

  // For store_fact, show the content from args
  const parsed = parsedArgs ?? tryParse(args)
  const factContent = parsed?.content as string | undefined
  const todoList = parsed?.todo_list as string | undefined
  const display = result || factContent || todoList

  if (!display) return null

  return (
    <div className="mt-2 border-l-2 border-accent/30 bg-muted/30 rounded p-3 min-w-0">
      <pre className="font-mono text-xs text-muted-foreground whitespace-pre-wrap break-all">
        {display}
      </pre>
    </div>
  )
})

function tryParse(s: string): Record<string, unknown> | undefined {
  try { return JSON.parse(s) } catch { return undefined }
}
