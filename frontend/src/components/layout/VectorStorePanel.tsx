import { useEffect, useRef, useCallback, useMemo } from 'react'
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
import type { SearchMode, VectorStoreEntry } from '@/types/models'
import { parsePlusTokens } from '@/lib/plusTokens'

const MAX_PREVIEW_LINES = 4
const MAX_PREVIEW_CHARS = 300

const MODES: SearchMode[] = ['hybrid', 'vector', 'lexical']

export function VectorStorePanel() {
    const status = useVectorIndexStore((s) => s.status)
    const entries = useVectorIndexStore((s) => s.entries)
    const isLoading = useVectorIndexStore((s) => s.isLoading)
    const query = useVectorIndexStore((s) => s.query)
    const topK = useVectorIndexStore((s) => s.topK)
    const filePattern = useVectorIndexStore((s) => s.filePattern)
    const mustMatch = useVectorIndexStore((s) => s.mustMatch)
    const mode = useVectorIndexStore((s) => s.mode)
    const setEntries = useVectorIndexStore((s) => s.setEntries)
    const setLoading = useVectorIndexStore((s) => s.setLoading)
    const setQuery = useVectorIndexStore((s) => s.setQuery)
    const setTopK = useVectorIndexStore((s) => s.setTopK)
    const setFilePattern = useVectorIndexStore((s) => s.setFilePattern)
    const setMustMatch = useVectorIndexStore((s) => s.setMustMatch)
    const removeMustMatch = useVectorIndexStore((s) => s.removeMustMatch)
    const setMode = useVectorIndexStore((s) => s.setMode)
    const clearFilter = useVectorIndexStore((s) => s.clearFilter)

    const activeProjectId = useProjectStore((s) => s.activeProjectId)

    const queryInputRef = useRef<HTMLInputElement>(null)
    const prevProjectRef = useRef(activeProjectId)

    // Fetch entries (browse or search)
    const fetchEntries = useCallback(async (
        q: string,
        k: number,
        pattern: string,
        tokens: string[],
        m: SearchMode,
    ) => {
        setLoading(true)
        try {
            const results = await searchVectorStore({
                query: q,
                top_k: k,
                file_pattern: pattern,
                must_match: tokens,
                mode: q === '' ? '' : m,
            })
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
            fetchEntries(query, topK, filePattern, mustMatch, mode)
        }
    }, [status.state, activeProjectId, fetchEntries, query, topK, filePattern, mustMatch, mode])

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
                    fetchEntries('', topK, '', [], mode)
                }
            }
        })
        return unsub
    }, [fetchEntries, topK, mode])

    const handleSearch = useCallback(() => {
        // Strip +tokens from query and merge into mustMatch before search
        const parsed = parsePlusTokens(query)
        let finalTokens = mustMatch
        if (parsed.tokens.length > 0) {
            const merged = [...mustMatch]
            for (const tok of parsed.tokens) {
                if (!merged.includes(tok)) merged.push(tok)
            }
            finalTokens = merged
            setQuery(parsed.query)
            setMustMatch(merged)
        }
        fetchEntries(parsed.query, topK, filePattern, finalTokens, mode)
    }, [fetchEntries, query, topK, filePattern, mustMatch, mode, setQuery, setMustMatch])

    const handleClear = useCallback(() => {
        clearFilter()
        fetchEntries('', topK, '', [], mode)
    }, [clearFilter, fetchEntries, topK, mode])

    const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
        if (e.key === 'Enter') {
            handleSearch()
        }
    }, [handleSearch])

    const isSearchMode = query !== ''

    const statusMetaText = useMemo(() => {
        if (status.state !== 'ready') return null
        return `${entries.length} entries · ${isSearchMode ? `Search (${mode})` : 'Browse'}`
    }, [status.state, entries.length, isSearchMode, mode])

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
                        placeholder="Keywords... (+tok forces match)"
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

                {/* Mode selector */}
                <div className="flex gap-1">
                    {MODES.map((m) => (
                        <Button
                            key={m}
                            variant={mode === m ? 'default' : 'ghost'}
                            size="sm"
                            onClick={() => setMode(m)}
                            className="h-6 flex-1 px-2 text-[11px] capitalize"
                        >
                            {m}
                        </Button>
                    ))}
                </div>

                {/* MustMatch chips */}
                {mustMatch.length > 0 && (
                    <div className="flex flex-wrap gap-1">
                        {mustMatch.map((tok) => (
                            <button
                                key={tok}
                                type="button"
                                onClick={() => removeMustMatch(tok)}
                                className="flex items-center gap-1 rounded bg-muted px-1.5 py-0.5 text-[10px] hover:bg-destructive hover:text-destructive-foreground"
                                title="Click to remove"
                            >
                                <span>+{tok}</span>
                                <X className="size-2.5" />
                            </button>
                        ))}
                    </div>
                )}
            </div>

            {/* Status bar */}
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
                <IndexingStatus />
                {statusMetaText && (
                    <>
                        <span className="text-border">|</span>
                        <span>{statusMetaText}</span>
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
                {showScore && entry.vector_rank !== undefined && entry.vector_rank > 0 && (
                    <Badge variant="outline" className="h-4 px-1 text-[10px] leading-none text-info" title={`Vector rank ${entry.vector_rank}`}>
                        V#{entry.vector_rank}
                    </Badge>
                )}
                {showScore && entry.lexical_rank !== undefined && entry.lexical_rank > 0 && (
                    <Badge variant="outline" className="h-4 px-1 text-[10px] leading-none text-warning" title={`Lexical rank ${entry.lexical_rank}`}>
                        L#{entry.lexical_rank}
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
