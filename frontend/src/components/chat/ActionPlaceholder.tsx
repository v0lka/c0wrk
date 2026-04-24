import { Clock } from 'lucide-react'
import type { DisplayItem } from '@/types/messages'

interface ActionPlaceholderProps {
  item: Extract<DisplayItem, { kind: 'action_placeholder' }>
}

export function ActionPlaceholder({ item }: ActionPlaceholderProps) {
  return (
    <div className="flex items-center gap-1.5 text-muted-foreground text-sm py-1">
      <Clock className="h-3.5 w-3.5 animate-pulse" />
      <span>{item.label}</span>
    </div>
  )
}
