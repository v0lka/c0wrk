import { useState, useMemo } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkEmoji from 'remark-emoji'
import remarkBreaks from 'remark-breaks'
import rehypeHighlight from 'rehype-highlight'
import rehypeSanitize from 'rehype-sanitize'
import rehypeExternalLinks from 'rehype-external-links'
import rehypeSlug from 'rehype-slug'
import rehypeAutolinkHeadings from 'rehype-autolink-headings'
import hljs from 'highlight.js/lib/core'
import markdown from 'highlight.js/lib/languages/markdown'
import { Code, Eye } from 'lucide-react'
import { customSchema, markdownComponents } from '@/lib/markdownConfig'
import { ErrorBoundary } from '@/components/ErrorBoundary'

// Register markdown language for raw mode highlighting
hljs.registerLanguage('markdown', markdown)

interface AssistantMessageProps {
  content: string
  isStreaming?: boolean
}

export function AssistantMessage({ content, isStreaming }: AssistantMessageProps) {
  const [showRaw, setShowRaw] = useState(false)

  // Memoize highlighted markdown for raw mode
  const highlightedMarkdown = useMemo(() => {
    try {
      return hljs.highlight(content, { language: 'markdown' }).value
    } catch {
      return content
    }
  }, [content])

  return (
    <div className="flex-1 min-w-0 overflow-hidden">
        <div className="relative group">
          {/* Toggle button - only show when not streaming */}
          {!isStreaming && (
            <button
              onClick={() => setShowRaw(!showRaw)}
              className="absolute right-2 top-2 z-10 flex items-center justify-center w-6 h-6 rounded opacity-0 group-hover:opacity-100 transition-opacity hover:bg-muted"
              title={showRaw ? 'View rendered' : 'View source'}
              aria-label={showRaw ? 'View rendered content' : 'View raw source'}
            >
              {showRaw ? (
                <Eye className="h-3 w-3 text-muted-foreground" />
              ) : (
                <Code className="h-3 w-3 text-muted-foreground" />
              )}
            </button>
          )}

          {showRaw ? (
            <pre className="font-mono text-sm whitespace-pre-wrap break-words text-foreground overflow-auto max-w-full min-w-0 bg-background border border-border rounded-lg px-4 py-3">
              <code
                className="hljs language-markdown"
                dangerouslySetInnerHTML={{ __html: highlightedMarkdown }}
              />
            </pre>
          ) : (
            <div className="prose prose-sm prose-stone dark:prose-invert max-w-full break-words min-w-0 bg-background border border-border rounded-lg px-4 py-3">
              <ErrorBoundary fallback={<div className="text-sm text-muted-foreground italic px-4 py-3">Failed to render message content</div>}>
              <ReactMarkdown
                remarkPlugins={[remarkGfm, remarkEmoji, remarkBreaks]}
                rehypePlugins={[
                  rehypeSlug,
                  [rehypeAutolinkHeadings, { behavior: 'wrap' }],
                  rehypeHighlight,
                  [rehypeExternalLinks, { target: '_blank', rel: ['noopener', 'noreferrer'] }],
                  [rehypeSanitize, customSchema],
                ]}
                components={markdownComponents}
              >
                {content}
              </ReactMarkdown>
              </ErrorBoundary>
            </div>
          )}
        </div>
        
        {/* Streaming cursor */}
        {isStreaming && (
          <span className="inline-block w-2 h-4 bg-primary ml-1 animate-pulse" />
        )}
    </div>
  )
}
