/**
 * Pure parser for list_directory result lines (sp4rk
 * tools/builtins/file_list.go). Kept out of the ListDirBody component file so
 * that file only exports components — required for React Fast Refresh.
 */

/** One parsed `name<TAB>kind<TAB>size` listing line. */
export interface ListEntry {
  name: string
  kind: 'file' | 'dir'
  size: string
}

/**
 * Parses a list_directory output line: `name\tkind\tsize` — kind is
 * "file"|"dir" and size is the entry's byte size. Fields parse from the
 * RIGHT so names that contain tabs survive. A line that does not match the
 * shape — truncation markers, empty separators, legacy formats — yields
 * null and renders as plain text.
 */
export function parseListEntry(line: string): ListEntry | null {
  const parts = line.split('\t')
  if (parts.length < 3) return null
  const size = parts[parts.length - 1]!
  const kind = parts[parts.length - 2]!
  const name = parts.slice(0, -2).join('\t')
  if (!name) return null
  if (kind !== 'file' && kind !== 'dir') return null
  if (!/^\d+$/.test(size)) return null
  return { name, kind, size }
}
