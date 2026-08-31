import { getApp } from './runtime'
import { logger } from '@/lib/logger'

export async function writeFile(sessionId: string, path: string, content: string): Promise<void> {
  try {
    const app = getApp()
    await app.WriteFile(sessionId, path, content)
  } catch (err) {
    logger.error('Failed to write file:', err)
    throw err
  }
}

/**
 * Save chat-message text to a Markdown file via the native save-file dialog.
 *
 * The backend opens the dialog (default directory = the active project's
 * directory), the user picks/confirms the path — including the overwrite
 * prompt — and the backend writes the file, normalizing the name to .md.
 *
 * Returns the saved absolute path, or '' when the user cancels the dialog.
 * Rejects on dialog/write failures; callers decide how to surface them.
 */
export async function saveMessageAsMarkdown(content: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.SaveMessageAsMarkdown(content)
    if (typeof result !== 'string') {
      logger.warn('saveMessageAsMarkdown: unexpected response shape, returning ""', result)
      return ''
    }
    return result
  } catch (err) {
    logger.error('Failed to save message as markdown:', err)
    throw err
  }
}
