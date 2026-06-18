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

export function extractMemoTitle(toolName: string, args: Args, rawArgs: string): string {
  const parsed = safeParseArgs(args, rawArgs)
  if (toolName === 'set_step_status') return 'step status'
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

// --- Hint extractors ---

export function extractFileHint(args: Args, rawArgs: string): string | undefined {
  const parsed = safeParseArgs(args, rawArgs)
  return str(parsed, 'path') || str(parsed, 'file_path') || undefined
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
