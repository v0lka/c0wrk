import { useState } from 'react'

interface TruncatedContentProps {
  content: string
  maxLines?: number
  className?: string
}

export function TruncatedContent({ content, maxLines = 20, className }: TruncatedContentProps) {
  const [expanded, setExpanded] = useState(false)

  const lines = content.split('\n')
  const isLong = lines.length > maxLines
  const display = !expanded && isLong ? lines.slice(0, maxLines).join('\n') : content

  return (
    <div className={className}>
      <pre className="font-mono text-xs text-muted-foreground whitespace-pre-wrap break-all">
        {display}
        {!expanded && isLong && <span className="text-muted-foreground/50">...</span>}
      </pre>
      {isLong && (
        <button
          onClick={(e) => { e.stopPropagation(); setExpanded(!expanded) }}
          className="text-xs text-muted-foreground hover:text-foreground hover:bg-accent/50 rounded px-1 py-0.5 mt-1 transition-colors"
        >
          {expanded ? 'Show less' : `Show all (${lines.length} lines)`}
        </button>
      )}
    </div>
  )
}
