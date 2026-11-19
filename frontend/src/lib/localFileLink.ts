// Pure utility functions for detecting, parsing, and validating local file link hrefs
// in markdown output. No React or store dependencies.

const PROTOCOL_RE = /^[a-z][a-z0-9+.-]*:/i

/**
 * Windows drive prefix: a single ASCII letter followed by a colon (`C:`,
 * `c:`). RFC 3986's grammar would accept a one-letter scheme (`C:` parses
 * as URI scheme "c"), but no real-world scheme is a single letter — every
 * such spelling is in practice a local Windows path: drive-rooted
 * (`C:\Documents`), forward-slash (`C:/ws/file`) or drive-relative
 * (`C:foo`). It must never be classified as an external URL and therefore
 * never reach `openExternalURL`/`BrowserOpenURL`.
 */
const WINDOWS_DRIVE_RE = /^[A-Za-z]:/

/**
 * Returns true if the href should be treated as a local file link
 * (i.e., not an external URL, anchor, or known protocol). A Windows drive
 * prefix (`C:` — see WINDOWS_DRIVE_RE) always counts as a local file path,
 * even though it syntactically resembles a scheme.
 */
export function isLocalFileHref(href: string | undefined): boolean {
  if (!href) return false
  if (href.startsWith('#')) return false
  if (WINDOWS_DRIVE_RE.test(href)) return true
  if (PROTOCOL_RE.test(href)) return false
  return true
}

/**
 * Returns true if the href is an external URL with a scheme
 * (http, https, mailto, ftp, etc.). Such links must be dispatched to the
 * system browser via `openExternalURL` — the Wails webview ignores
 * `<a target="_blank">` or opens it inside the webview, which cannot render
 * arbitrary web pages. A Windows drive prefix (`C:`) parses
 * as a URI scheme but is never a real one: those hrefs are local file
 * paths and are never external URLs.
 */
export function isExternalUrl(href: string | undefined): boolean {
  return !!href && !WINDOWS_DRIVE_RE.test(href) && PROTOCOL_RE.test(href)
}

const LINE_SUFFIX_RE = /#L(\d+)(?:-L?\d+)?$/

/**
 * Extracts the file path and optional line number from a local file href.
 * Supports formats: `path/file.ts#L42`, `path/file.ts#L5-10`, and the GitHub
 * canonical range forms `path/file.ts#L5-L10` / `path/file.ts#L20-L36`
 * (uses start line). Decodes URI-encoded characters in the path.
 */
export function parseLocalFileHref(href: string): { path: string; line?: number } {
  const match = LINE_SUFFIX_RE.exec(href)
  let path: string
  let line: number | undefined

  if (match) {
    path = href.slice(0, href.lastIndexOf('#'))
    line = parseInt(match[1]!, 10)
  } else {
    path = href
  }

  try {
    path = decodeURIComponent(path)
  } catch {
    // If decoding fails (malformed URI), use the raw path
  }

  return line !== undefined ? { path, line } : { path }
}

/**
 * Drive-rooted Windows absolute path: a single ASCII drive letter followed by
 * a colon and a separator (`C:\ws`, `C:/ws`). Drive-relative forms (`C:foo`)
 * do NOT match — mirroring Go's `filepath.IsAbs` Windows semantics.
 */
const DRIVE_ROOTED_RE = /^[A-Za-z]:[\\/]/

/**
 * UNC absolute path: two leading separators (`\\srv\share`, `//srv/share`).
 */
const UNC_RE = /^[\\/]{2}/

/**
 * Returns true if the path is absolute in a GOOS-independent sense, mirroring
 * the Go-side `IsAbsolutePath` (core/pathabs.go):
 *   - POSIX absolute: leading `/`
 *   - Windows drive-rooted: `^[A-Za-z]:[\\/]` (`C:\ws`, `C:/ws`)
 *   - Windows UNC: `^[\\/]{2}` (`\\srv\share`, `//srv/share`)
 */
export function isAbsolutePath(p: string): boolean {
  if (!p) return false
  if (p.startsWith('/')) return true
  if (UNC_RE.test(p)) return true
  return DRIVE_ROOTED_RE.test(p)
}

/** Collapses `.`/`..` segments; `..` never pops below `floor` anchored segments. */
function normalizeSegments(parts: string[], floor: number): string[] {
  const segments: string[] = []

  for (const part of parts) {
    if (part === '' || part === '.') continue
    if (part === '..') {
      if (segments.length > floor) segments.pop()
    } else {
      segments.push(part)
    }
  }

  return segments
}

/**
 * Normalizes an absolute path, preserving its root dialect in canonical
 * forward-slash form: POSIX `/a/b`, Windows drive `C:/a/b` (no leading slash),
 * UNC `//server/share/a/b`. `..` is clamped at the anchored root — it can
 * never rise above the filesystem root, the drive root, or the UNC share.
 */
function normalizeAbsolute(p: string): string {
  // UNC: `//server/share` is the anchor — the first two segments are pinned.
  if (p.startsWith('//')) {
    const segments = normalizeSegments(p.split('/'), 2)
    return '//' + segments.join('/')
  }

  // Drive-rooted: the drive letter is the anchor; the canonical form has no
  // leading slash before the drive letter.
  if (DRIVE_ROOTED_RE.test(p)) {
    const segments = normalizeSegments(p.slice(2).split('/'), 0)
    return p.slice(0, 2) + '/' + segments.join('/')
  }

  // POSIX: the leading slash is the anchor.
  const segments = normalizeSegments(p.split('/'), 0)
  return '/' + segments.join('/')
}

/**
 * Resolves a file path against the workspace root, normalizes `.` and `..` segments,
 * and returns the normalized absolute path in canonical forward-slash form.
 * Windows drive (`C:\...`), UNC (`\\srv\share\...`) and POSIX (`/...`) absolute
 * inputs are recognized and normalized WITHOUT joining the workspace root —
 * the drive/UNC/POSIX form is preserved. Relative paths (backslashes included)
 * are joined with the normalized root first. `..` inside an absolute path is
 * clamped at its anchor root (filesystem root / drive root / UNC share);
 * relative joins that traverse above the workspace root are returned normalized
 * rather than rejected, so the viewer can open files anywhere on disk.
 */
export function normalizePath(rootPath: string, filePath: string): string {
  const normalized = filePath.replace(/\\/g, '/')

  if (isAbsolutePath(filePath)) {
    return normalizeAbsolute(normalized)
  }

  const normalizedRoot = rootPath.replace(/\\/g, '/')
  return normalizeAbsolute(normalizedRoot + '/' + normalized)
}

/**
 * Resolves a clicked `@file` reference from a user message to the path handed
 * to the file viewer. Absolute inputs — POSIX (`/...`), Windows drive
 * (`C:\...`) and UNC (`\\srv\share\...`) — are recognized by isAbsolutePath
 * and normalized WITHOUT joining the workspace root, so a `C:\Doc\f.md` ref
 * becomes the canonical `C:/Doc/f.md` instead of being glued onto the root.
 * Relative inputs are joined with the workspace root (backslash roots and
 * paths included) and normalized; without a root there is nothing to resolve
 * against and the input is returned unchanged.
 */
export function resolveFileRefPath(rootPath: string | null | undefined, path: string): string {
  if (!path) return path

  // Absolute input: normalizePath never consults the root in this branch, so
  // an empty root placeholder is safe.
  if (isAbsolutePath(path)) return normalizePath('', path)

  if (!rootPath) return path
  return normalizePath(rootPath, path)
}

/**
 * Builds the ordered list of absolute paths to try when resolving a clicked
 * local-file link href. Absolute inputs — POSIX (`/...`), Windows drive
 * (`C:\...`) and UNC (`\\srv\share\...`) — yield the single normalized
 * candidate. Relative paths yield the containing document's directory first
 * (markdown's own resolution semantics: a link resolves against the file it
 * is written in), then the workspace root as a fallback — the same
 * dir-first/root-fallback candidate order the image resolver uses, covering
 * both the "next to the document" and "relative to the repo root"
 * conventions. Duplicates collapse, so a document at the workspace root
 * probes once. Returns an empty array when neither base is available, so a
 * root-less chat stays inert instead of opening a CWD-relative guess.
 */
export function candidateLinkPaths(
  path: string,
  baseFilePath: string | null | undefined,
  workspaceRoot: string | null | undefined,
): string[] {
  if (!path) return []

  if (isAbsolutePath(path)) return [normalizePath('', path)]

  const candidates: string[] = []
  const push = (candidate: string): void => {
    if (!candidates.includes(candidate)) candidates.push(candidate)
  }

  if (baseFilePath) {
    // Windows document paths arrive with backslashes; the directory slice
    // needs forward slashes to find the last separator (and so does the
    // concatenation below).
    const normalizedBase = baseFilePath.replace(/\\/g, '/')
    const slash = normalizedBase.lastIndexOf('/')
    const dir = slash >= 0 ? normalizedBase.slice(0, slash) : ''
    if (dir) push(normalizePath('', dir + '/' + path))
  }
  if (workspaceRoot) push(normalizePath(workspaceRoot, path))
  return candidates
}

/**
 * Computes a POSIX-style relative path by stripping the workspace root prefix
 * from an absolute path. Converts backslashes to forward slashes.
 * Returns '.' if the absolute path equals the workspace root.
 * If the path is not under the workspace root, returns the absolute path
 * with forward slashes (without a leading '/') as a best-effort fallback.
 */
export function relativePath(rootPath: string, absolutePath: string): string {
  const normalizedAbs = absolutePath.replace(/\\/g, '/')
  const normalizedRoot = rootPath.replace(/\\/g, '/')

  if (normalizedAbs === normalizedRoot) {
    return '.'
  }

  const prefix = normalizedRoot.endsWith('/') ? normalizedRoot : normalizedRoot + '/'

  if (normalizedAbs.startsWith(prefix)) {
    return normalizedAbs.slice(prefix.length)
  }

  // Not under workspace root — return as a forward-slash path without leading '/'
  return normalizedAbs.startsWith('/') ? normalizedAbs.slice(1) : normalizedAbs
}
