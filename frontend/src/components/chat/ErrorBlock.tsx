import { memo } from 'react'
import { AlertCircle } from 'lucide-react'
import type { DisplayItem } from '@/types/messages'
import { areDisplayItemsEqual } from '@/lib/displayItemStability'

type ErrorItem = Extract<DisplayItem, { kind: 'error' }>

interface ErrorBlockProps {
  item: ErrorItem
}

/** Memoized so an unchanged error is not re-rendered when a sibling changes. */
export const ErrorBlock = memo(
  function ErrorBlock({ item }: ErrorBlockProps) {
    return (
      <div className="flex items-start gap-2 min-w-0">
        <AlertCircle className="h-3.5 w-3.5 text-destructive shrink-0 mt-0.5" />
        <p className="text-sm text-destructive/80 whitespace-pre-wrap">{item.message.content}</p>
      </div>
    )
  },
  (prev, next) => prev.item === next.item || areDisplayItemsEqual(prev.item, next.item),
)
