import React, { useMemo, useState } from 'react'
import type { ToolBodyProps } from '../toolCardRegistry'
import { FileLink } from '../shared/FileLink'
import { extractToolPathArg, makeWorkspacePathResolver } from '../shared/workspacePathResolver'
import { parseListEntry } from '../shared/listDirEntry'

const MAX_ENTRIES = 20

export const ListDirBody = React.memo(function ListDirBody({ parsedArgs, args, result, status }: ToolBodyProps) {
  const [showAll, setShowAll] = useState(false)

  // Entry names are relative to the LISTED directory, so clicks resolve each
  // name against the call's own `path` argument (falling back to the chat
  // workspace root) before the viewer opens. Computed unconditionally — hooks
  // must not sit behind the early returns below.
  const listedDir = extractToolPathArg(parsedArgs, args)
  const resolveEntry = useMemo(
    () => makeWorkspacePathResolver(listedDir),
    [listedDir],
  )

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
        const parsed = parseListEntry(entry)
        if (!parsed || parsed.kind === 'dir') {
          // Directories and unparsed lines (truncation markers) carry no
          // link — the full raw line stays the display text.
          return (
            <div key={entry} className="text-xs truncate text-muted-foreground font-mono">
              {entry}
            </div>
          )
        }
        // The kind/size columns stay part of the DISPLAY (the raw line) but
        // never leak into the link: the click opens exactly the entry name.
        return (
          <div key={entry} className="text-xs truncate">
            <FileLink path={parsed.name} label={entry} resolve={resolveEntry} />
          </div>
        )
      })}
      {hasMore && !showAll && (
        <button
          onClick={(e) => { e.stopPropagation(); setShowAll(true) }}
          className="text-xs text-muted-foreground hover:text-foreground hover:bg-accent/50 rounded px-1 py-0.5 mt-1 transition-colors"
          title={`Show all ${entries.length} entries`}
        >
          Show all ({entries.length} entries)
        </button>
      )}
    </div>
  )
})

