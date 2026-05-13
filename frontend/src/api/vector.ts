// Vector store API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type { SearchRequest, VectorStoreEntry } from '@/types/models'

export async function searchVectorStore(req: SearchRequest): Promise<VectorStoreEntry[]> {
  try {
    const app = getApp()
    return await app.SearchVectorStore(req) as VectorStoreEntry[]
  } catch (err) {
    logger.error('Failed to search vector store:', err)
    throw err
  }
}
