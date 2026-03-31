import { Lightbulb, RotateCcw, AlertCircle, CheckCircle2, ArrowRight, Info } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { useInspectorStore } from '@/stores/inspectorStore'

// View-specific reflection type (matches store shape)
interface ReflectionView {
  id: string
  attemptNumber: number
  summary: string
  insights: string[]
  suggestedAction: string
  actionType: 'retry' | 'modify' | 'continue' | 'abort'
  timestamp: number
}

// Placeholder data - will be populated by events
// const placeholderReflections: Reflection[] = [
//   {
//     id: '1',
//     attemptNumber: 1,
//     summary: 'Initial approach did not handle edge cases properly',
//     suggestedAction: 'Add validation for null inputs',
//     actionType: 'modify',
//     timestamp: Date.now() - 3600000,
//   },
//   {
//     id: '2',
//     attemptNumber: 2,
//     summary: 'Tests are passing but code could be more efficient',
//     suggestedAction: 'Optimize the loop structure',
//     actionType: 'continue',
//     timestamp: Date.now() - 1800000,
//   },
// ]

function ActionBadge({ type }: { type: ReflectionView['actionType'] }) {
  switch (type) {
    case 'retry':
      return (
        <Badge variant="secondary" className="text-xs bg-blue-500/10 text-blue-500 hover:bg-blue-500/20">
          <RotateCcw className="h-3 w-3 mr-1" />
          Retry
        </Badge>
      )
    case 'modify':
      return (
        <Badge variant="secondary" className="text-xs bg-amber-500/10 text-amber-500 hover:bg-amber-500/20">
          <AlertCircle className="h-3 w-3 mr-1" />
          Modify
        </Badge>
      )
    case 'continue':
      return (
        <Badge variant="secondary" className="text-xs bg-green-500/10 text-green-500 hover:bg-green-500/20">
          <CheckCircle2 className="h-3 w-3 mr-1" />
          Continue
        </Badge>
      )
    case 'abort':
      return (
        <Badge variant="secondary" className="text-xs bg-red-500/10 text-red-500 hover:bg-red-500/20">
          <AlertCircle className="h-3 w-3 mr-1" />
          Abort
        </Badge>
      )
  }
}

function ReflectionItem({ reflection, isLast }: { reflection: ReflectionView; isLast: boolean }) {
  return (
    <div className="relative pl-6 pb-4">
      {/* Timeline connector */}
      {!isLast && (
        <div className="absolute left-[9px] top-6 bottom-0 w-px bg-border" />
      )}
      
      {/* Timeline dot */}
      <div className="absolute left-0 top-1 w-5 h-5 rounded-full bg-muted border border-border flex items-center justify-center">
        <span className="text-[10px] font-medium text-muted-foreground">
          {reflection.attemptNumber}
        </span>
      </div>
      
      {/* Content */}
      <div className="border border-border rounded-lg p-3 bg-card">
        <div className="flex items-start justify-between gap-2 mb-2">
          <span className="text-xs text-muted-foreground">
            Attempt #{reflection.attemptNumber}
          </span>
          <ActionBadge type={reflection.actionType} />
        </div>
        
        <p className="text-sm mb-2">{reflection.summary}</p>
        
        {reflection.insights && reflection.insights.length > 0 && (
          <div className="mb-2 space-y-1">
            {reflection.insights.map((insight, idx) => (
              <div key={idx} className="flex items-start gap-2 text-xs text-muted-foreground">
                <Info className="h-3 w-3 mt-0.5 shrink-0" />
                <span>{insight}</span>
              </div>
            ))}
          </div>
        )}
        
        <div className="flex items-center gap-2 text-xs text-muted-foreground bg-muted/50 rounded-md px-2 py-1.5">
          <ArrowRight className="h-3 w-3 shrink-0" />
          <span>{reflection.suggestedAction}</span>
        </div>
      </div>
    </div>
  )
}

export function ReflectionsView() {
  // Get reflections from store
  const storeReflections = useInspectorStore((s) => s.reflections)
  
  // Map store reflections to view format (they already match)
  const reflections: ReflectionView[] = storeReflections.map((r) => ({
    id: r.id,
    attemptNumber: r.attemptNumber,
    summary: r.summary,
    insights: r.insights || [],
    suggestedAction: r.suggestedAction,
    actionType: r.actionType,
    timestamp: r.timestamp,
  }))
  const hasReflections = reflections.length > 0

  if (!hasReflections) {
    return (
      <div className="flex flex-col items-center justify-center py-12 text-center">
        <Lightbulb className="h-8 w-8 text-muted-foreground/50 mb-3" />
        <p className="text-sm text-muted-foreground">No reflections yet</p>
        <p className="text-xs text-muted-foreground/70 mt-1 max-w-[200px]">
          Reflections will appear here when the system self-corrects
        </p>
      </div>
    )
  }

  // Sort by attempt number (chronological)
  const sortedReflections = [...reflections].sort((a, b) => a.attemptNumber - b.attemptNumber)

  return (
    <div className="space-y-0">
      {sortedReflections.map((reflection, index) => (
        <ReflectionItem 
          key={reflection.id} 
          reflection={reflection} 
          isLast={index === sortedReflections.length - 1}
        />
      ))}
    </div>
  )
}
