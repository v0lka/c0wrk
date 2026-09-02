// Chat-event bookmark API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isSessionBookmark, isArrayOf } from '@/types/guards'
import type { SessionBookmark } from '@/types/models'

export async function addBookmark(sessionId: string, eventKey: string, title: string): Promise<SessionBookmark> {
  try {
    const app = getApp()
    const result = await app.AddBookmark(sessionId, eventKey, title)
    if (!isSessionBookmark(result)) {
      throw new Error('addBookmark: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to add bookmark:', err)
    throw err
  }
}

export async function listBookmarks(sessionId: string): Promise<SessionBookmark[]> {
  try {
    const app = getApp()
    const result = await app.ListBookmarks(sessionId)
    if (!isArrayOf(result, isSessionBookmark)) {
      logger.error('listBookmarks: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('Failed to list bookmarks:', err)
    throw err
  }
}

export async function deleteBookmark(sessionId: string, bookmarkId: string): Promise<void> {
  try {
    const app = getApp()
    await app.DeleteBookmark(sessionId, bookmarkId)
  } catch (err) {
    logger.error('Failed to delete bookmark:', err)
    throw err
  }
}

export async function renameBookmark(sessionId: string, bookmarkId: string, title: string): Promise<void> {
  try {
    const app = getApp()
    await app.RenameBookmark(sessionId, bookmarkId, title)
  } catch (err) {
    logger.error('Failed to rename bookmark:', err)
    throw err
  }
}
