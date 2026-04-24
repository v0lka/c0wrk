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
    return await app.OptimizePrompt(text) as OptimizePromptResponse
  } catch (err) {
    logger.error('Failed to optimize prompt:', err)
    throw err
  }
}
