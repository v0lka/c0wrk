import { useVectorSearch } from '@/hooks/useVectorSearch'
import { VectorSearchFilters } from '@/components/layout/VectorSearchFilters'
import { VectorSearchStatus } from '@/components/layout/VectorSearchStatus'
import { VectorSearchResults } from '@/components/layout/VectorSearchResults'

/**
 * VectorStorePanel composes the vector store search UI:
 *   filters → status → results
 *
 * State and side-effects (search/clear, project change reset, vector index
 * status subscription) live in useVectorSearch. (W-30 split)
 */
export function VectorStorePanel() {
  const { isSearchMode, statusMetaText, handleSearch, handleClear, handleKeyDown } = useVectorSearch()

  return (
    <div className="flex h-full flex-col gap-2 p-2">
      <VectorSearchFilters
        isSearchMode={isSearchMode}
        onSearch={handleSearch}
        onClear={handleClear}
        onKeyDown={handleKeyDown}
      />
      <VectorSearchStatus statusMetaText={statusMetaText} />
      <VectorSearchResults isSearchMode={isSearchMode} />
    </div>
  )
}
