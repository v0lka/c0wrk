import React, { useState } from 'react'
import type { ToolBodyProps } from '../toolCardRegistry'
import { FileLink } from '../shared/FileLink'

const MAX_ENTRIES = 15

export const SearchBody = React.memo(function SearchBody({ result, status, parsedArgs, args }: ToolBodyProps) {
  const [showAll, setShowAll] = useState(false)

  if (status === 'running') {
    return (
      <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0">
        <span className="text-xs text-muted-foreground italic">Searching...</span>
      </div>
    )
  }

  if (!result) return null

  const parsed = parsedArgs ?? tryParse(args)
  const toolName = getToolName(parsed)
  const entries = parseEntries(result, toolName)
  const visible = showAll ? entries : entries.slice(0, MAX_ENTRIES)
  const hasMore = entries.length > MAX_ENTRIES

  return (
    <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0 space-y-0.5">
      {visible.map((entry) => (
        <div key={entry.display} className="text-xs truncate">
          {entry.path ? (
            <FileLink path={entry.path} line={entry.line} label={entry.display} />
          ) : (
            <span className="text-muted-foreground font-mono">{entry.display}</span>
          )}
        </div>
      ))}
      {hasMore && !showAll && (
        <button
          onClick={(e) => { e.stopPropagation(); setShowAll(true) }}
          className="text-xs text-muted-foreground hover:text-foreground hover:bg-accent/50 rounded px-1 py-0.5 mt-1 transition-colors"
        >
          Show all ({entries.length} results)
        </button>
      )}
    </div>
  )
})

interface Entry { path?: string; line?: number; display: string }

function parseEntries(result: string, toolName: string): Entry[] {
  const lines = result.split('\n').filter(l => l.trim())
  return lines.map(line => {
    // ripgrep format: path:line:content or path:line-content
    if (toolName === 'ripgrep') {
      const match = line.match(/^(.+?):(\d+)[:-](.*)$/)
      if (match) return { path: match[1]!, line: parseInt(match[2]!, 10), display: line }
    }
    // glob/list format: just a path
    if (line.startsWith('/') || line.startsWith('./')) {
      return { path: line.replace(/\/$/, ''), display: line }
    }
    return { display: line }
  })
}

function getToolName(parsed: Record<string, unknown> | undefined): string {
  return (parsed?._toolName as string) || ''
}

function tryParse(s: string): Record<string, unknown> | undefined {
  try { return JSON.parse(s) } catch { return undefined }
}
