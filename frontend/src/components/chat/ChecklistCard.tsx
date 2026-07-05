import { CheckSquare, Square, ListChecks } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { DisplayItem } from '@/types/messages'

type ChecklistItem = Extract<DisplayItem, { kind: 'checklist' }>

export function ChecklistCard({ item }: { item: ChecklistItem }) {
  const completed = item.items.filter(i => i.checked).length
  const allDone = completed === item.items.length

  return (
    <div
      className={cn(
        'rounded-md border px-3 py-2 space-y-1.5',
        allDone ? 'border-success/30 bg-success/5' : 'border-info/30 bg-info/5',
      )}
      data-checklist-step={item.stepId ?? undefined}
    >
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <ListChecks className="h-3.5 w-3.5 shrink-0" />
        <span>Checklist</span>
        <span className="ml-auto text-muted-foreground/60">{completed}/{item.items.length}</span>
      </div>
      <ul className="space-y-1">
        {item.items.map((todo, i) => (
          <li key={`${todo.text}-${i}`} className="flex items-center gap-2 text-xs text-muted-foreground">
            {todo.checked ? (
              <CheckSquare className="h-3.5 w-3.5 text-success shrink-0" />
            ) : (
              <Square className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
            )}
            <span className={cn(todo.checked && 'line-through opacity-60')}>{todo.text}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}
