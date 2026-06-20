export interface ParsedStep {
  title: string
  what: string
  where: string
  how: string
  acceptanceCriteria: string
}

export interface ParsedPlan {
  steps: ParsedStep[]
}

const STEP_HEADER_RE = /^# Step (\d+): (.+)$/gm

/**
 * Parse plan markdown into structured steps.
 */
export function parsePlanMarkdown(content: string): ParsedPlan {
  const steps: ParsedStep[] = []
  const matches = [...content.matchAll(STEP_HEADER_RE)]

  for (let i = 0; i < matches.length; i++) {
    const match = matches[i]!
    const stepNum = parseInt(match[1]!, 10)
    const title = `Step ${stepNum}: ${match[2]!.trim()}`

    // Extract block content (from end of this header to next header or end)
    const blockStart = (match.index ?? 0) + match[0].length
    const blockEnd = i + 1 < matches.length ? matches[i + 1]!.index! : content.length
    const block = content.slice(blockStart, blockEnd)

    const fields = extractFields(block)

    steps.push({
      title,
      what: fields.what ?? '',
      where: fields.where ?? '',
      how: fields.how ?? '',
      acceptanceCriteria: fields.acceptance_criteria ?? '',
    })
  }

  return { steps }
}

/**
 * Extract What/Where/How/Acceptance Criteria fields from a step block.
 */
function extractFields(block: string): Record<string, string> {
  const result: Record<string, string> = {}

  // Match both "### What:" and "**What**: content" formats
  const lines = block.split('\n')
  let currentField = ''
  const contentLines: string[] = []

  const flushField = () => {
    if (currentField) {
      result[currentField] = contentLines.join('\n').trim()
      contentLines.length = 0
    }
  }

  for (const line of lines) {
    const trimmed = line.trim()
    if (!trimmed) continue

    const header = detectFieldHeader(trimmed)
    if (header) {
      flushField()
      currentField = header.name
      if (header.inlineContent) {
        contentLines.push(header.inlineContent)
      }
      continue
    }

    if (currentField) {
      contentLines.push(trimmed)
    }
  }
  flushField()

  return result
}

function detectFieldHeader(line: string): { name: string; inlineContent: string } | null {
  // "### What:" format
  if (line.startsWith('###')) {
    const after = line.slice(3).trim().replace(/:$/, '')
    const name = normalizeFieldName(after)
    if (name) return { name, inlineContent: '' }
  }

  // "**What**: content" format
  if (line.startsWith('**')) {
    const after = line.slice(2)
    // Find separator: ":" or "**"
    const colonIdx = after.indexOf(':')
    const starIdx = after.indexOf('**')
    const sepIdx = colonIdx >= 0 && starIdx >= 0
      ? Math.min(colonIdx, starIdx)
      : colonIdx >= 0 ? colonIdx : starIdx >= 0 ? starIdx : -1

    if (sepIdx < 0) return null

    const name = normalizeFieldName(after.slice(0, sepIdx))
    if (name) {
      const rest = after.slice(sepIdx).replace(/^[*: ]+/, '').trim()
      return { name, inlineContent: rest }
    }
  }

  return null
}

function normalizeFieldName(name: string): string | null {
  const normalized = name.toLowerCase().trim().replace(/\s+/g, '_')
  const valid = ['what', 'where', 'how', 'acceptance_criteria']
  return valid.includes(normalized) ? normalized : null
}

/**
 * Serialize a parsed plan back to markdown, matching the Go SerializePlan format.
 */
export function serializePlanMarkdown(plan: ParsedPlan): string {
  return plan.steps
    .map(
      (s, i) =>
        `# Step ${i + 1}: ${s.title.replace(/^Step \d+: /, '')}\n\n` +
        `**What**: ${s.what || '...'}\n\n` +
        `**Where**: ${s.where || '...'}\n\n` +
        `**How**: ${s.how || '...'}\n\n` +
        `**Acceptance Criteria**: ${s.acceptanceCriteria || '...'}\n`
    )
    .join('\n')
}
