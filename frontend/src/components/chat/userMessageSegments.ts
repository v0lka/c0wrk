// Pure parsing logic for user message content: splits text into segments
// (text / skill / file refs). Extracted from UserMessageContent.tsx so the
// component file only exports a React component (react-refresh requirement).

// Combined pattern for splitting: matches /skill-name, #agent-name, or @path refs.
// Line anchors accept an optional `L` prefix (e.g. #L20-L36), consistent with
// GitHub canonical form and the backend preprocessor. The #agent-name alt must
// only match when followed by [\w-]+ with a word boundary so a trailing @file
// line anchor (@x.go#L20) is never captured as an agent.
const REF_PATTERN = /(?:^|\s)(\/[\w-]+|#([\w-]+)|@(?:[^\s\\]|\\.)+(?:#L?\d+(?:-L?\d+)?)?)/g

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
            const raw = ref.slice(1)
            // Parse line number suffix. Accept optional `L` prefix on either
            // bound; only the start line is consumed (group 1).
            const lineMatch = raw.match(/#L?(\d+)(?:-L?\d+)?$/)
            let path = lineMatch ? raw.slice(0, raw.lastIndexOf('#')) : raw
            // Unescape backslash-escaped spaces
            path = path.replace(/\\ /g, ' ')
            const lineStr = lineMatch?.[1]
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
