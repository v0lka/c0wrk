type Args = Record<string, unknown> | undefined

function str(args: Args, key: string): string {
  if (!args) return ''
  const v = args[key]
  return typeof v === 'string' ? v : ''
}

function basename(path: string): string {
  return path.split('/').pop() ?? path
}

function safeParseArgs(args: Args, rawArgs: string): Record<string, unknown> {
  if (args) return args
  try {
    const parsed = JSON.parse(rawArgs)
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) return parsed
  } catch { /* ignore */ }
  return {}
}

// --- Title extractors ---

export function extractBashTitle(args: Args, rawArgs: string): string {
  const parsed = safeParseArgs(args, rawArgs)
  return str(parsed, 'command') || 'command'
}

export function extractFileTitle(args: Args, rawArgs: string): string {
  const parsed = safeParseArgs(args, rawArgs)
  const path = str(parsed, 'path') || str(parsed, 'file_path')
  if (!path) return 'file'
  const name = basename(path)
  const start = parsed.start_line as number | undefined
  const end = parsed.end_line as number | undefined
  if (start && end) return `${name} L${start}-${end}`
  if (start) return `${name} L${start}`
  return name
}

export function extractDirTitle(args: Args, rawArgs: string): string {
  const parsed = safeParseArgs(args, rawArgs)
  const path = str(parsed, 'path') || str(parsed, 'dir_path')
  return path ? basename(path) : 'directory'
}

export function extractSearchTitle(args: Args, rawArgs: string): string {
  const parsed = safeParseArgs(args, rawArgs)
  const pattern = str(parsed, 'pattern') || str(parsed, 'query')
  if (pattern) return pattern
  const keywords = parsed.keywords
  if (Array.isArray(keywords)) return keywords.join(', ')
  return 'query'
}

export function extractUrlTitle(args: Args, rawArgs: string): string {
  const parsed = safeParseArgs(args, rawArgs)
  return str(parsed, 'url') || 'URL'
}

// extractStepOutputTitle returns the step ID for read_step_output.
export function extractStepOutputTitle(args: Args, rawArgs: string): string {
  const parsed = safeParseArgs(args, rawArgs)
  return str(parsed, 'step_id') || 'step output'
}

// extractDelegationId returns the delegation id from cancel_delegation args,
// falling back to the 'delegation' label when absent. Mirrors
// extractStepOutputTitle's single-id-with-label-fallback shape.
export function extractDelegationId(args: Args, rawArgs: string): string {
  const parsed = safeParseArgs(args, rawArgs)
  return str(parsed, 'id') || 'delegation'
}

// extractFactsTitle returns the searched keywords for search_facts.
export function extractFactsTitle(args: Args, rawArgs: string): string {
  const parsed = safeParseArgs(args, rawArgs)
  const keywords = parsed.keywords
  if (Array.isArray(keywords) && keywords.length > 0) return keywords.join(', ')
  return 'facts'
}

// extractAttachmentTitle returns the attachment id (or the 'attachment' label)
// for read_attachment. Delegates to extractAttachmentId so the attachment_id
// parsing logic lives in exactly one place.
export function extractAttachmentTitle(args: Args, rawArgs: string): string {
  return extractAttachmentId(args, rawArgs) ?? 'attachment'
}

// extractAttachmentId returns the raw attachment_id from read_attachment args,
// or undefined when absent. Unlike extractAttachmentTitle (which falls back to
// the label 'attachment'), this returns only a real id so callers can look up
// the attachment's file name and fall back to the id themselves.
export function extractAttachmentId(args: Args, rawArgs: string): string | undefined {
  const parsed = safeParseArgs(args, rawArgs)
  return str(parsed, 'attachment_id') || undefined
}

export function extractMemoTitle(toolName: string, args: Args, rawArgs: string): string {
  const parsed = safeParseArgs(args, rawArgs)
  if (toolName === 'update_checklist') return 'checklist'
  if (toolName === 'declare_step_complete') return 'step complete'
  if (toolName === 'store_fact') {
    const keywords = parsed.keywords
    if (Array.isArray(keywords) && keywords.length > 0) return `fact: ${keywords.join(', ')}`
    return 'fact'
  }
  return toolName
}

export function extractGenericTitle(toolName: string): string {
  return toolName
}

// tasksCount renders a compact "N tasks" / "1 task" marker from a `tasks`
// array arg, falling back to the bare label when absent. Shared by the
// delegate / declare_plan tool cards.
function tasksCount(parsed: Record<string, unknown>): string {
  const tasks = parsed.tasks
  if (Array.isArray(tasks) && tasks.length > 0) {
    return `${tasks.length} ${tasks.length === 1 ? 'task' : 'tasks'}`
  }
  return 'tasks'
}

// extractDelegateTitle returns a compact subagent count for delegate.
export function extractDelegateTitle(args: Args, rawArgs: string): string {
  return tasksCount(safeParseArgs(args, rawArgs))
}

// extractReflectTitle returns the reflection scope (trajectory | delegation),
// defaulting to "trajectory" when omitted.
export function extractReflectTitle(args: Args, rawArgs: string): string {
  return str(safeParseArgs(args, rawArgs), 'scope') || 'trajectory'
}

// extractDeclarePlanTitle returns a compact "mode · N tasks" marker for
// declare_plan. Mode defaults to "present".
export function extractDeclarePlanTitle(args: Args, rawArgs: string): string {
  const parsed = safeParseArgs(args, rawArgs)
  const mode = str(parsed, 'mode') || 'present'
  return `${mode} · ${tasksCount(parsed)}`
}

// extractExecutePlanTitle returns a static label: execute_plan takes no args
// (it runs the previously-declared plan), so there is nothing to extract.
export function extractExecutePlanTitle(): string {
  return 'plan'
}

// extractProposeGoalTitle returns the proposed goal condition, falling back to
// the bare label when absent.
export function extractProposeGoalTitle(args: Args, rawArgs: string): string {
  return str(safeParseArgs(args, rawArgs), 'condition') || 'goal'
}

// --- Hint extractors ---

export function extractFileHint(args: Args, rawArgs: string): string | undefined {
  const parsed = safeParseArgs(args, rawArgs)
  return str(parsed, 'path') || str(parsed, 'file_path') || undefined
}

// extractFileLine returns the first line number to position the file viewer at
// for file tools that carry a line range (e.g. read_file with start_line).
// Returns undefined when no line is specified, so the file opens without a
// cursor position. Mirrors the line logic in extractFileTitle.
export function extractFileLine(args: Args, rawArgs: string): number | undefined {
  const parsed = safeParseArgs(args, rawArgs)
  const start = parsed.start_line
  return typeof start === 'number' && start > 0 ? start : undefined
}

export function extractBashHint(args: Args, rawArgs: string): string | undefined {
  const parsed = safeParseArgs(args, rawArgs)
  const cmd = str(parsed, 'command')
  return cmd.length > 60 ? cmd : undefined
}

export function extractSearchHint(args: Args, rawArgs: string): string | undefined {
  const parsed = safeParseArgs(args, rawArgs)
  return str(parsed, 'path') || str(parsed, 'directory') || undefined
}

export function extractMcpHint(args: Args, rawArgs: string): string | undefined {
  const parsed = safeParseArgs(args, rawArgs)
  try {
    return JSON.stringify(parsed, null, 2)
  } catch {
    return rawArgs || undefined
  }
}
