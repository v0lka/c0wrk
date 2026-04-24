// Vector store API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type { VectorStoreEntry } from '@/types/models'

export async function searchVectorStore(query: string, topK: number, filePattern: string): Promise<VectorStoreEntry[]> {
  try {
    const app = getApp()
    return await app.SearchVectorStore(query, topK, filePattern) as VectorStoreEntry[]
  } catch (err) {
    logger.error('Failed to search vector store:', err)
    throw err
  }
}
