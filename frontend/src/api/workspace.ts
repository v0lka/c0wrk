// Workspace API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isFileEntry, isArrayOf } from '@/types/guards'
import type { FileEntry, GitStatusEntry } from '@/types/models'

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

export async function getGitStatus(path: string): Promise<Record<string, GitStatusEntry>> {
  try {
    const app = getApp()
    const result = await app.GetGitStatus(path)
    if (typeof result !== 'object' || result === null) {
      throw new Error('getGitStatus: backend returned invalid data')
    }
    return result as Record<string, GitStatusEntry>
  } catch (err) {
    logger.error('Failed to get git status:', err)
    throw err
  }
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
