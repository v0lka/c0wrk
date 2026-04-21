import { useMemo, useState, useEffect, useRef } from 'react'
import hljs from 'highlight.js/lib/core'
import { Code, Eye, Loader2 } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkEmoji from 'remark-emoji'
import remarkBreaks from 'remark-breaks'
import rehypeHighlight from 'rehype-highlight'
import rehypeSanitize from 'rehype-sanitize'
import rehypeExternalLinks from 'rehype-external-links'
import rehypeSlug from 'rehype-slug'
import rehypeAutolinkHeadings from 'rehype-autolink-headings'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { parseUnifiedDiff, buildDisplayLines, type DisplayLine } from '@/lib/diffParser'
import { customSchema, markdownComponents } from '@/lib/markdownConfig'
import { ErrorBoundary } from '@/components/ErrorBoundary'

export function FileViewerContent() {
  const activeFilePath = useFileViewerStore((s) => s.activeFilePath)
  const openFiles = useFileViewerStore((s) => s.openFiles)
  const silentRefreshAllFiles = useFileViewerStore((s) => s.silentRefreshAllFiles)

  const activeFile = openFiles.find((f) => f.path === activeFilePath)

  // Listen for workspace:tree_changed to silently refresh open files
  // and preserve scroll position.
  useEffect(() => {
    const rt = window?.runtime
    if (!rt) return () => {}
    const cleanup = rt.EventsOn('workspace:tree_changed', () => {
      silentRefreshAllFiles()
    })
    return cleanup
  }, [silentRefreshAllFiles])

  if (!activeFile) return null

  if (activeFile.isLoading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (activeFile.error) {
    return (
      <div className="flex-1 flex items-center justify-center p-4">
        <p className="text-sm text-destructive text-center">{activeFile.error}</p>
      </div>
    )
  }

  if (activeFile.isBinary) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <p className="text-sm text-muted-foreground">Unsupported file format</p>
      </div>
    )
  }

  return <HighlightedContent content={activeFile.content} language={activeFile.language} diff={activeFile.diff} />
}

// -- Internal component: highlighted content with inline diff ----------------

function HighlightedContent({ content, language, diff }: {
  content: string
  language: string
  diff: string
}) {
  const [showRaw, setShowRaw] = useState(false)
  const isMarkdown = language === 'markdown'
  const lines = content.split('\n')
  const scrollRef = useRef<HTMLDivElement>(null)

  // Save and restore scroll position when content changes
  const prevContentRef = useRef(content)
  useEffect(() => {
    if (prevContentRef.current !== content && scrollRef.current) {
      const savedScroll = scrollRef.current.scrollTop
      // Restore after React re-renders with new content
      requestAnimationFrame(() => {
        if (scrollRef.current) {
          scrollRef.current.scrollTop = savedScroll
        }
      })
    }
    prevContentRef.current = content
  }, [content])

  // Parse diff and build display lines (includes removed lines and char diffs)
  const displayLines = useMemo((): DisplayLine[] => {
    if (!diff) return []
    const { hunks } = parseUnifiedDiff(diff)
    if (hunks.length === 0) return []
    return buildDisplayLines(lines, hunks)
  }, [diff, lines])

  // Highlight the full content
  const highlightedHtml = useMemo(() => {
    try {
      return hljs.highlight(content, { language }).value
    } catch {
      return hljs.highlightAuto(content).value
    }
  }, [content, language])

  // Split highlighted HTML into per-line HTML
  const highlightedLines = useMemo(() => {
    return splitHighlightedLines(highlightedHtml, lines.length)
  }, [highlightedHtml, lines.length])

  // Build a lookup: lineNumber → highlighted HTML for that line
  const highlightedMap = useMemo(() => {
    const map = new Map<number, string>()
    for (let i = 0; i < lines.length; i++) {
      map.set(i + 1, highlightedLines[i] ?? '')
    }
    return map
  }, [highlightedLines, lines.length])

  // For Markdown files in rendered mode, show the ReactMarkdown preview
  if (isMarkdown && !showRaw) {
    return (
      <div className="flex-1 flex flex-col overflow-hidden">
        {/* Toggle button */}
        <div className="flex items-center justify-end flex-shrink-0 px-2 py-1 border-b border-border bg-secondary/30">
          <button
            onClick={() => setShowRaw(true)}
            className="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground hover:bg-muted/50 rounded px-1.5 py-0.5 transition-colors"
            title="View source"
            aria-label="View raw Markdown source"
          >
            <Code className="h-3 w-3" />
            <span>Source</span>
          </button>
        </div>
        <div className="flex-1 overflow-auto custom-scrollbar">
          <div className="prose prose-sm dark:prose-invert max-w-full break-words min-w-0 p-4">
            <ErrorBoundary fallback={<pre className="text-xs font-mono whitespace-pre-wrap">{content}</pre>}>
              <ReactMarkdown
                remarkPlugins={[remarkGfm, remarkEmoji, remarkBreaks]}
                rehypePlugins={[
                  rehypeSlug,
                  [rehypeAutolinkHeadings, { behavior: 'wrap' }],
                  rehypeHighlight,
                  [rehypeExternalLinks, { target: '_blank', rel: ['noopener', 'noreferrer'] }],
                  [rehypeSanitize, customSchema],
                ]}
                components={markdownComponents}
              >
                {content}
              </ReactMarkdown>
            </ErrorBoundary>
          </div>
        </div>
      </div>
    )
  }

  // Raw code view (default for non-Markdown, or when showRaw is true for Markdown)
  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      {/* Toggle button for Markdown files */}
      {isMarkdown && (
        <div className="flex items-center justify-end flex-shrink-0 px-2 py-1 border-b border-border bg-secondary/30">
          <button
            onClick={() => setShowRaw(false)}
            className="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground hover:bg-muted/50 rounded px-1.5 py-0.5 transition-colors"
            title="View rendered"
            aria-label="View rendered Markdown"
          >
            <Eye className="h-3 w-3" />
            <span>Preview</span>
          </button>
        </div>
      )}
      <div ref={scrollRef} className="flex-1 overflow-auto custom-scrollbar">
        <pre className="text-xs leading-5 font-mono min-w-max">
          <code className={`hljs language-${language}`}>
            {displayLines.length > 0
              ? displayLines.map((dl, i) => (
                  <DiffLineRow key={`dl-${i}`} displayLine={dl} highlightedMap={highlightedMap} />
                ))
              : lines.map((_, i) => {
                  const lineNo = i + 1
                  return (
                    <div key={i} data-line-number={lineNo} className="file-viewer-line">
                      <span className="file-viewer-line-number">{lineNo}</span>
                      <span
                        className="file-viewer-line-content"
                        dangerouslySetInnerHTML={{ __html: highlightedLines[i] ?? '' }}
                      />
                    </div>
                  )
                })}
          </code>
        </pre>
      </div>
    </div>
  )
}

// -- Utility: split highlighted HTML into per-line segments ------------------

function splitHighlightedLines(html: string, expectedLines: number): string[] {
  // We need to split the highlighted HTML by newlines while keeping
  // HTML tags balanced across lines. Strategy:
  // 1. Walk through the HTML character by character
  // 2. Track open tags as we encounter them
  // 3. When we hit a \n, close all open tags for this line,
  //    then re-open them for the next line

  const result: string[] = []
  let currentLine = ''
  const openTags: string[] = []

  let i = 0
  while (i < html.length) {
    if (html[i] === '<') {
      // Find the end of the tag
      const tagEnd = html.indexOf('>', i)
      if (tagEnd === -1) {
        // Malformed — just append the rest
        currentLine += html.slice(i)
        break
      }
      const tag = html.slice(i, tagEnd + 1)
      currentLine += tag

      if (tag.startsWith('</')) {
        // Closing tag — pop from open tags
        openTags.pop()
      } else if (!tag.endsWith('/>')) {
        // Opening tag (not self-closing) — push
        openTags.push(tag)
      }
      i = tagEnd + 1
    } else if (html[i] === '\n') {
      // Close all open tags for this line
      for (let t = openTags.length - 1; t >= 0; t--) {
        currentLine += closeTag(openTags[t]!)
      }
      result.push(currentLine)

      // Start new line, re-open tags
      currentLine = ''
      for (const ot of openTags) {
        currentLine += ot
      }
      i++
    } else {
      currentLine += html[i]
      i++
    }
  }

  // Push the last line
  result.push(currentLine)

  // Ensure we have the right number of lines
  while (result.length < expectedLines) {
    result.push('')
  }

  return result
}

function closeTag(openTag: string): string {
  const match = openTag.match(/<(\w+)/)
  if (!match) return ''
  return `</${match[1]}>`
}

// -- Diff line renderer ------------------------------------------------------

/**
 * Renders a single display line based on its diff type.
 * - normal: standard line with syntax highlighting
 * - added: green background, syntax-highlighted
 * - removed: red background, no line number, plain text
 * - modified: light green background with inline character-level diff
 */
function DiffLineRow({ displayLine, highlightedMap }: {
  displayLine: DisplayLine
  highlightedMap: Map<number, string>
}) {
  const { type, lineNumber, hunkId } = displayLine

  if (type === 'removed') {
    return (
      <div
        data-hunk-id={hunkId}
        className="file-viewer-line diff-line-removed"
      >
        <span className="file-viewer-line-number" />
        <span className="file-viewer-line-content">{displayLine.content}</span>
      </div>
    )
  }

  if (type === 'modified' && displayLine.charDiff) {
    return (
      <div
        data-line-number={lineNumber}
        data-hunk-id={hunkId}
        className="file-viewer-line diff-line-modified"
      >
        <span className="file-viewer-line-number">{lineNumber}</span>
        <span className="file-viewer-line-content">
          {displayLine.charDiff.map((part, idx) => {
            if (part.type === 'equal') {
              return <span key={idx}>{part.value}</span>
            }
            if (part.type === 'added') {
              return <span key={idx} className="diff-char-added">{part.value}</span>
            }
            // removed: shown inline, struck-through and red-highlighted
            return <span key={idx} className="diff-char-removed">{part.value}</span>
          })}
        </span>
      </div>
    )
  }

  // normal or added line — use syntax-highlighted HTML
  const isAdded = type === 'added'
  const html = lineNumber ? (highlightedMap.get(lineNumber) ?? '') : ''

  return (
    <div
      data-line-number={lineNumber}
      data-hunk-id={hunkId}
      className={`file-viewer-line ${isAdded ? 'diff-line-added' : ''}`}
    >
      <span className="file-viewer-line-number">{lineNumber}</span>
      <span
        className="file-viewer-line-content"
        dangerouslySetInnerHTML={{ __html: html }}
      />
    </div>
  )
}
