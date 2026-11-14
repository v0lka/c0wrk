// Vector store API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isArrayOf, isGPUDevice, isVectorIndexDiagnosticsDump, isVectorStoreEntry } from '@/types/guards'
import type { SearchRequest, VectorStoreEntry, VectorIndexStatus, VectorIndexDiagnosticsDump, GPUDeviceResponse } from '@/types/models'

export async function searchVectorStore(req: SearchRequest): Promise<VectorStoreEntry[]> {
  try {
    const app = getApp()
    const result = await app.SearchVectorStore(req)
    if (!isArrayOf(result, isVectorStoreEntry)) {
      logger.error('searchVectorStore: unexpected response shape, returning []', result)
      return []
    }
    return result
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
 * Collect one on-demand vector-index diagnostics bundle: the backend pairs
 * the focus root's live status snapshot (stalled sub-status included) with
 * the same capped all-goroutine stack dump the pass-stall watchdog logs as
 * evidence, writes it under ~/.c0wrk/diagnostics/ and returns where it
 * landed. Nothing is collected automatically — this RPC is the only writer.
 * Rejects with the routing cause when there is no indexable target
 * (No Project, embedder still loading, shutdown window).
 */
export async function dumpVectorIndexDiagnostics(): Promise<VectorIndexDiagnosticsDump> {
  try {
    const app = getApp()
    const result = await app.DumpVectorIndexDiagnostics()
    if (!isVectorIndexDiagnosticsDump(result)) {
      logger.error('dumpVectorIndexDiagnostics: unexpected response shape', result)
      throw new Error('unexpected diagnostics response shape')
    }
    return result
  } catch (err) {
    logger.error('Failed to dump vector index diagnostics:', err)
    throw err
  }
}

/**
 * List the GPUs visible on the machine (for the vector-index settings
 * device picker). The backend merges two external probe sources under a
 * bounded 2s probe budget each — `nvidia-smi --query-gpu=index,name`
 * (provider "cuda") and `vulkaninfo --summary` filtered to Intel
 * (provider "openvino"): machines without either tool yield an empty list
 * (absence of a driver's userspace means "no GPUs to offer", not a
 * failure); a present-but-failing driver (down, non-zero exit, timeout)
 * rejects the promise so the UI can distinguish "no hardware" from "probe
 * failed" — but only when no source answered; a healthy source wins over
 * a broken one. The result reflects what the drivers report right now —
 * the running embedder's facts travel in VectorIndexStatus, not here.
 */
export async function listVectorIndexGPUs(): Promise<GPUDeviceResponse[]> {
  try {
    const app = getApp()
    const result = await app.ListVectorIndexGPUs()
    if (!isArrayOf(result, isGPUDevice)) {
      logger.error('listVectorIndexGPUs: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('Failed to list vector index GPUs:', err)
    throw err
  }
}
