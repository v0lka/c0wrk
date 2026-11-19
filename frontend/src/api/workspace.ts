// Workspace API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isFileEntry, isArrayOf } from '@/types/guards'
import type { FileEntry, GitStatusEntry } from '@/types/models'

export async function getSessionWorkspace(sessionId: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.GetSessionWorkspace(sessionId)
    if (typeof result !== 'string') {
      throw new Error('getSessionWorkspace: backend returned non-string data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get session workspace:', err)
    throw err
  }
}

export async function listDirectory(path: string, recursive = false): Promise<FileEntry[]> {
  try {
    const app = getApp()
    const result = await app.ListDirectory(path, recursive)
    if (!isArrayOf(result, isFileEntry)) {
      logger.error('listDirectory: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error(recursive ? 'Failed to list directory recursively:' : 'Failed to list directory:', err)
    throw err
  }
}

/** Per-entry guard for the GetGitStatus map: the consumer (lib/gitStatus
 *  toEntries) dereferences status/staged/index_status/worktree_status on
 *  every value inside a fire-and-forget refresh — a null/non-object map
 *  value must fail the boundary instead of throwing there. The Go DTO is
 *  fully typed (strings + bool, no omitempty), so this cannot reject a
 *  conforming payload. */
function isGitStatusEntry(v: unknown): v is GitStatusEntry {
  return typeof v === 'object' && v !== null
    && typeof (v as GitStatusEntry).status === 'string'
    && typeof (v as GitStatusEntry).staged === 'boolean'
    && typeof (v as GitStatusEntry).index_status === 'string'
    && typeof (v as GitStatusEntry).worktree_status === 'string'
}

export async function getGitStatus(path: string): Promise<Record<string, GitStatusEntry>> {
  try {
    const app = getApp()
    const result = await app.GetGitStatus(path)
    return validateGitStatusMap(result)
  } catch (err) {
    logger.error('Failed to get git status:', err)
    throw err
  }
}

/**
 * The FILE TREE's status source: untracked directories stay collapsed
 * (`-unormal`), so git never enumerates every untracked file beneath them.
 * Tracked-change entries are identical to getGitStatus; untracked verdicts
 * arrive as one collapsed entry per directory (worktree_status '?'), which
 * the tree inherits down to the entries it lists. The Git panel keeps the
 * full per-file enumeration for staging.
 */
export async function getGitStatusLite(path: string): Promise<Record<string, GitStatusEntry>> {
  try {
    const app = getApp()
    const result = await app.GetGitStatusLite(path)
    return validateGitStatusMap(result)
  } catch (err) {
    logger.error('Failed to get git status (lite):', err)
    throw err
  }
}

/** Shared boundary validation for both status variants (the consumers
 *  dereference status/staged/index_status/worktree_status inside
 *  fire-and-forget refreshes — a malformed value must fail here). */
function validateGitStatusMap(result: unknown): Record<string, GitStatusEntry> {
  if (typeof result !== 'object' || result === null) {
    throw new Error('getGitStatus: backend returned invalid data')
  }
  const raw = result as Record<string, unknown>
  if (!Object.values(raw).every(isGitStatusEntry)) {
    logger.error('getGitStatus: malformed status entry in response, returning {}', result)
    return {}
  }
  return raw as Record<string, GitStatusEntry>
}

export async function watchDirectory(path: string): Promise<void> {
  try {
    const app = getApp()
    await app.WatchDirectory(path)
  } catch (err) {
    logger.error('Failed to watch directory:', err)
    throw err
  }
}

export async function unwatchDirectory(path: string): Promise<void> {
  try {
    const app = getApp()
    await app.UnwatchDirectory(path)
  } catch (err) {
    logger.error('Failed to unwatch directory:', err)
    throw err
  }
}

export async function readFile(filePath: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.ReadFile(filePath)
    if (typeof result !== 'string') {
      throw new Error('readFile: backend returned non-string data')
    }
    return result
  } catch (err) {
    logger.error('Failed to read file:', err)
    throw err
  }
}

/** Tri-state existence verdict for one candidate path: `missing` is a
 *  definitive no (the probe may move to the next candidate), `exists` is a
 *  definitive yes (even when ReadFile would refuse the file — too large, a
 *  directory, permission), and `unknown` is everything else (RPC failure,
 *  malformed response) — never evidence of absence. */
export type PathExistence = 'missing' | 'exists' | 'unknown'

/**
 * Click-time existence probe for local-file link resolution, via the
 * lightweight StatPath RPC (no payload is transferred — unlike the ReadFile
 * seam it replaces, existence is reported even for files the viewer cannot
 * read). Failures stay QUIET: the probe runs speculatively (one link click
 * may test several candidate paths, several of which legitimately do not
 * exist), so failure here is expected control flow, not an error worth
 * logging. Any failure maps to `unknown`, never to `missing`.
 */
export async function probePathStat(filePath: string): Promise<PathExistence> {
  try {
    const app = getApp()
    const stat = await app.StatPath(filePath)
    if (typeof stat !== 'object' || stat === null || typeof (stat as { exists?: unknown }).exists !== 'boolean') {
      logger.error('probePathStat: unexpected response shape', stat)
      return 'unknown'
    }
    return stat.exists ? 'exists' : 'missing'
  } catch {
    // Unknown, NOT missing: a failed probe must keep the caller on the
    // current candidate rather than silently fall through to the next one.
    return 'unknown'
  }
}

/**
 * Fetch a local file's bytes as a `data:` URL (base64-encoded). Used by the
 * markdown renderer to embed images that live on disk — the webview cannot
 * load file:// or project-root-relative URLs directly, so binary content is
 * round-tripped through this IPC. Returns the data URL string on success.
 */
export async function readFileAsDataURL(filePath: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.ReadFileAsDataURL(filePath)
    if (typeof result !== 'string') {
      throw new Error('readFileAsDataURL: backend returned non-string data')
    }
    return result
  } catch (err) {
    logger.error('Failed to read file as data URL:', err)
    throw err
  }
}

/**
 * Fetch a local file's bytes as a `data:` URL (base64-encoded) for the file
 * viewer's image tab. Unlike `readFileAsDataURL` (workspace-contained, used by
 * the markdown auto-render path) this RPC is NOT workspace-contained, so the
 * viewer can display an image the agent surfaced anywhere on disk — mirroring
 * `readFile`. It is invoked only on an explicit user action (opening an image
 * tab), never during automatic rendering.
 */
export async function readImageAsDataURL(filePath: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.ReadImageAsDataURL(filePath)
    if (typeof result !== 'string') {
      throw new Error('readImageAsDataURL: backend returned non-string data')
    }
    return result
  } catch (err) {
    logger.error('Failed to read image as data URL:', err)
    throw err
  }
}

export async function getFileDiff(filePath: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.GetFileDiff(filePath)
    if (typeof result !== 'string') {
      throw new Error('getFileDiff: backend returned non-string data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get file diff:', err)
    throw err
  }
}

export async function getFileIcon(filePath: string): Promise<{ icon: string; icon_color: string }> {
  try {
    const app = getApp()
    const result = await app.GetFileIcon(filePath)
    if (typeof result !== 'object' || result === null || typeof (result as Record<string, unknown>).icon !== 'string' || typeof (result as Record<string, unknown>).icon_color !== 'string') {
      throw new Error('getFileIcon: backend returned invalid data')
    }
    return result as { icon: string; icon_color: string }
  } catch (err) {
    logger.error('Failed to get file icon:', err)
    throw err
  }
}

/** Semantic reveal destination resolved SERVER-SIDE by RevealInFileManager —
 *  raw app-internal paths (the session temp dir) never cross the wire
 *  (specs/contracts/desktop-frontend.md). */
export type RevealTarget = 'workspace' | 'temp'

// --- Structural tree mutations + reveal (issue #60) ---
// The backend owns admission (workspace containment, No Project per-session
// isolation, symlink hardening — specs/domains/workspace.md); these wrappers
// forward string args and rethrow so the tree decides how a failure renders
// (inline validation error, confirmation dialog, runtime_error toast).

export async function createFile(sessionId: string, path: string): Promise<void> {
  try {
    const app = getApp()
    await app.CreateFile(sessionId, path)
  } catch (err) {
    logger.error('Failed to create file:', err)
    throw err
  }
}

export async function createDirectory(sessionId: string, path: string): Promise<void> {
  try {
    const app = getApp()
    await app.CreateDirectory(sessionId, path)
  } catch (err) {
    logger.error('Failed to create directory:', err)
    throw err
  }
}

export async function renameEntry(sessionId: string, oldPath: string, newPath: string): Promise<void> {
  try {
    const app = getApp()
    await app.RenameEntry(sessionId, oldPath, newPath)
  } catch (err) {
    logger.error('Failed to rename:', err)
    throw err
  }
}

export async function deleteEntry(sessionId: string, path: string): Promise<void> {
  try {
    const app = getApp()
    await app.DeleteEntry(sessionId, path)
  } catch (err) {
    logger.error('Failed to delete:', err)
    throw err
  }
}

export async function revealInFileManager(sessionId: string, target: RevealTarget, path: string): Promise<void> {
  try {
    const app = getApp()
    await app.RevealInFileManager(sessionId, target, path)
  } catch (err) {
    logger.error('Failed to reveal in file manager:', err)
    throw err
  }
}
