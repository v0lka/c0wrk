// Vector store API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type { SearchRequest, VectorStoreEntry, VectorIndexStatus } from '@/types/models'

export async function searchVectorStore(req: SearchRequest): Promise<VectorStoreEntry[]> {
  try {
    const app = getApp()
    const result = await app.SearchVectorStore(req)
    if (!Array.isArray(result)) {
      logger.error('searchVectorStore: unexpected response shape, returning []', result)
      return []
    }
    return result as VectorStoreEntry[]
  } catch (err) {
    logger.error('Failed to search vector store:', err)
    throw err
  }
}

export async function getVectorIndexStatus(): Promise<VectorIndexStatus> {
  try {
    const app = getApp()
    const result = await app.GetVectorIndexStatus()
    return result as VectorIndexStatus
  } catch (err) {
    logger.error('Failed to get vector index status:', err)
    throw err
  }
}
