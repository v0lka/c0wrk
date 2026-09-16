import { memo, useState } from 'react'
import { Code, Eye } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Markdown } from '@/lib/markdownConfig'

interface MarkdownViewerProps {
  content: string
  className?: string
}

/**
 * Memoized so an unchanged message does not re-render (and re-parse) its
 * Markdown when a sibling changes. The comparator is on the plain `content`
 * string so it is independent of the parent rebuilding anything.
 */
export const MarkdownViewer = memo(
  function MarkdownViewer({ content, className }: MarkdownViewerProps) {
    const [showSource, setShowSource] = useState(false)

    return (
      <div className={className}>
        {showSource ? (
          <>
            <Button
              variant="ghost"
              size="icon-xs"
              onClick={() => setShowSource(false)}
              className="float-right ml-3 mb-3"
              title="Preview"
              aria-label="Switch to preview"
            >
              <Eye className="size-4" />
            </Button>
            <pre className="whitespace-pre-wrap font-mono text-sm m-0 text-foreground">{content}</pre>
          </>
        ) : (
          <>
            <Button
              variant="ghost"
              size="icon-xs"
              onClick={() => setShowSource(true)}
              className="float-right ml-3 mb-3"
              title="Source"
              aria-label="View source"
            >
              <Code className="size-4" />
            </Button>
            <Markdown content={content} />
          </>
        )}
      </div>
    )
  },
  (prev, next) => prev.content === next.content && prev.className === next.className,
)
