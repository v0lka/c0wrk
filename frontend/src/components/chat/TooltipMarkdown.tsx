import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkEmoji from 'remark-emoji'
import remarkBreaks from 'remark-breaks'
import rehypeHighlight from 'rehype-highlight'
import rehypeSanitize from 'rehype-sanitize'
import rehypeExternalLinks from 'rehype-external-links'
import rehypeSlug from 'rehype-slug'
import rehypeAutolinkHeadings from 'rehype-autolink-headings'
import type { Components } from 'react-markdown'
import { cn } from '@/lib/utils'
import { customSchema } from '@/lib/markdownConfig'
import { ErrorBoundary } from '@/components/ErrorBoundary'

// Markdown component overrides for tooltip rendering — no explicit size classes
// so prose-xs sizing (12px base) cascades through em units correctly.
const tooltipMarkdownComponents: Components = {
  code({ className, children, ...props }) {
    const match = /language-(\w+)/.exec(className || '')
    const isInline = !match && !className

    if (isInline) {
      return (
        <code
          className="bg-muted px-1 py-0.5 rounded font-mono"
          {...props}
        >
          {children}
        </code>
      )
    }

    return (
      <div className="relative group">
        <pre className="bg-background border border-border rounded-md p-2 overflow-x-auto max-w-full min-w-0">
          <code
            className={cn(
              'font-mono block',
              match ? `language-${match[1]}` : ''
            )}
            {...props}
          >
            {children}
          </code>
        </pre>
      </div>
    )
  },
  pre({ children }) {
    return <>{children}</>
  },
}

interface TooltipMarkdownProps {
  content: string
}

export function TooltipMarkdown({ content }: TooltipMarkdownProps) {
  return (
    <div className="prose prose-sm prose-neutral dark:prose-invert prose-xs max-w-full break-words min-w-0">
      <ErrorBoundary fallback={<span>{content}</span>}>
        <ReactMarkdown
          remarkPlugins={[remarkGfm, remarkEmoji, remarkBreaks]}
          rehypePlugins={[
            rehypeSlug,
            [rehypeAutolinkHeadings, { behavior: 'wrap' }],
            rehypeHighlight,
            [rehypeExternalLinks, { target: '_blank', rel: ['noopener', 'noreferrer'] }],
            [rehypeSanitize, customSchema],
          ]}
          components={tooltipMarkdownComponents}
        >
          {content}
        </ReactMarkdown>
      </ErrorBoundary>
    </div>
  )
}
