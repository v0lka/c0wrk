import { useEffect, useRef, useCallback } from 'react'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore } from '@/stores/projectStore'
import { searchVectorStore } from '@/api/vector'
import { subscribe } from '@/api/runtime'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { IndexingStatus } from './IndexingStatus'
import { Search, X, Loader2, FileCode } from 'lucide-react'
import type { VectorStoreEntry } from '@/types/models'

const MAX_PREVIEW_LINES = 4
const MAX_PREVIEW_CHARS = 300

export function VectorStorePanel() {
    const status = useVectorIndexStore((s) => s.status)
    const entries = useVectorIndexStore((s) => s.entries)
    const isLoading = useVectorIndexStore((s) => s.isLoading)
    const query = useVectorIndexStore((s) => s.query)
    const topK = useVectorIndexStore((s) => s.topK)
    const filePattern = useVectorIndexStore((s) => s.filePattern)
    const setEntries = useVectorIndexStore((s) => s.setEntries)
    const setLoading = useVectorIndexStore((s) => s.setLoading)
    const setQuery = useVectorIndexStore((s) => s.setQuery)
    const setTopK = useVectorIndexStore((s) => s.setTopK)
    const setFilePattern = useVectorIndexStore((s) => s.setFilePattern)
    const clearFilter = useVectorIndexStore((s) => s.clearFilter)

    const activeProjectId = useProjectStore((s) => s.activeProjectId)

    const queryInputRef = useRef<HTMLInputElement>(null)
    const prevProjectRef = useRef(activeProjectId)

    // Fetch entries (browse or search)
    const fetchEntries = useCallback(async (q: string, k: number, pattern: string) => {
        setLoading(true)
        try {
            const results = await searchVectorStore(q, k, pattern)
            setEntries(results)
        } catch {
            setEntries([])
        } finally {
            setLoading(false)
        }
    }, [setEntries, setLoading])

    // Auto-browse on mount and when index becomes ready
    useEffect(() => {
        if (status.state === 'ready' && activeProjectId) {
            fetchEntries(query, topK, filePattern)
        }
    }, [status.state, activeProjectId, fetchEntries, query, topK, filePattern])

    // Reset entries when project changes
    useEffect(() => {
        if (prevProjectRef.current !== activeProjectId) {
            prevProjectRef.current = activeProjectId
            setEntries([])
            clearFilter()
        }
    }, [activeProjectId, setEntries, clearFilter])

    // Re-fetch on vector_index:status ready event
    useEffect(() => {
        const unsub = subscribe('vector_index:status', (data: unknown) => {
            if (data && typeof data === 'object' && 'state' in data) {
                const record = data as Record<string, unknown>
                if (record.state === 'ready') {
                    fetchEntries('', topK, '')
                }
            }
        })
        return unsub
    }, [fetchEntries, topK])

    const handleSearch = useCallback(() => {
        fetchEntries(query, topK, filePattern)
    }, [fetchEntries, query, topK, filePattern])

    const handleClear = useCallback(() => {
        clearFilter()
        fetchEntries('', topK, '')
    }, [clearFilter, fetchEntries, topK])

    const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
        if (e.key === 'Enter') {
            handleSearch()
        }
    }, [handleSearch])

    const isSearchMode = query !== ''

    return (
        <div className="flex h-full flex-col gap-2 p-2">
            {/* Filter area */}
            <div className="flex flex-col gap-1.5">
                <div className="flex gap-1">
                    <Input
                        ref={queryInputRef}
                        value={query}
                        onChange={(e) => setQuery(e.target.value)}
                        onKeyDown={handleKeyDown}
                        placeholder="Keywords..."
                        className="h-7 text-xs"
                    />
                    <Input
                        type="number"
                        value={topK}
                        onChange={(e) => setTopK(Math.max(1, parseInt(e.target.value) || 50))}
                        min={1}
                        max={500}
                        className="h-7 w-20 text-xs text-center"
                    />
                </div>
                <div className="flex gap-1">
                    <Input
                        value={filePattern}
                        onChange={(e) => setFilePattern(e.target.value)}
                        onKeyDown={handleKeyDown}
                        placeholder="File pattern (e.g. *.go, src/**)"
                        className="h-7 text-xs"
                    />
                    <Button
                        variant="default"
                        size="sm"
                        onClick={handleSearch}
                        disabled={isLoading || status.state !== 'ready'}
                        className="h-7 px-2"
                    >
                        <Search className="size-3.5" />
                    </Button>
                    {isSearchMode && (
                        <Button
                            variant="ghost"
                            size="sm"
                            onClick={handleClear}
                            className="h-7 px-2"
                        >
                            <X className="size-3.5" />
                        </Button>
                    )}
                </div>
            </div>

            {/* Status bar */}
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
                <IndexingStatus />
                {status.state === 'ready' && (
                    <>
                        <span className="text-border">|</span>
                        <span>{entries.length} entries</span>
                        <span className="text-border">|</span>
                        <span>{isSearchMode ? 'Search' : 'Browse'}</span>
                    </>
                )}
            </div>

            {/* Results */}
            {status.state !== 'ready' && status.state !== 'idle' ? (
                <div className="flex-1" />
            ) : status.state === 'idle' ? (
                <div className="flex-1 flex items-center justify-center">
                    <p className="text-xs text-muted-foreground">Select a project to browse</p>
                </div>
            ) : isLoading ? (
                <div className="flex-1 flex items-center justify-center">
                    <Loader2 className="size-4 animate-spin text-muted-foreground" />
                </div>
            ) : entries.length === 0 ? (
                <div className="flex-1 flex items-center justify-center">
                    <p className="text-xs text-muted-foreground">
                        {isSearchMode ? 'No results found' : 'Vector store is empty'}
                    </p>
                </div>
            ) : (
                <div className="flex-1 overflow-auto custom-scrollbar">
                    {entries.map((entry) => (
                        <VectorStoreEntryItem key={`${entry.file_path}:${entry.start_line}:${entry.end_line}`} entry={entry} showScore={isSearchMode} />
                    ))}
                </div>
            )}
        </div>
    )
}

// --- Individual entry component ---

function VectorStoreEntryItem({ entry, showScore }: { entry: VectorStoreEntry; showScore: boolean }) {
    const openFileAtLine = useFileViewerStore((s) => s.openFileAtLine)

    const handleClick = useCallback(() => {
        openFileAtLine(entry.file_path, entry.start_line)
    }, [openFileAtLine, entry.file_path, entry.start_line])

    // Build content preview
    const previewLines = entry.content.split('\n').slice(0, MAX_PREVIEW_LINES)
    let preview = previewLines.join('\n')
    if (preview.length > MAX_PREVIEW_CHARS) {
        preview = preview.slice(0, MAX_PREVIEW_CHARS) + '...'
    }
    if (entry.content.split('\n').length > MAX_PREVIEW_LINES) {
        preview += '\n...'
    }

    // Extract relative path segment for display
    const fileName = entry.file_name || entry.file_path.split('/').pop() || ''
    const dirPath = entry.file_path.substring(0, entry.file_path.length - fileName.length)

    return (
        <button
            type="button"
            onClick={handleClick}
            className="w-full text-left px-1.5 py-1.5 rounded hover:bg-muted/50 transition-colors cursor-pointer"
        >
            {/* Header line */}
            <div className="flex items-center gap-1.5 text-xs">
                <FileCode className="size-3 shrink-0 text-muted-foreground" />
                <span className="truncate font-medium text-foreground">{fileName}</span>
                {entry.start_line > 0 && (
                    <span className="shrink-0 text-muted-foreground">
                        L{entry.start_line}{entry.end_line > entry.start_line ? `-${entry.end_line}` : ''}
                    </span>
                )}
                {entry.language && (
                    <Badge variant="outline" className="h-4 px-1 text-[10px] leading-none">
                        {entry.language}
                    </Badge>
                )}
                {showScore && (
                    <span className="ml-auto shrink-0 text-muted-foreground tabular-nums">
                        {entry.score.toFixed(2)}
                    </span>
                )}
            </div>

            {/* Directory path */}
            {dirPath && (
                <div className="truncate text-[10px] text-muted-foreground mt-0.5 pl-[18px]">
                    {dirPath}
                </div>
            )}

            {/* Content preview */}
            <pre className="mt-1 pl-[18px] text-[10px] leading-4 text-muted-foreground whitespace-pre-wrap break-all line-clamp-4">
                {preview}
            </pre>
        </button>
    )
}
