// Renders user message content with /-ref chips (issue #110) and clickable
// file links. Qualified refs (`/agent: name`, `/skill: name`, `/mcp: name`)
// render verbatim in their kind's color; a plain `/name` resolves its kind
// from the catalogs at display time (agent chip / skill chip / mcp chip /
// neutral when unresolvable). Historical `#foo` mentions are plain text —
// `#` is no longer a ref.

import { useMemo, useCallback, useEffect, useState } from 'react'
import { Markdown } from '@/lib/markdownConfig'
import { resolveChatWorkspaceRoot } from '@/lib/chatWorkspaceRoot'
import { normalizePath } from '@/lib/localFileLink'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { cachedRefCatalogs, loadRefCatalogs, type RefCatalogs } from '@/lib/refCatalogs'
import { parseSegments, type Segment } from './userMessageSegments'

interface UserMessageContentProps {
    content: string
}

export function UserMessageContent({ content }: UserMessageContentProps) {
    const segments = useMemo(() => parseSegments(content), [content])
    const hasRefs = useMemo(() => segments.some((s) => s.type !== 'text'), [segments])
    // Only plain /name segments need the catalogs for display-time kind
    // resolution; qualified refs carry their kind in the text itself.
    const needsCatalogs = useMemo(() => segments.some((s) => s.type === 'ref'), [segments])
    const [catalogs, setCatalogs] = useState<RefCatalogs | null>(cachedRefCatalogs())

    // Load the shared catalog snapshot once for the whole app; a re-render
    // after it settles upgrades plain chips from neutral to their kind.
    useEffect(() => {
        if (!needsCatalogs || catalogs) return
        let cancelled = false
        loadRefCatalogs().then((loaded) => {
            if (!cancelled) setCatalogs(loaded)
        })
        return () => {
            cancelled = true
        }
    }, [needsCatalogs, catalogs])

    const resolvePlainRef = useCallback(
        (name: string): 'agent' | 'skill' | 'mcp' | null => {
            if (!catalogs) return null
            const isAgent = catalogs.agentNames.has(name)
            const isSkill = catalogs.skillNames.has(name)
            const isMCP = catalogs.mcpNames.has(name)
            if (Number(isAgent) + Number(isSkill) + Number(isMCP) > 1) return null
            if (isAgent) return 'agent'
            if (isMCP) return 'mcp'
            if (isSkill) return 'skill'
            // Unknown in every catalog (unresolvable prose) or colliding
            // (catalogs changed after the message was sent): neutral chip.
            return null
        },
        [catalogs],
    )

    const handleFileClick = useCallback(async (path: string, startLine?: number) => {
        const rootPath = await resolveChatWorkspaceRoot()
        const fullPath = rootPath && !path.startsWith('/') ? normalizePath(rootPath, path) : path
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

    const renderSlashRef = (seg: Segment, i: number) => {
        const label = seg.raw ?? `/${seg.content}`
        if (seg.type === 'agent') {
            return (
                <span
                    key={`${seg.type}-${seg.content}-${i}`}
                    className="inline-flex items-center rounded px-1.5 py-0.5 text-xs font-mono mx-0.5"
                    style={{ color: 'var(--color-highlight)' }}
                >
                    {label}
                </span>
            )
        }
        if (seg.type === 'skill') {
            return (
                <span
                    key={`${seg.type}-${seg.content}-${i}`}
                    className="inline-flex items-center bg-background text-foreground rounded px-1.5 py-0.5 text-xs font-mono mx-0.5"
                >
                    {label}
                </span>
            )
        }
        if (seg.type === 'mcp') {
            return (
                <span
                    key={`${seg.type}-${seg.content}-${i}`}
                    className="inline-flex items-center rounded px-1.5 py-0.5 text-xs font-mono mx-0.5"
                    style={{ color: 'var(--color-info)' }}
                >
                    {label}
                </span>
            )
        }
        // Plain /name — kind resolved from the catalogs at display time.
        const kind = resolvePlainRef(seg.content)
        if (kind === 'agent') {
            return (
                <span
                    key={`ref-${seg.content}-${i}`}
                    className="inline-flex items-center rounded px-1.5 py-0.5 text-xs font-mono mx-0.5"
                    style={{ color: 'var(--color-highlight)' }}
                >
                    {label}
                </span>
            )
        }
        if (kind === 'mcp') {
            return (
                <span
                    key={`ref-${seg.content}-${i}`}
                    className="inline-flex items-center rounded px-1.5 py-0.5 text-xs font-mono mx-0.5"
                    style={{ color: 'var(--color-info)' }}
                >
                    {label}
                </span>
            )
        }
        const neutralClass = kind === 'skill'
            ? 'inline-flex items-center bg-background text-foreground rounded px-1.5 py-0.5 text-xs font-mono mx-0.5'
            : 'inline-flex items-center bg-background text-muted-foreground rounded px-1.5 py-0.5 text-xs font-mono mx-0.5'
        return (
            <span key={`ref-${seg.content}-${i}`} className={neutralClass}>
                {label}
            </span>
        )
    }

    return (
        <span className="whitespace-pre-wrap break-words text-sm">
            {segments.map((seg, i) => {
                const segKey = seg.type === 'text' ? `text-${i}-${seg.content}` : `${seg.type}-${seg.content}-${i}`
                if (seg.type === 'skill' || seg.type === 'agent' || seg.type === 'mcp' || seg.type === 'ref') {
                    return renderSlashRef(seg, i)
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
