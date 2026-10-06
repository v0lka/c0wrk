// Pure parsing logic for user message content: splits text into segments
// (text / slash refs / file refs). Extracted from UserMessageContent.tsx so the
// component file only exports a React component (react-refresh requirement).

import { REF_BOUNDARY_SPACE_SOURCE, SLASH_REF_SOURCE } from '@/lib/parseReferences'

// Combined pattern for splitting: matches /-refs or @path refs (issue #110).
// A /-ref is either collision-qualified (`/agent: name` / `/skill: name` /
// `/mcp: name`, the space after the colon optional) or plain (`/name` — kind
// resolved at display time against the catalogs). `#` is NOT a ref trigger
// anymore: historical `#foo` mentions fall through as plain text, while the
// `#` in @file line anchors belongs to the file alternative below. The @path
// alt accepts two forms, tried in order: a single-quoted path (@'my file.go',
// the canonical form for paths with spaces) and a bare path with
// backslash-escaped spaces (@my\ file.go, the legacy form).
// Keep the file branch's existing whitespace semantics; slash refs use the
// same complete-token grammar as send-path extraction.
const REF_PATTERN = new RegExp(
    String.raw`(?:^|${REF_BOUNDARY_SPACE_SOURCE})(${SLASH_REF_SOURCE})|(?:^|\s)(@(?:'[^']+'|(?:[^\s\\]|\\.)+)(?:#L?\d+(?:-L?\d+)?)?)`,
    'g',
)

export interface Segment {
    /**
     * 'skill' / 'agent' / 'mcp': a collision-qualified /-ref (explicit kind).
     * 'ref': a plain /name whose kind is resolved at display time against
     * the catalogs (agent chip / skill chip / mcp chip / neutral when
     * unresolvable).
     */
    type: 'text' | 'skill' | 'agent' | 'mcp' | 'ref' | 'file'
    /** The ref name (slash refs) or verbatim ref (file refs). */
    content: string
    /** Verbatim source spelling of a slash ref (`/agent: name`, `/name`). */
    raw?: string
    // For file refs:
    path?: string
    startLine?: number
}

export function parseSegments(content: string): Segment[] {
    const segments: Segment[] = []
    let lastIndex = 0

    REF_PATTERN.lastIndex = 0
    let match: RegExpExecArray | null
    while ((match = REF_PATTERN.exec(content)) !== null) {
        const fullMatch = match[0]
        const ref = match[1] ?? match[5]
        if (ref === undefined) continue
        // Account for leading whitespace in match
        const refStart = match.index + (fullMatch.length - ref.length)

        // Push preceding text
        if (refStart > lastIndex) {
            segments.push({ type: 'text', content: content.slice(lastIndex, refStart) })
        }

        if (ref.startsWith('/')) {
            const qualifiedKind = match[2]
            const qualifiedName = match[3]
            const plainName = match[4]
            if (qualifiedKind !== undefined && qualifiedName !== undefined) {
                segments.push({
                    type: qualifiedKind as 'agent' | 'skill' | 'mcp',
                    content: qualifiedName,
                    raw: ref,
                })
            } else if (plainName !== undefined) {
                segments.push({ type: 'ref', content: plainName, raw: ref })
            }
        } else if (ref.startsWith('@')) {
            // Two path forms: single-quoted (@'my file.go', content
            // verbatim) and legacy backslash-escaped (@my\ file.go). The
            // trailing line anchor is split off FIRST so quote stripping
            // works with the anchor after the closing quote — and, because
            // the quoted content is then re-checked, with the anchor inside
            // the quotes as well (mirrors the backend's two-step split).
            let raw = ref.slice(1)
            const outer = raw.match(/#L?(\d+)(?:-L?\d+)?$/)
            let lineStr = outer?.[1]
            if (outer) raw = raw.slice(0, raw.lastIndexOf('#'))

            let path = raw
            if (path.length >= 2 && path.startsWith("'") && path.endsWith("'")) {
                path = path.slice(1, -1)
            } else if (path.startsWith("'") && !path.endsWith("'")) {
                // Unterminated quoted ref (@'my file — the quoted alternative
                // of REF_PATTERN never matches, so the bare alternative
                // captured the lone opening quote). Strip just that quote so
                // the file chip label carries no stray apostrophe; mirrors
                // cmChatAutocomplete.fileSource's token.startsWith("'") handling.
                path = path.slice(1)
            } else {
                path = path.replace(/\\ /g, ' ')
            }

            const inner = path.match(/#L?(\d+)(?:-L?\d+)?$/)
            if (inner) {
                lineStr ??= inner[1]
                path = path.slice(0, path.lastIndexOf('#'))
            }

            const startLine = lineStr !== undefined ? parseInt(lineStr, 10) : undefined
            segments.push({ type: 'file', content: ref, path, startLine })
        }

        lastIndex = refStart + ref.length
    }

    // Push remaining text
    if (lastIndex < content.length) {
        segments.push({ type: 'text', content: content.slice(lastIndex) })
    }

    return segments
}
