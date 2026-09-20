// Pure parsing logic for user message content: splits text into segments
// (text / skill / file refs). Extracted from UserMessageContent.tsx so the
// component file only exports a React component (react-refresh requirement).

// Combined pattern for splitting: matches /skill-name, #agent-name, or @path refs.
// Line anchors accept an optional `L` prefix (e.g. #L20-L36), consistent with
// GitHub canonical form and the backend preprocessor. The #agent-name alt must
// only match when followed by [\w-]+ with a word boundary so a trailing @file
// line anchor (@x.go#L20) is never captured as an agent. The @path alt accepts
// two forms, tried in order: a single-quoted path (@'my file.go', the
// canonical form for paths with spaces) and a bare path with backslash-escaped
// spaces (@my\ file.go, the legacy form).
const REF_PATTERN = /(?:^|\s)(\/[\w-]+|#([\w-]+)|@(?:'[^']+'|(?:[^\s\\]|\\.)+)(?:#L?\d+(?:-L?\d+)?)?)/g

export interface Segment {
    type: 'text' | 'skill' | 'agent' | 'file'
    content: string
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
        const ref = match[1]
        if (ref === undefined) continue
        // Account for leading whitespace in match
        const refStart = match.index + (fullMatch.length - ref.length)

        // Push preceding text
        if (refStart > lastIndex) {
            segments.push({ type: 'text', content: content.slice(lastIndex, refStart) })
        }

        if (ref.startsWith('/')) {
            segments.push({ type: 'skill', content: ref.slice(1) })
        } else if (ref.startsWith('#')) {
            segments.push({ type: 'agent', content: match[2] ?? ref.slice(1) })
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
