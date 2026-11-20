export interface ParsedStep {
  /** Stable step id from the `# Step N (id):` header, when present (newer plans). */
  id?: string
  title: string
  what: string
  where: string
  how: string
  acceptanceCriteria: string
}

export interface ParsedPlan {
  steps: ParsedStep[]
}

// Matches both the legacy `# Step N: Summary` headers and the current Go
// SerializePlan format `# Step N (step_id): Summary` (see core/plan_serializer.go).
const STEP_HEADER_RE = /^# Step (\d+)(?: \(([^)]*)\))?: (.+)$/gm

/**
 * Parse plan markdown into structured steps.
 */
export function parsePlanMarkdown(content: string): ParsedPlan {
  const steps: ParsedStep[] = []
  const matches = [...content.matchAll(STEP_HEADER_RE)]

  for (let i = 0; i < matches.length; i++) {
    const match = matches[i]!
    const stepNum = parseInt(match[1]!, 10)
    const stepId = match[2]?.trim() ?? ''
    const title = `Step ${stepNum}: ${match[3]!.trim()}`

    // Extract block content (from end of this header to next header or end)
    const blockStart = (match.index ?? 0) + match[0].length
    const blockEnd = i + 1 < matches.length ? matches[i + 1]!.index! : content.length
    const block = content.slice(blockStart, blockEnd)

    const fields = extractFields(block)

    steps.push({
      ...(stepId ? { id: stepId } : {}),
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
  // Fence bodies stay verbatim, mirroring the Go normalizer
  // (core/plan_description.go planFenceState): no checkbox stripping and no
  // field-header detection inside an open fence.
  const fences = { in: false, marker: '`', length: 0 }

  const flushField = () => {
    if (currentField) {
      result[currentField] = contentLines.join('\n').trim()
      contentLines.length = 0
    }
  }

  for (const rawLine of lines) {
    // Mirror Go's strings.TrimSuffix(line, "\r") at line intake: in a CRLF
    // plan file every line still ends with `\r` after the '\n' split, and a
    // trailing `\r` would break planFenceCloses' pure-marker-run check (the
    // fence would never close and swallow the following fields) and leak
    // into stored fence bodies.
    const line = rawLine.endsWith('\r') ? rawLine.slice(0, -1) : rawLine
    const trimmed = line.trim()

    if (fences.in) {
      // Fence bodies stay byte-verbatim — indentation, trailing whitespace
      // and blank lines included (the Go normalizer appends fence lines
      // raw); only prose blank lines are skipped below.
      if (planFenceCloses(line, fences.marker, fences.length)) {
        fences.in = false
      }
      if (currentField) {
        contentLines.push(line)
      }
      continue
    }
    if (!trimmed) continue

    const opened = planFenceOpens(line)
    if (opened) {
      fences.in = true
      fences.marker = opened.marker
      fences.length = opened.length
      if (currentField) {
        contentLines.push(line)
      }
      continue
    }

    const header = detectFieldHeader(trimmed)
    if (header) {
      flushField()
      currentField = header.name
      if (header.inlineContent) {
        contentLines.push(header.inlineContent)
        // serializePlanMarkdown writes multi-line field values inline after
        // the label (`**How**: ```md …`), so a fence can OPEN inside the
        // header line; arm the state from the content exactly like the
        // standalone path (the closing run is still a standalone line).
        // The check content is header.fenceCheck — the mirror of Go's
        // planBoldLabelContent (one leading `:` or `-` consumed) — NOT the
        // emitted inline content, whose `*: `-class strip collapses more
        // separator shapes than Go does.
        const opened = planFenceOpens(header.fenceCheck)
        if (opened) {
          fences.in = true
          fences.marker = opened.marker
          fences.length = opened.length
        }
      }
      continue
    }

    if (currentField) {
      contentLines.push(stripCheckbox(trimmed))
    }
  }
  flushField()

  return result
}

// Task-list checkboxes are not part of the canonical plan grammar (AC are a
// plain unordered list — the Go normalizer strips them at publish, see
// core/plan_description.go stripPlanTaskMarkers). Stripping here mirrors that
// rule so legacy checkbox-bearing plans stay checkbox-free across a PlanEditor
// round-trip. Checkboxed ordered items demote to plain bullets.
function stripCheckbox(line: string): string {
  let out = line
  for (;;) {
    // `\s*` after the `]` mirrors the Go normalizer's TrimSpace: extra
    // whitespace between the checkbox and the item collapses (line-local —
    // callers pass single lines).
    const m = /^([-*+]|\d+[.)]) \[( |x|X)\]\s*(.*)$/.exec(out)
    if (!m) return out
    const bullet = /^\d/.test(m[1]!) ? '-' : m[1]!
    out = `${bullet} ${m[3]!}`.trimEnd()
  }
}

// Inline content after a field label can carry a checkbox whose list marker
// was consumed as the label separator (`**Acceptance Criteria**: - [ ] foo`
// parses the content as `[ ] foo`), so a bare box strips here too — the
// mirror of stripLabelContentCheckboxes in core/plan_description.go. All
// strips repeat to a fixed point: one strip can expose another checkbox
// (`[ ] [x] foo`), and a separator residue run a doubled label spelling
// leaves in front of a box (`:: - [ ] foo`) drops via
// stripSeparatorsToCheckbox, mirroring the Go loop.
function stripInlineCheckboxes(content: string): string {
  let out = content
  for (;;) {
    const stripped = stripCheckbox(out)
    if (stripped !== out) {
      out = stripped
      continue
    }
    const m = /^\[( |x|X)\]\s*(.*)$/.exec(out)
    if (m) {
      out = m[2]!.trimEnd()
      continue
    }
    const separated = stripSeparatorsToCheckbox(out)
    if (separated === out) return out
    out = separated
  }
}

// Mirrors stripSeparatorsToCheckbox in core/plan_description.go: a leading
// run of label-separator and emphasis characters (`:`, `*`, spaces, tabs)
// drops only when the remainder starts with a strippable checkbox, so prose
// that legitimately starts with `:` or `*` (`:: intro`, `**bold** intro`)
// stays untouched. Inside the fixed-point loop it lets `[ ] :: - [ ] x`
// converge to `- x` like Go, and lets the fenceCheck derivation expose a
// checkbox-hidden opener (`**How**: :: [ ] ```sh`) the same way the Go
// rewritten line arms.
function stripSeparatorsToCheckbox(content: string): string {
  const run = /^[:*\t ]+/.exec(content)
  if (!run || run[0]!.length >= content.length) return content
  const tail = content.slice(run[0]!.length)
  const stripped = stripCheckbox(tail)
  if (stripped !== tail) return stripped
  const bare = /^\[( |x|X)\]\s*(.*)$/.exec(tail)
  if (bare) return bare[2]!.trimEnd()
  return content
}

// Mirrors the Go fence rules (core/plan_description.go fenceOpens): a fence
// opens with a run of >= 3 backticks or tildes after up to three leading
// spaces; a backtick info string must not contain backticks.
function planFenceOpens(line: string): { marker: string; length: number } | null {
  const s = line.replace(/^ {0,3}/, '')
  if (s.length < 3 || (s[0] !== '`' && s[0] !== '~')) return null
  let n = 0
  while (n < s.length && s[n] === s[0]) n++
  if (n < 3 || (s[0] === '`' && s.slice(n).includes('`'))) return null
  return { marker: s[0]!, length: n }
}

// Mirrors the Go fenceCloses: an equally long pure run of the opening marker
// closes the fence (no info string on closing lines).
function planFenceCloses(line: string, marker: string, minLength: number): boolean {
  const s = line.replace(/^ {0,3}/, '')
  if (s.length < minLength) return false
  let n = 0
  while (n < s.length && s[n] === marker) n++
  return n === s.length
}

function detectFieldHeader(line: string): { name: string; inlineContent: string; fenceCheck: string } | null {
  // "### What:" format
  if (line.startsWith('###')) {
    const after = line.slice(3).trim().replace(/:$/, '')
    const name = normalizeFieldName(after)
    if (name) return { name, inlineContent: '', fenceCheck: '' }
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
      // The fence check mirrors Go's planInlineFenceContent on the rewritten
      // line (core/plan_description.go): Go re-parses the canonical line it
      // emitted — planBoldLabelContent takes the tail after the closing `**`,
      // trims it, consumes ONE leading `:` or `-`, and planBoldLabelLine has
      // already stripped inline checkboxes — then strips any remaining
      // separator run and runs fenceOpens on the result. The `[*: ]`-class
      // strip used for the emitted content above is NOT that rule (it would
      // eat the dash of `**How**: - ```sh` before the checkbox strip can run
      // on it, and miss that a checkbox-hidden opener `**How**: [ ] ```sh`
      // arms only through the rewritten line), so the check content is
      // derived separately.
      let fenceCheck = after.slice(starIdx + 2).trim()
      if (fenceCheck.startsWith(':') || fenceCheck.startsWith('-')) {
        fenceCheck = fenceCheck.slice(1).trim()
      }
      fenceCheck = stripInlineCheckboxes(fenceCheck)
      fenceCheck = fenceCheck.replace(/^[-: \t]+/, '')
      return { name, inlineContent: stripInlineCheckboxes(rest), fenceCheck }
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
 * Preserves the step id from the `# Step N (id):` header when present.
 */
export function serializePlanMarkdown(plan: ParsedPlan): string {
  return plan.steps
    .map(
      (s, i) =>
        `# Step ${i + 1}${s.id ? ` (${s.id})` : ''}: ${s.title.replace(/^Step \d+: /, '')}\n\n` +
        `**What**: ${s.what || '...'}\n\n` +
        `**Where**: ${s.where || '...'}\n\n` +
        `**How**: ${s.how || '...'}\n\n` +
        `**Acceptance Criteria**: ${s.acceptanceCriteria || '...'}\n`
    )
    .join('\n')
}
