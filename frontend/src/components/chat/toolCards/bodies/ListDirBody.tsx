import React, { useState } from 'react'
import type { ToolBodyProps } from '../toolCardRegistry'
import { FileLink } from '../shared/FileLink'

const MAX_ENTRIES = 20

export const ListDirBody = React.memo(function ListDirBody({ result, status }: ToolBodyProps) {
  const [showAll, setShowAll] = useState(false)

  if (status === 'running') {
    return (
      <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0">
        <span className="text-xs text-muted-foreground italic">Listing...</span>
      </div>
    )
  }

  if (!result) return null

  const entries = result.split('\n').filter(l => l.trim())
  const visible = showAll ? entries : entries.slice(0, MAX_ENTRIES)
  const hasMore = entries.length > MAX_ENTRIES

  return (
    <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0 space-y-0.5">
      {visible.map((entry) => {
        const isDir = entry.endsWith('/')
        return (
          <div key={entry} className="text-xs truncate">
            {isDir ? (
              <span className="text-muted-foreground font-mono">{entry}</span>
            ) : (
              <FileLink path={entry} />
            )}
          </div>
        )
      })}
      {hasMore && !showAll && (
        <button
          onClick={(e) => { e.stopPropagation(); setShowAll(true) }}
          className="text-xs text-muted-foreground hover:text-foreground hover:bg-accent/50 rounded px-1 py-0.5 mt-1 transition-colors"
        >
          Show all ({entries.length} entries)
        </button>
      )}
    </div>
  )
})
