// Prompt optimization API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'

export interface OptimizePromptResponse {
  optimized_prompt: string
  keywords: string[]
  used_context: boolean
}

export async function optimizePrompt(text: string): Promise<OptimizePromptResponse> {
  try {
    const app = getApp()
    const result = await app.OptimizePrompt(text)
    if (typeof result !== 'object' || result === null || typeof (result as Record<string, unknown>).optimized_prompt !== 'string') {
      throw new Error('optimizePrompt: backend returned invalid data')
    }
    return result as OptimizePromptResponse
  } catch (err) {
    logger.error('Failed to optimize prompt:', err)
    throw err
  }
}
