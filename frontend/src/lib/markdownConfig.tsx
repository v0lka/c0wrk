import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkEmoji from 'remark-emoji'
import remarkBreaks from 'remark-breaks'
import rehypeSlug from 'rehype-slug'
import rehypeAutolinkHeadings from 'rehype-autolink-headings'
import rehypeHighlight from 'rehype-highlight'
import rehypeExternalLinks from 'rehype-external-links'
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize'
import { cn } from '@/lib/utils'
import { isLocalFileHref, parseLocalFileHref, normalizePath } from '@/lib/localFileLink'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useFileTreeStore } from '@/stores/fileTreeStore'
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

// --- Custom link component for local file navigation ---

const markdownComponents: Components = {
  a: ({ href, children, node: _node, ...rest }) => {
    if (!isLocalFileHref(href)) {
      return <a href={href} {...rest}>{children}</a>
    }

    const handleClick = () => {
      const { path, line } = parseLocalFileHref(href!)
      const rootPath = useFileTreeStore.getState().rootPath
      if (!rootPath) return
      const resolved = normalizePath(rootPath, path)
      if (line !== undefined) {
        useFileViewerStore.getState().openFileAtLine(resolved, line)
      } else {
        useFileViewerStore.getState().openFile(resolved)
      }
    }

    return (
      <span
        className="text-info hover:underline cursor-pointer"
        onClick={handleClick}
        role="link"
        tabIndex={0}
        onKeyDown={(e) => { if (e.key === 'Enter') handleClick() }}
      >
        {children}
      </span>
    )
  },
}

// --- Markdown wrapper component ---

interface MarkdownProps {
  content: string
  className?: string
  compact?: boolean
}

export function Markdown({ content, className, compact }: MarkdownProps) {
  return (
    <div className={cn('prose prose-sm max-w-none', compact && 'prose-xs', className)}>
      <ReactMarkdown
        remarkPlugins={remarkPlugins}
        rehypePlugins={rehypePlugins}
        components={markdownComponents}
      >
        {content}
      </ReactMarkdown>
    </div>
  )
}

