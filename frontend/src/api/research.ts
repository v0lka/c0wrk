// RESEARCH mode API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type { ResearchStatus, ResearchGraphResponse } from '@/types/models'

/**
 * Enable RESEARCH mode for a project. Seeds the methodology skill-pack,
 * persists the research root, and returns the parsed research status.
 *
 * Pass an empty rootPath to use the project's default research directory
 * (<workspace>/.research); pass an explicit path to use a custom root.
 */
export async function enableResearch(
  projectId: string,
  rootPath = '',
): Promise<ResearchStatus> {
  try {
    const app = getApp()
    const result = await app.EnableResearch(projectId, rootPath)
    if (!isResearchStatus(result)) {
      throw new Error('Invalid research status response from backend')
    }
    return normalizeResearchStatus(result)
  } catch (err) {
    logger.error('Failed to enable research:', err)
    throw err
  }
}

/** Disable RESEARCH mode for a project (clears the toggle; preserves files). */
export async function disableResearch(projectId: string): Promise<void> {
  try {
    const app = getApp()
    await app.DisableResearch(projectId)
  } catch (err) {
    logger.error('Failed to disable research:', err)
    throw err
  }
}

/**
 * Get the live RESEARCH mode status for a project: the toggle plus the parsed
 * research root (graph + metrics + project list). Returns an empty-state DTO
 * (enabled=false, no root) when RESEARCH is off.
 */
export async function getResearchStatus(projectId: string): Promise<ResearchStatus> {
  try {
    const app = getApp()
    const result = await app.GetResearchStatus(projectId)
    if (!isResearchStatus(result)) {
      throw new Error('Invalid research status response from backend')
    }
    return normalizeResearchStatus(result)
  } catch (err) {
    logger.error('Failed to get research status:', err)
    throw err
  }
}

/**
 * Get only the hypothesis graph and metrics for a research project.
 * Lightweight alternative to getResearchStatus for incremental file-change
 * updates — avoids parsing the full research root (index, brief, prior-art).
 */
export async function getResearchGraph(projectId: string): Promise<ResearchGraphResponse> {
  try {
    const app = getApp()
    const result = await app.GetResearchGraph(projectId)
    if (!isResearchGraphResponse(result)) {
      throw new Error('Invalid research graph response from backend')
    }
    return normalizeResearchGraphResponse(result)
  } catch (err) {
    logger.error('Failed to get research graph:', err)
    throw err
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null
}

/**
 * Go's encoding/json serializes a nil slice as `null`, not `[]`. The research
 * DTOs carry slices without `omitempty` (root.projects, graph.nodes,
 * graph.edges), so a freshly initialized (or empty) research root legitimately
 * arrives with those keys set to null. Accept `[]`, `null`, and missing alike.
 */
function isArrayOrMissing(v: unknown): v is unknown[] | null | undefined {
  return v === undefined || v === null || Array.isArray(v)
}

/** Validate the fields the research UI actually consumes so a malformed
 *  backend payload fails closed at the RPC boundary instead of rendering as
 *  an uncaught store exception later. */
function isResearchStatus(v: unknown): v is ResearchStatus {
  if (!isRecord(v)) return false
  if (typeof v['enabled'] !== 'boolean') return false
  if (typeof v['project_id'] !== 'string') return false
  if (typeof v['research_root'] !== 'string') return false
  if (v['root'] !== undefined && v['root'] !== null) {
    const root = v['root']
    if (!isRecord(root) || !isArrayOrMissing(root['projects'])) return false
    const projects = root['projects']
    if (Array.isArray(projects)) {
      for (const p of projects) {
        if (!isRecord(p) || !isRecord(p['graph'])) return false
        if (!isArrayOrMissing(p['graph']['nodes']) || !isArrayOrMissing(p['graph']['edges'])) return false
      }
    }
  }
  return true
}

function isResearchGraphResponse(v: unknown): v is ResearchGraphResponse {
  if (!isRecord(v)) return false
  if (typeof v['project_id'] !== 'string') return false
  if (typeof v['has_report'] !== 'boolean') return false
  if (!isRecord(v['graph'])) return false
  if (!isArrayOrMissing(v['graph']['nodes']) || !isArrayOrMissing(v['graph']['edges'])) return false
  if (!isRecord(v['metrics'])) return false
  return true
}

/** Normalize backend `null` slice fields to `[]` so downstream store/UI code
 *  can rely on the declared array types (e.g. `.map`, `.length`). The graph
 *  path (normalizeResearchGraphResponse) normalizes only a single graph; the
 *  status path carries a project list, so every project's graph must be
 *  normalized the same way. */
function normalizeResearchStatus(status: ResearchStatus): ResearchStatus {
  if (status.root) {
    status.root.projects ??= []
    for (const project of status.root.projects) {
      project.graph.nodes ??= []
      project.graph.edges ??= []
    }
  }
  return status
}

function normalizeResearchGraphResponse(res: ResearchGraphResponse): ResearchGraphResponse {
  res.graph.nodes ??= []
  res.graph.edges ??= []
  return res
}
