import { useMemo, useState, useEffect, useRef, useCallback } from 'react'
import { Code, Eye, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { subscribe } from '@/api/runtime'
import { readFile, getFileDiff } from '@/api/workspace'
import { parseUnifiedDiff, buildDisplayLines, type DisplayLine } from '@/lib/diffParser'
import { Markdown } from '@/lib/markdownConfig'
import { highlightLines, detectLanguageFromPath, isBinaryContent } from '@/lib/fileViewerUtils'
import { cn } from '@/lib/utils'

export function FileViewerContent() {
  const activeFile = useFileViewerStore((s) => s.activeFile)
  const files = useFileViewerStore((s) => s.files)
  const openTabs = useFileViewerStore((s) => s.openTabs)
  const highlightLine = useFileViewerStore((s) => s.highlightLine)
  const setFileContent = useFileViewerStore((s) => s.setFileContent)
  const setFileDiff = useFileViewerStore((s) => s.setFileDiff)
  const setFileError = useFileViewerStore((s) => s.setFileError)
  const setFileBinary = useFileViewerStore((s) => s.setFileBinary)
  const setFileLoading = useFileViewerStore((s) => s.setFileLoading)

  const scrollRef = useRef<HTMLDivElement>(null)

  const loadFile = useCallback(async (path: string, silent: boolean) => {
    if (!silent) setFileLoading(path, true)
    try {
      const content = await readFile(path)
      if (isBinaryContent(content)) { setFileBinary(path); return }
      setFileContent(path, content, detectLanguageFromPath(path))
      try { const diff = await getFileDiff(path); if (diff) setFileDiff(path, diff) } catch { /* optional */ }
    } catch (err) { setFileError(path, err instanceof Error ? err.message : String(err)) }
  }, [setFileLoading, setFileBinary, setFileContent, setFileDiff, setFileError])

  const loadFileRef = useRef(loadFile)
  loadFileRef.current = loadFile
  const filesRef = useRef(files)
  filesRef.current = files

  // Load active file when it changes
  useEffect(() => {
    if (!activeFile) return
    const data = filesRef.current[activeFile]
    if (data && !data.loading && (data.content || data.error || data.isBinary)) return
    loadFileRef.current(activeFile, false)
  }, [activeFile])

  // Auto-refresh on workspace:tree_changed — silently reload all open files
  useEffect(() => {
    const unsub = subscribe('workspace:tree_changed', () => {
      const savedScroll = scrollRef.current?.scrollTop ?? 0
      for (const path of openTabs) {
        loadFile(path, true)
      }
      requestAnimationFrame(() => {
        if (scrollRef.current) scrollRef.current.scrollTop = savedScroll
      })
    })
    return unsub
  }, [openTabs, loadFile])

  if (!activeFile) return null
  const fileData = files[activeFile]
  if (!fileData) return null

  if (fileData.loading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (fileData.error) {
    return (
      <div className="flex-1 flex items-center justify-center p-4">
        <p className="text-sm text-destructive text-center">{fileData.error}</p>
      </div>
    )
  }

  if (fileData.isBinary) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <p className="text-sm text-muted-foreground">Unsupported file format</p>
      </div>
    )
  }

  return (
    <HighlightedContent
      content={fileData.content}
      language={fileData.language ?? 'plaintext'}
      diff={fileData.diff}
      highlightLine={highlightLine}
      scrollRef={scrollRef}
    />
  )
}

// --- Highlighted content with diff overlay ---

function HighlightedContent({ content, language, diff, highlightLine, scrollRef }: {
  content: string
  language: string
  diff?: string
  highlightLine: number | null
  scrollRef: React.RefObject<HTMLDivElement | null>
}) {
  const [showRaw, setShowRaw] = useState(false)
  const isMarkdown = language === 'markdown'
  const lines = useMemo(() => content.split('\n'), [content])

  const displayLines = useMemo((): DisplayLine[] => {
    if (!diff) return []
    const { hunks } = parseUnifiedDiff(diff)
    return hunks.length > 0 ? buildDisplayLines(lines, hunks) : []
  }, [diff, lines])

  const highlighted = useMemo(
    () => highlightLines(content, language),
    [content, language],
  )

  const clearHighlightLine = useFileViewerStore((s) => s.clearHighlightLine)

  // Scroll to the highlighted line and clear after a delay
  useEffect(() => {
    if (highlightLine == null) return
    const container = scrollRef.current
    if (!container) return

    // Find the line element by data-line-number attribute
    const lineEl = container.querySelector<HTMLElement>(
      `[data-line-number="${highlightLine}"]`
    )
    if (lineEl) {
      lineEl.scrollIntoView({ block: 'center', behavior: 'smooth' })
    }

    const timer = setTimeout(() => {
      clearHighlightLine()
    }, 3000)

    return () => clearTimeout(timer)
  }, [highlightLine, scrollRef, clearHighlightLine])

  // Markdown preview mode
  if (isMarkdown && !showRaw) {
    return (
      <div className="flex-1 flex flex-col overflow-hidden">
        <div ref={scrollRef} className="flex-1 overflow-auto custom-scrollbar p-4">
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={() => setShowRaw(true)}
            className="float-right ml-3 mb-3"
            title="Source"
          >
            <Code className="size-4" />
          </Button>
          <Markdown content={content} />
        </div>
      </div>
    )
  }

  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      <div ref={scrollRef} className="flex-1 overflow-auto custom-scrollbar p-4">
        {isMarkdown && (
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={() => setShowRaw(false)}
            className="float-right ml-3 mb-3"
            title="Preview"
          >
            <Eye className="size-4" />
          </Button>
        )}
        <pre className="text-xs leading-5 font-mono min-w-max">
          <code className={`hljs language-${language}`}>
            {displayLines.length > 0
              ? displayLines.map((dl, i) => (
                <DiffLine key={i} dl={dl} highlighted={highlighted} highlightLine={highlightLine} />
              ))
              : lines.map((_, i) => (
                <div key={i} className={cn('file-viewer-line', highlightLine === i + 1 && 'file-viewer-line-highlighted')} data-line-number={i + 1}>
                  <span className="file-viewer-line-number">{i + 1}</span>
                  <span className="file-viewer-line-content" dangerouslySetInnerHTML={{ __html: highlighted[i] ?? '' }} />
                </div>
              ))}
          </code>
        </pre>
      </div>
    </div>
  )
}



function DiffLine({ dl, highlighted, highlightLine }: { dl: DisplayLine; highlighted: string[]; highlightLine: number | null }) {
  const isHighlighted = dl.lineNumber != null && highlightLine === dl.lineNumber
  if (dl.type === 'removed') {
    return (
      <div className={cn('file-viewer-line', 'diff-line-removed', isHighlighted && 'file-viewer-line-highlighted')} data-hunk-id={dl.hunkId}>
        <span className="file-viewer-line-number" />
        <span className="file-viewer-line-content">{dl.content}</span>
      </div>
    )
  }
  if (dl.type === 'modified' && dl.charDiff) {
    return (
      <div className={cn('file-viewer-line', 'diff-line-modified', isHighlighted && 'file-viewer-line-highlighted')} data-line-number={dl.lineNumber} data-hunk-id={dl.hunkId}>
        <span className="file-viewer-line-number">{dl.lineNumber}</span>
        <span className="file-viewer-line-content">
          {dl.charDiff.map((part, idx) => (
            <span key={idx} className={part.type === 'added' ? 'diff-char-added' : part.type === 'removed' ? 'diff-char-removed' : undefined}>
              {part.value}
            </span>
          ))}
        </span>
      </div>
    )
  }
  const html = dl.lineNumber ? (highlighted[dl.lineNumber - 1] ?? '') : ''
  return (
    <div className={cn('file-viewer-line', dl.type === 'added' && 'diff-line-added', isHighlighted && 'file-viewer-line-highlighted')} data-line-number={dl.lineNumber} data-hunk-id={dl.hunkId}>
      <span className="file-viewer-line-number">{dl.lineNumber}</span>
      <span className="file-viewer-line-content" dangerouslySetInnerHTML={{ __html: html }} />
    </div>
  )
}
