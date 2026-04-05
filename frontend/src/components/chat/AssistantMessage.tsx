import { Component, useState, useMemo } from 'react'
import type { ReactNode, ErrorInfo } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkEmoji from 'remark-emoji'
import remarkBreaks from 'remark-breaks'
import rehypeHighlight from 'rehype-highlight'
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize'
import rehypeExternalLinks from 'rehype-external-links'
import rehypeSlug from 'rehype-slug'
import rehypeAutolinkHeadings from 'rehype-autolink-headings'
import hljs from 'highlight.js/lib/core'
import markdown from 'highlight.js/lib/languages/markdown'
import { Code, Eye } from 'lucide-react'
import { cn } from '@/lib/utils'
import { MermaidBlock } from './MermaidBlock'

// Register markdown language for raw mode highlighting
hljs.registerLanguage('markdown', markdown)

// Custom sanitize schema that allows highlight.js classes, heading IDs, and link attributes
const customSchema = {
  ...defaultSchema,
  attributes: {
    ...defaultSchema.attributes,
    code: ['className'],
    span: ['className'],
    pre: ['className'],
    div: ['className'],
    h1: ['id'],
    h2: ['id'],
    h3: ['id'],
    h4: ['id'],
    h5: ['id'],
    h6: ['id'],
    a: ['href', 'target', 'rel', 'className'],
  },
}

interface AssistantMessageProps {
  content: string
  isStreaming?: boolean
}

class MarkdownErrorBoundary extends Component<
  { children: ReactNode },
  { hasError: boolean }
> {
  constructor(props: { children: ReactNode }) {
    super(props)
    this.state = { hasError: false }
  }

  static getDerivedStateFromError(): { hasError: boolean } {
    return { hasError: true }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Markdown render error:', error, info)
  }

  render() {
    if (this.state.hasError) {
      return (
        <div className="text-sm text-muted-foreground italic px-4 py-3">
          Failed to render message content
        </div>
      )
    }
    return this.props.children
  }
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
            <pre className="font-mono text-sm whitespace-pre-wrap break-words text-foreground overflow-auto max-w-full min-w-0 bg-zinc-100 dark:bg-zinc-800 rounded-lg px-4 py-3">
              <code
                className="hljs language-markdown"
                dangerouslySetInnerHTML={{ __html: highlightedMarkdown }}
              />
            </pre>
          ) : (
            <div className="prose prose-sm dark:prose-invert max-w-full break-words min-w-0 bg-zinc-100 dark:bg-zinc-800 rounded-lg px-4 py-3">
              <MarkdownErrorBoundary>
              <ReactMarkdown
                remarkPlugins={[remarkGfm, remarkEmoji, remarkBreaks]}
                rehypePlugins={[
                  rehypeSlug,
                  [rehypeAutolinkHeadings, { behavior: 'wrap' }],
                  rehypeHighlight,
                  [rehypeExternalLinks, { target: '_blank', rel: ['noopener', 'noreferrer'] }],
                  [rehypeSanitize, customSchema],
                ]}
                components={{
                  code({ className, children, ...props }) {
                    const match = /language-(\w+)/.exec(className || '')
                    const isInline = !match && !className
                    const codeContent = String(children).replace(/\n$/, '')
                    
                    if (isInline) {
                      return (
                        <code
                          className="bg-muted px-1.5 py-0.5 rounded text-sm font-mono"
                          {...props}
                        >
                          {children}
                        </code>
                      )
                    }
                    
                    // Check for mermaid diagram
                    if (match?.[1] === 'mermaid') {
                      return <MermaidBlock code={codeContent} />
                    }
                    
                    return (
                      <div className="relative group">
                        <div className="absolute right-2 top-2 opacity-0 group-hover:opacity-100 transition-opacity">
                          <span className="text-xs text-muted-foreground uppercase">
                            {match?.[1] || 'text'}
                          </span>
                        </div>
                        <pre className="bg-muted rounded-lg p-4 overflow-x-auto max-w-full min-w-0">
                          <code
                            className={cn(
                              'text-sm font-mono block',
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
                }}
              >
                {content}
              </ReactMarkdown>
              </MarkdownErrorBoundary>
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
