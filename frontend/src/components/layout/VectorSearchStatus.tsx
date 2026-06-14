import { IndexingStatus } from '@/components/layout/IndexingStatus'

interface VectorSearchStatusProps {
  statusMetaText: string | null
}

/**
 * VectorSearchStatus renders the indexing status pill plus an optional meta
 * line (entry count + browse/search mode). (W-30 split)
 */
export function VectorSearchStatus({ statusMetaText }: VectorSearchStatusProps) {
  return (
    <div className="flex items-center gap-2 text-xs text-muted-foreground">
      <IndexingStatus />
      {statusMetaText && (
        <>
          <span className="text-border">|</span>
          <span>{statusMetaText}</span>
        </>
      )}
    </div>
  )
}
