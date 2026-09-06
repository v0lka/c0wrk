// RESEARCH mode API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type {
  ResearchStatus,
  ResearchGraphResponse,
  ResearchNextStep,
  HypothesisUpdateFields,
  NewHypothesisCard,
  HypothesisNode,
} from '@/types/models'

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

/**
 * Get the single recommended next research action for a project, derived from
 * the active R-NNN's current phase. Returns the setup recommendation
 * (research-init) when there is no active R-NNN yet.
 */
export async function getResearchNextStep(projectId: string): Promise<ResearchNextStep> {
  try {
    const app = getApp()
    const result = await app.GetResearchNextStep(projectId)
    if (!isResearchNextStep(result)) {
      throw new Error('Invalid research next-step response from backend')
    }
    return result
  } catch (err) {
    logger.error('Failed to get research next step:', err)
    throw err
  }
}

/**
 * Update an existing hypothesis card and its graph entries (Mermaid node +
 * catalog row) for a research project. Returns the refreshed graph.
 *
 * `researchId` is the caller's EXPECTED research project (R-NNN) — the one
 * its UI snapshot resolved the hypothesis against. The backend validates it
 * against the project's own research root and targets exactly this project
 * (not the backend's possibly-moved active one), closing the cross-R-NNN
 * save race where H-001-style ids collide across projects.
 *
 * Status transitions are validated by the backend (no backward transitions);
 * an illegal transition rejects the whole call and leaves the files unchanged.
 */
export async function updateHypothesis(
  projectId: string,
  researchId: string,
  hypothesisId: string,
  fields: HypothesisUpdateFields,
): Promise<ResearchGraphResponse> {
  try {
    const app = getApp()
    const result = await app.UpdateHypothesis(projectId, researchId, hypothesisId, fields)
    if (!isResearchGraphResponse(result)) {
      throw new Error('Invalid research graph response from backend')
    }
    return normalizeResearchGraphResponse(result)
  } catch (err) {
    logger.error('Failed to update hypothesis:', err)
    throw err
  }
}

/**
 * Create a new hypothesis card (the backend assigns the next H-NNN id) and
 * update the graph for a project's active R-NNN. Returns the refreshed graph.
 */
export async function createHypothesis(
  projectId: string,
  newCard: NewHypothesisCard,
): Promise<ResearchGraphResponse> {
  try {
    const app = getApp()
    const result = await app.CreateHypothesis(projectId, newCard)
    if (!isResearchGraphResponse(result)) {
      throw new Error('Invalid research graph response from backend')
    }
    return normalizeResearchGraphResponse(result)
  } catch (err) {
    logger.error('Failed to create hypothesis:', err)
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

/** Validate the array-shaped fields the research UI consumes so a malformed
 *  backend payload fails closed at the RPC boundary instead of rendering as
 *  an uncaught store exception later. Node/edge ENTRY shapes are additionally
 *  validated in the normalize step below, which drops malformed entries
 *  (per-entry fail-closed) rather than rejecting the whole payload. */
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
        if (!isArrayOrMissing(p['log'])) return false
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
  if (!isArrayOrMissing(v['log'])) return false
  return true
}

/** Valid action kinds for GetResearchNextStep (mirrors core/research ActionKind). */
const RESEARCH_ACTION_KINDS: ReadonlySet<string> = new Set([
  'research-init',
  'research-hypothesis',
  'research-experiment',
  'research-decision',
  'research-synthesis',
])

function isResearchNextStep(v: unknown): v is ResearchNextStep {
  if (!isRecord(v)) return false
  if (typeof v['project_id'] !== 'string') return false
  if (typeof v['reason'] !== 'string') return false
  if (typeof v['skill'] !== 'string') return false
  if (typeof v['action'] !== 'string' || !RESEARCH_ACTION_KINDS.has(v['action'])) return false
  if (v['target'] !== undefined && v['target'] !== null && typeof v['target'] !== 'string') return false
  return true
}

/**
 * A hypothesis node is well-typed when every field the research UI consumes
 * has the declared shape: id/title/status (layout crashes on a non-string
 * title in `titleTextWidth`), and the optional `parents` adjacency list.
 * Malformed entries are dropped at the boundary instead of crashing the DAG
 * render path on a backend bug/version skew.
 */
function isHypothesisNode(v: unknown): v is HypothesisNode {
  if (!isRecord(v)) return false
  if (typeof v['id'] !== 'string') return false
  if (typeof v['title'] !== 'string') return false
  if (typeof v['status'] !== 'string') return false
  if (v['parents'] !== undefined && v['parents'] !== null) {
    if (!Array.isArray(v['parents'])) return false
    if (v['parents'].some((p) => typeof p !== 'string')) return false
  }
  return true
}

/** Normalize backend `null` slice fields to `[]` so downstream store/UI code
 *  can rely on the declared array types (e.g. `.map`, `.length`). Malformed
 *  node entries are dropped here (per-entry fail-closed). The graph path
 *  (normalizeResearchGraphResponse) normalizes only a single graph; the
 *  status path carries a project list, so every project's graph must be
 *  normalized the same way. */
function normalizeResearchStatus(status: ResearchStatus): ResearchStatus {
  if (status.root) {
    status.root.projects ??= []
    for (const project of status.root.projects) {
      project.graph.nodes = (project.graph.nodes ?? []).filter(isHypothesisNode)
      project.graph.edges ??= []
      project.log ??= []
    }
  }
  return status
}

function normalizeResearchGraphResponse(res: ResearchGraphResponse): ResearchGraphResponse {
  res.graph.nodes = (res.graph.nodes ?? []).filter(isHypothesisNode)
  res.graph.edges ??= []
  res.log ??= []
  return res
}
