import type { Components } from 'react-markdown'
import { defaultSchema } from 'rehype-sanitize'
import { cn } from '@/lib/utils'
import { MermaidBlock } from '@/components/chat/MermaidBlock'

// Custom sanitize schema that allows highlight.js classes, heading IDs, and link attributes
export const customSchema = {
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

// Shared markdown component overrides for ReactMarkdown
export const markdownComponents: Components = {
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
}
