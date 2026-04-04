import { Clock } from 'lucide-react'

interface ActionPlaceholderProps {
  label: string
}

export function ActionPlaceholder({ label }: ActionPlaceholderProps) {
  return (
    <div className="flex items-center gap-1.5 text-muted-foreground text-sm py-1">
      <Clock className="h-3.5 w-3.5 animate-pulse" />
      <span>{label}</span>
    </div>
  )
}
