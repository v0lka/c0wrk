import React, { useMemo, useState } from 'react'
import type { ToolBodyProps } from '../toolCardRegistry'
import { FileLink } from '../shared/FileLink'
import { extractToolPathArg, makeWorkspacePathResolver } from '../shared/workspacePathResolver'

const MAX_ENTRIES = 15

export const SearchBody = React.memo(function SearchBody({ toolName, parsedArgs, args, result, status }: ToolBodyProps) {
  const [showAll, setShowAll] = useState(false)

  // glob entries are relative to the SEARCH ROOT (the call's own `path`
  // argument); ripgrep emits absolute paths. The resolver joins the search
  // root for the relative shapes and normalizes absolutes in place.
  const searchDir = extractToolPathArg(parsedArgs, args)
  const resolvePath = useMemo(
    () => makeWorkspacePathResolver(searchDir),
    [searchDir],
  )

  if (status === 'running') {
    return (
      <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0">
        <span className="text-xs text-muted-foreground italic">Searching...</span>
      </div>
    )
  }

  if (!result) return null

  // An errored call's result text is a diagnostic ("glob error: malformed
  // pattern …", "path error: …"), never path data — render it plain so no
  // error line becomes a file link.
  const entries: Entry[] = status === 'error'
    ? result.split('\n').filter(l => l.trim()).map(line => ({ display: line }))
    : parseEntries(result, toolName ?? '')
  const visible = showAll ? entries : entries.slice(0, MAX_ENTRIES)
  const hasMore = entries.length > MAX_ENTRIES

  return (
    <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0 space-y-0.5">
      {visible.map((entry) => (
        <div key={entry.display} className="text-xs truncate">
          {entry.path ? (
            <FileLink path={entry.path} line={entry.line} label={entry.display} resolve={resolvePath} />
          ) : (
            <span className="text-muted-foreground font-mono">{entry.display}</span>
          )}
        </div>
      ))}
      {hasMore && !showAll && (
        <button
          onClick={(e) => { e.stopPropagation(); setShowAll(true) }}
          className="text-xs text-muted-foreground hover:text-foreground hover:bg-accent/50 rounded px-1 py-0.5 mt-1 transition-colors"
          title={`Show all ${entries.length} results`}
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
  // ripgrep format: path:line:content or path-line-content (context lines).
  // The tool resolves its search root to an absolute path before invoking
  // rg, so the paths arrive absolute; anything that does not match the
  // shape (the trailing "Found N matches…" summary, notice lines) renders
  // as plain text.
  if (toolName === 'ripgrep') {
    return lines.map(line => {
      const match = line.match(/^(.+?):(\d+)[:-](.*)$/)
      if (match) return { path: match[1]!, line: parseInt(match[2]!, 10), display: line }
      return { display: line }
    })
  }
  // glob format: one path per line, relative to the search root. A bounded
  // walk appends "warning: …" lines after a blank separator — never linked.
  // A trailing "/" marks a directory entry (the tool's marker for
  // type: dirs|all — the viewer reads files only), and the fixed
  // zero-match summary is not a path: both render plain.
  if (toolName === 'glob') {
    return lines.map(line => {
      if (line.startsWith('warning:') || line.endsWith('/') || line === 'no matching files found') {
        return { display: line }
      }
      return { path: line, display: line }
    })
  }
  // Unknown producers: keep the legacy heuristic — an absolute-looking line
  // is a bare path; anything else renders as plain text.
  return lines.map(line =>
    line.startsWith('/') || line.startsWith('./')
      ? { path: line.replace(/\/$/, ''), display: line }
      : { display: line },
  )
}

