import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import { cn } from '@/lib/utils'

interface AssistantMessageProps {
  content: string
  isStreaming?: boolean
}

export function AssistantMessage({ content, isStreaming }: AssistantMessageProps) {
  return (
    <div className="flex-1 min-w-0 overflow-hidden">
        <div className="prose prose-sm dark:prose-invert max-w-full break-words min-w-0">
          <ReactMarkdown
            remarkPlugins={[remarkGfm]}
            rehypePlugins={[rehypeHighlight]}
            components={{
              code({ className, children, ...props }) {
                const match = /language-(\w+)/.exec(className || '')
                const isInline = !match && !className
                
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
        </div>
        
        {/* Streaming cursor */}
        {isStreaming && (
          <span className="inline-block w-2 h-4 bg-primary ml-1 animate-pulse" />
        )}
    </div>
  )
}
