// Vector store API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type { SearchRequest, VectorStoreEntry, VectorIndexStatus, GPUDeviceResponse } from '@/types/models'

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

/**
 * Force a full reindex of the active project's vector index. The pass runs in
 * the background; progress streams through `vector_index:status` events. The
 * RPC itself resolves once the pass has been scheduled (or rejects when the
 * vector index is unavailable — No Project mode or no wired manager).
 */
export async function reindexVectorIndex(): Promise<void> {
  try {
    const app = getApp()
    await app.ReindexVectorIndex()
  } catch (err) {
    logger.error('Failed to reindex vector index:', err)
    throw err
  }
}

/**
 * List the NVIDIA GPUs visible on the machine (for the vector-index
 * settings device picker). The backend shells out to
 * `nvidia-smi --query-gpu=index,name` under a bounded 2s probe budget:
 * machines without nvidia-smi yield an empty list (absence of the NVIDIA
 * userspace means "no GPUs to offer", not a failure); a present-but-failing
 * driver (down, non-zero exit, timeout) rejects the promise so the UI can
 * distinguish "no hardware" from "probe failed". The result reflects what
 * the driver reports right now — the running embedder's facts travel in
 * VectorIndexStatus, not here.
 */
export async function listVectorIndexGPUs(): Promise<GPUDeviceResponse[]> {
  try {
    const app = getApp()
    const result = await app.ListVectorIndexGPUs()
    if (!Array.isArray(result)) {
      logger.error('listVectorIndexGPUs: unexpected response shape, returning []', result)
      return []
    }
    return result as GPUDeviceResponse[]
  } catch (err) {
    logger.error('Failed to list vector index GPUs:', err)
    throw err
  }
}
