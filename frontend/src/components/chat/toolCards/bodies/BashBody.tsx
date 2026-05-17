import React, { useMemo } from 'react'
import { AnsiUp } from 'ansi_up'
import type { ToolBodyProps } from '../toolCardRegistry'
import { TruncatedContent } from '../shared/TruncatedContent'

const ansiConverter = new AnsiUp()
ansiConverter.use_classes = false

export const BashBody = React.memo(function BashBody({ result, status }: ToolBodyProps) {
  const html = useMemo(() => {
    if (!result) return ''
    return ansiConverter.ansi_to_html(result)
  }, [result])

  if (status === 'running') {
    return (
      <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0">
        <span className="text-xs text-muted-foreground italic">Running...</span>
      </div>
    )
  }

  if (!result) return null

  const lines = result.split('\n')
  if (lines.length <= 20) {
    return (
      <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0">
        <pre
          className="font-mono text-xs text-muted-foreground whitespace-pre-wrap break-all"
          dangerouslySetInnerHTML={{ __html: html }}
        />
      </div>
    )
  }

  return (
    <div className="mt-2 border-l-2 border-border bg-muted/30 rounded p-3 min-w-0">
      <TruncatedContent content={result} maxLines={20} />
    </div>
  )
})
