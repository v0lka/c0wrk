import React from 'react'
import type { ToolBodyProps } from '../toolCardRegistry'
import { TruncatedContent } from '../shared/TruncatedContent'

export const WebFetchBody = React.memo(function WebFetchBody({ result, status }: ToolBodyProps) {
  if (status === 'running') {
    return (
      <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0">
        <span className="text-xs text-muted-foreground italic">Fetching...</span>
      </div>
    )
  }

  if (!result) return null

  return (
    <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0">
      <TruncatedContent content={result} maxLines={15} />
    </div>
  )
})
