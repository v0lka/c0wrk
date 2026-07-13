// Terminal API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'

export async function startTerminal(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.StartTerminal(sessionId)
  } catch (err) {
    logger.error('Failed to start terminal:', err)
    throw err
  }
}

export async function startTerminalInDir(sessionId: string, workDir: string): Promise<void> {
  try {
    const app = getApp()
    await app.StartTerminalInDir(sessionId, workDir)
  } catch (err) {
    logger.error('Failed to start terminal in directory:', err)
    throw err
  }
}

export async function terminalInput(sessionId: string, data: string): Promise<void> {
  try {
    const app = getApp()
    await app.TerminalInput(sessionId, data)
  } catch (err) {
    logger.error('Failed to send terminal input:', err)
    throw err
  }
}

export async function terminalResize(sessionId: string, cols: number, rows: number): Promise<void> {
  try {
    const app = getApp()
    await app.TerminalResize(sessionId, cols, rows)
  } catch (err) {
    logger.error('Failed to resize terminal:', err)
    throw err
  }
}

export async function stopTerminal(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.StopTerminal(sessionId)
  } catch (err) {
    logger.error('Failed to stop terminal:', err)
    throw err
  }
}

export async function getTerminalHistory(sessionId: string): Promise<string[]> {
  try {
    const app = getApp()
    const result = await app.GetTerminalHistory(sessionId)
    if (!Array.isArray(result)) {
      logger.warn('getTerminalHistory: unexpected response shape', result)
      return []
    }
    return result.map((c: unknown) => {
      if (typeof c === 'object' && c !== null && 'command' in c && typeof (c as Record<string, unknown>).command === 'string') {
        return (c as { command: string }).command
      }
      return ''
    }).filter(Boolean)
  } catch (err) {
    logger.error('Failed to get terminal history:', err)
    throw err
  }
}
