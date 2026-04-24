import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { Code, Eye } from 'lucide-react'
import remarkGfm from 'remark-gfm'
import remarkEmoji from 'remark-emoji'
import remarkBreaks from 'remark-breaks'
import rehypeSlug from 'rehype-slug'
import rehypeAutolinkHeadings from 'rehype-autolink-headings'
import rehypeHighlight from 'rehype-highlight'
import rehypeExternalLinks from 'rehype-external-links'
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize'
import { cn } from '@/lib/utils'
import type { PluggableList } from 'unified'

// Custom sanitize schema — extends default to allow highlight.js classes,
// heading IDs, task list inputs, and mermaid container styles.
const customSanitizeSchema = {
  ...defaultSchema,
  attributes: {
    ...defaultSchema.attributes,
    code: ['className'],
    pre: ['className'],
    span: ['className'],
    div: ['className', 'style'],
    h1: ['id'],
    h2: ['id'],
    h3: ['id'],
    h4: ['id'],
    h5: ['id'],
    h6: ['id'],
    a: ['href', 'target', 'rel', 'className'],
    input: ['type', 'checked', 'disabled'],
  },
}

const remarkPlugins: PluggableList = [remarkGfm, remarkEmoji, remarkBreaks]

const rehypePlugins: PluggableList = [
  rehypeSlug,
  [rehypeAutolinkHeadings, { behavior: 'wrap' }],
  rehypeHighlight,
  [rehypeExternalLinks, { target: '_blank', rel: ['noopener', 'noreferrer'] }],
  [rehypeSanitize, customSanitizeSchema],
]

// --- Markdown wrapper component ---

interface MarkdownProps {
  content: string
  className?: string
  compact?: boolean
  showSourceToggle?: boolean
}

export function Markdown({ content, className, compact, showSourceToggle }: MarkdownProps) {
  const [showSource, setShowSource] = useState(false)

  if (showSource && showSourceToggle) {
    return (
      <div>
        <button
          onClick={() => setShowSource(false)}
          className="float-right ml-3 mb-3 p-1.5 rounded text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors"
          title="Preview"
        >
          <Eye className="size-4" />
        </button>
        <pre className="whitespace-pre-wrap font-mono text-sm m-0 text-foreground">{content}</pre>
      </div>
    )
  }

  return (
    <div className={cn('prose prose-sm max-w-none', compact && 'prose-xs', className)}>
      {showSourceToggle && (
        <button
          onClick={() => setShowSource(true)}
          className="float-right ml-3 mb-3 p-1.5 rounded text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors"
          title="Source"
        >
          <Code className="size-4" />
        </button>
      )}
      <ReactMarkdown
        remarkPlugins={remarkPlugins}
        rehypePlugins={rehypePlugins}
      >
        {content}
      </ReactMarkdown>
    </div>
  )
}

