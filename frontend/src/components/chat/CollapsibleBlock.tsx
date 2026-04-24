import { useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

interface CollapsibleBlockProps {
  icon?: React.ReactNode
  label: React.ReactNode
  statusIcon?: React.ReactNode
  badge?: React.ReactNode
  defaultOpen?: boolean
  open?: boolean
  onOpenChange?: (open: boolean) => void
  className?: string
  children: React.ReactNode
  headerExtra?: React.ReactNode
}

export function CollapsibleBlock({
  icon,
  label,
  statusIcon,
  badge,
  defaultOpen,
  open: controlledOpen,
  onOpenChange,
  className,
  children,
  headerExtra,
}: CollapsibleBlockProps) {
  const [uncontrolledOpen, setUncontrolledOpen] = useState(defaultOpen ?? false)

  const isControlled = controlledOpen !== undefined
  const isOpen = isControlled ? controlledOpen : uncontrolledOpen
  const setOpen = isControlled ? onOpenChange : setUncontrolledOpen

  return (
    <Collapsible
      open={isOpen}
      onOpenChange={setOpen}
      className={cn('group', className)}
    >
      <CollapsibleTrigger className="flex items-center gap-1.5 text-muted-foreground hover:text-foreground transition-colors">
        <span className="opacity-0 group-hover:opacity-100 transition-opacity inline-flex">
          {isOpen ? (
            <ChevronDown className="h-3.5 w-3.5" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5" />
          )}
        </span>
        {statusIcon}
        {icon}
        <span className="text-sm min-w-0 overflow-hidden">{label}</span>
        {badge}
        {headerExtra}
      </CollapsibleTrigger>
      <CollapsibleContent>
        {children}
      </CollapsibleContent>
    </Collapsible>
  )
}
