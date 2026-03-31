import { GitBranch, RotateCcw, ArrowUpRight, ListChecks } from 'lucide-react'

interface ServiceMessageProps {
  id: string
  variant: 'routing' | 'retry' | 'escalation' | 'ac_extracted'
  content: string
  metadata?: Record<string, unknown>
}

const variantConfig = {
  routing: {
    icon: GitBranch,
    label: 'Routing',
  },
  retry: {
    icon: RotateCcw,
    label: 'Retry',
  },
  escalation: {
    icon: ArrowUpRight,
    label: 'Escalation',
  },
  ac_extracted: {
    icon: ListChecks,
    label: 'AC Extracted',
  },
}

export function ServiceMessage({ variant, content }: ServiceMessageProps) {
  const config = variantConfig[variant]
  const Icon = config.icon

  return (
    <div className="flex items-center gap-1.5 text-muted-foreground">
      <Icon className="h-3.5 w-3.5" />
      <span className="text-xs">{content}</span>
    </div>
  )
}
