import { useState } from 'react'
import { ChevronDown } from 'lucide-react'
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

interface CommitOutputSectionProps {
  /** Bounded combined stdout+stderr of the last successful commit (may be empty). */
  output: string | null
}

/**
 * The collapsed-by-default "hook output" section under the commit success
 * banner: the bounded combined stdout+stderr of the most recent commit —
 * hook and signing output for trusted repositories (their own hooks run).
 * The output outlives the banner's 4s auto-dismissal (the store keeps it
 * until the next commit replaces it), so a log the user expands later never
 * vanishes mid-read.
 */
export function CommitOutputSection({ output }: CommitOutputSectionProps) {
  const [isOpen, setIsOpen] = useState(false)
  if (output === null || output.length === 0) return null

  return (
    <Collapsible open={isOpen} onOpenChange={setIsOpen} className="mt-1.5">
      <CollapsibleTrigger
        className="flex w-fit items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
        data-testid="commit-output-toggle"
      >
        <ChevronDown className={cn('size-3 transition-transform', isOpen && 'rotate-180')} />
        hook output
      </CollapsibleTrigger>
      <CollapsibleContent data-testid="commit-output-content">
        <pre
          className="custom-scrollbar mt-1 max-h-32 overflow-auto rounded-md border border-border bg-muted/40 p-2 font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-words"
        >
          {output}
        </pre>
      </CollapsibleContent>
    </Collapsible>
  )
}
