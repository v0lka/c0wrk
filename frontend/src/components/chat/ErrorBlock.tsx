import { AlertCircle } from 'lucide-react'

interface ErrorBlockProps {
  content: string
}

export function ErrorBlock({ content }: ErrorBlockProps) {
  return (
    <div className="flex items-start gap-2 min-w-0">
      <AlertCircle className="h-3.5 w-3.5 text-red-500 shrink-0 mt-0.5" />
      <p className="text-sm text-red-400 whitespace-pre-wrap">{content}</p>
    </div>
  )
}
