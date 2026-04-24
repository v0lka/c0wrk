import { Markdown } from '@/lib/markdownConfig'
import { ErrorBoundary } from '@/components/ErrorBoundary'

interface TooltipMarkdownProps {
  content: string
}

export function TooltipMarkdown({ content }: TooltipMarkdownProps) {
  return (
    <ErrorBoundary fallback={<span>{content}</span>}>
      <Markdown content={content} compact />
    </ErrorBoundary>
  )
}
