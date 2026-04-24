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
    const commands = await app.GetTerminalHistory(sessionId) as Array<{ command: string }>
    return commands.map(c => c.command)
  } catch (err) {
    logger.error('Failed to get terminal history:', err)
    return []
  }
}
