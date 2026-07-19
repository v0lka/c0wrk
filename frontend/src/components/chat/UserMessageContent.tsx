// Renders user message content with skill chips and clickable file links.

import { useMemo, useCallback } from 'react'
import { Markdown } from '@/lib/markdownConfig'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { parseSegments } from './userMessageSegments'

interface UserMessageContentProps {
    content: string
}

export function UserMessageContent({ content }: UserMessageContentProps) {
    const segments = useMemo(() => parseSegments(content), [content])
    const hasRefs = useMemo(() => segments.some((s) => s.type !== 'text'), [segments])

    const handleFileClick = useCallback((path: string, startLine?: number) => {
        const rootPath = useFileTreeStore.getState().rootPath
        const fullPath = rootPath && !path.startsWith('/') ? `${rootPath}/${path}` : path
        if (startLine !== undefined) {
            useFileViewerStore.getState().openFileAtLine(fullPath, startLine)
        } else {
            useFileViewerStore.getState().openFile(fullPath)
        }
    }, [])

    // If no references, fall back to standard Markdown rendering.
    if (!hasRefs) {
        return <Markdown content={content} className="user-message-prose" />
    }

    return (
        <span className="whitespace-pre-wrap break-words text-sm">
            {segments.map((seg, i) => {
                const segKey = seg.type === 'text' ? `text-${i}-${seg.content}` : `${seg.type}-${seg.content}-${i}`
                if (seg.type === 'skill') {
                    return (
                        <span
                            key={segKey}
                            className="inline-flex items-center bg-background text-foreground rounded px-1.5 py-0.5 text-xs font-mono mx-0.5"
                        >
                            /{seg.content}
                        </span>
                    )
                }
                if (seg.type === 'file') {
                    const filePath = seg.path ?? ''
                    const display = filePath + (seg.startLine !== undefined ? `#L${seg.startLine}` : '')
                    return (
                        <span
                            key={segKey}
                            className="text-info hover:underline cursor-pointer font-mono text-xs mx-0.5"
                            onClick={() => handleFileClick(filePath, seg.startLine)}
                            role="link"
                            tabIndex={0}
                            onKeyDown={(e) => { if (e.key === 'Enter') handleFileClick(filePath, seg.startLine) }}
                        >
                            @{display}
                        </span>
                    )
                }
                // Plain text — render inline (no markdown for mixed content).
                return <span key={segKey}>{seg.content}</span>
            })}
        </span>
    )
}
