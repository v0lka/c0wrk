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
      logger.warn('listDirectory: unexpected response shape', result)
    }
    return result as FileEntry[]
  } catch (err) {
    logger.error(recursive ? 'Failed to list directory recursively:' : 'Failed to list directory:', err)
    throw err
  }
}

export async function getGitStatus(path: string): Promise<Record<string, GitStatusEntry>> {
  try {
    const app = getApp()
    return await app.GetGitStatus(path) as Record<string, GitStatusEntry>
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
    return await app.ReadFile(filePath) as string
  } catch (err) {
    logger.error('Failed to read file:', err)
    throw err
  }
}

export async function getFileDiff(filePath: string): Promise<string> {
  try {
    const app = getApp()
    return await app.GetFileDiff(filePath) as string
  } catch (err) {
    logger.error('Failed to get file diff:', err)
    throw err
  }
}

export async function getFileIcon(filePath: string): Promise<{ icon: string; icon_color: string }> {
  try {
    const app = getApp()
    return await app.GetFileIcon(filePath) as { icon: string; icon_color: string }
  } catch (err) {
    logger.error('Failed to get file icon:', err)
    throw err
  }
}
