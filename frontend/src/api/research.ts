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
 *
 * `hypothesisId` scopes the recommendation to that hypothesis (the dashboard's
 * selected card); the default empty string yields the project-level
 * recommendation.
 */
export async function getResearchNextStep(
  projectId: string,
  hypothesisId = '',
): Promise<ResearchNextStep> {
  try {
    const app = getApp()
    const result = await app.GetResearchNextStep(projectId, hypothesisId)
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
 * Make a research project (R-NNN) the ACTIVE one: the backend rewrites the
 * research root's index.md so the project's row is the last table entry (the
 * chronological rule its active-project picker applies) and returns the
 * refreshed status. Foreign/unknown R-NNNs are rejected by the backend.
 */
export async function setActiveResearch(
  projectId: string,
  researchId: string,
): Promise<ResearchStatus> {
  try {
    const app = getApp()
    const result = await app.SetActiveResearch(projectId, researchId)
    if (!isResearchStatus(result)) {
      throw new Error('Invalid research status response from backend')
    }
    return normalizeResearchStatus(result)
  } catch (err) {
    logger.error('Failed to set active research:', err)
    throw err
  }
}

/**
 * Delete a research project (R-NNN): its index rows, its directory tree, and
 * its persisted pins. Deleting the active project falls the active selection
 * through to the remaining projects. Returns the refreshed status (the
 * toggle stays on even after deleting the last project).
 */
export async function deleteResearch(
  projectId: string,
  researchId: string,
): Promise<ResearchStatus> {
  try {
    const app = getApp()
    const result = await app.DeleteResearch(projectId, researchId)
    if (!isResearchStatus(result)) {
      throw new Error('Invalid research status response from backend')
    }
    return normalizeResearchStatus(result)
  } catch (err) {
    logger.error('Failed to delete research:', err)
    throw err
  }
}

/**
 * Pin or unpin a research project (persists across restarts via the
 * project's stored pins). Idempotent in both directions; the R-NNN is
 * ownership-checked by the backend. Emits no event — the resolved promise
 * is the refresh signal (callers refetch the status to see the new pins).
 */
export async function setResearchPinned(
  projectId: string,
  researchId: string,
  pinned: boolean,
): Promise<void> {
  try {
    const app = getApp()
    await app.SetResearchPinned(projectId, researchId, pinned)
  } catch (err) {
    logger.error('Failed to set research pin:', err)
    throw err
  }
}

/**
 * Pin or unpin a hypothesis card. Pinning requires the card file to exist;
 * unpinning tolerates a deleted card, so stale pins stay removable. Emits no
 * event — the resolved promise is the refresh signal.
 */
export async function setHypothesisPinned(
  projectId: string,
  researchId: string,
  hypothesisId: string,
  pinned: boolean,
): Promise<void> {
  try {
    const app = getApp()
    await app.SetHypothesisPinned(projectId, researchId, hypothesisId, pinned)
  } catch (err) {
    logger.error('Failed to set hypothesis pin:', err)
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
  // Pins are optional on the wire (accepted missing/null like the graph
  // slices), but when present every entry must be a string path — a
  // malformed pins field fails closed at the boundary instead of rendering
  // as an uncaught store exception later.
  if (!isStringArrayOrMissing(v['pinned_research'])) return false
  const pinnedHypotheses = v['pinned_hypotheses']
  if (pinnedHypotheses !== undefined && pinnedHypotheses !== null) {
    if (!isRecord(pinnedHypotheses)) return false
    for (const cards of Object.values(pinnedHypotheses)) {
      if (!isStringArrayOrMissing(cards)) return false
    }
  }
  return true
}

/** `[]`, `null`, and missing are all accepted (Go serializes nil slices as
 *  null); anything else — or a non-string entry — fails the boundary check. */
function isStringArrayOrMissing(v: unknown): v is string[] | null | undefined {
  if (v === undefined || v === null) return true
  if (!Array.isArray(v)) return false
  return v.every((entry) => typeof entry === 'string')
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
 * title in `titleTextWidth`), the optional `parents` adjacency list, and the
 * optional long-form fields (statement / verification criterion /
 * experiment notes / decision / timebox / result). Malformed entries are
 * dropped at the boundary instead of crashing the DAG render path on a
 * backend bug/version skew.
 */
function isHypothesisNode(v: unknown): v is HypothesisNode {
  if (!isRecord(v)) return false
  if (typeof v['id'] !== 'string') return false
  if (typeof v['title'] !== 'string') return false
  if (typeof v['status'] !== 'string') return false
  const stringField = (key: string): boolean =>
    v[key] === undefined || v[key] === null || typeof v[key] === 'string'
  if (
    !stringField('timebox') ||
    !stringField('result') ||
    !stringField('statement') ||
    !stringField('verification_criterion') ||
    !stringField('experiment_notes') ||
    !stringField('decision')
  ) {
    return false
  }
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
  // Pins follow the same nil-slice normalization: the store consumes them
  // as always-present collections, never null/undefined.
  status.pinned_research ??= []
  status.pinned_hypotheses ??= {}
  return status
}

function normalizeResearchGraphResponse(res: ResearchGraphResponse): ResearchGraphResponse {
  res.graph.nodes = (res.graph.nodes ?? []).filter(isHypothesisNode)
  res.graph.edges ??= []
  res.log ??= []
  return res
}
