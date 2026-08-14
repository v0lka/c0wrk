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
    return result as unknown as ResearchStatus
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
    return result as unknown as ResearchStatus
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
    return result as unknown as ResearchGraphResponse
  } catch (err) {
    logger.error('Failed to get research graph:', err)
    throw err
  }
}
