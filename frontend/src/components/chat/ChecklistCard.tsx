import { useState } from 'react'
import { CheckSquare, Square, ListChecks, ChevronDown, ChevronRight } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { DisplayItem } from '@/types/messages'

type ChecklistItem = Extract<DisplayItem, { kind: 'checklist' }>

export function ChecklistCard({ item }: { item: ChecklistItem }) {
  const completed = item.items.filter(i => i.checked).length
  const allDone = completed === item.items.length
  const [open, setOpen] = useState(true)

  return (
    <div
      className={cn(
        'rounded-md border px-3 py-2 group',
        allDone ? 'border-success/30 bg-success/5' : 'border-info/30 bg-info/5',
      )}
      data-checklist-step={item.stepId ?? undefined}
    >
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-1.5 w-full text-left text-xs font-medium text-muted-foreground hover:text-foreground transition-colors"
      >
        <span className="opacity-0 group-hover:opacity-100 transition-opacity inline-flex shrink-0">
          {open
            ? <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
            : <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />}
        </span>
        <ListChecks className="h-3.5 w-3.5 shrink-0" />
        <span>Checklist</span>
        <span className="ml-auto text-muted-foreground/60">{completed}/{item.items.length}</span>
      </button>
      {open && (
        <ul className="mt-1.5 space-y-1">
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
      )}
    </div>
  )
}
