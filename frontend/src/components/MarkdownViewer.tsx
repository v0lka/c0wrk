import { useState } from 'react'
import { Code, Eye } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Markdown } from '@/lib/markdownConfig'

interface MarkdownViewerProps {
  content: string
  className?: string
}

export function MarkdownViewer({ content, className }: MarkdownViewerProps) {
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
}
