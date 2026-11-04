// Specs panel API wrappers (ADR-105): the parsed specs/ tree of a project.

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type {
  SpecsStatus,
  SpecsCounts,
  SpecFile,
  SpecIndexTask,
  SpecADRMeta,
  SpecFinding,
} from '@/types/models'

/**
 * Get the parsed specs/ status of a project: every spec file, the INDEX.md
 * task navigation table, ADR metadata, and the tier-1 integrity findings
 * (GetSpecsStatus — one light read-only parse per call, always fresh). A
 * missing or non-directory root is the neutral present=false state, and an
 * existing empty or partial tree parses present=true with whatever content
 * is readable — never an error in either case; No Project gets the same
 * neutral state (the panel is not rendered there — the defensive contract
 * of GetResearchStatus).
 */
export async function getSpecsStatus(projectId: string): Promise<SpecsStatus> {
  try {
    const app = getApp()
    const result = await app.GetSpecsStatus(projectId)
    if (!isSpecsStatus(result)) {
      throw new Error('Invalid specs status response from backend')
    }
    return normalizeSpecsStatus(result)
  } catch (err) {
    logger.error('Failed to get specs status:', err)
    throw err
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null
}

/**
 * Go's encoding/json serializes a nil slice as `null`, not `[]`. The specs
 * DTO promises non-nil collections, but the boundary accepts `[]`, `null`,
 * and missing alike (the research contract) and normalizes to `[]` below —
 * a version-skewed or hand-rolled payload degrades instead of throwing.
 */
function isArrayOrMissing(v: unknown): v is unknown[] | null | undefined {
  return v === undefined || v === null || Array.isArray(v)
}

/** `[]`, `null`, and missing are all accepted; anything else — or a
 *  non-string entry — fails the boundary check. */
function isStringArrayOrMissing(v: unknown): v is string[] | null | undefined {
  if (v === undefined || v === null) return true
  if (!Array.isArray(v)) return false
  return v.every((entry) => typeof entry === 'string')
}

/**
 * Validate the outer shape of the specs status so a malformed backend
 * payload fails closed at the RPC boundary instead of rendering as an
 * uncaught store exception later. Entry shapes (files, index rows, ADRs,
 * findings) are validated per-entry in the normalize step below, which
 * drops malformed entries rather than rejecting the whole payload.
 */
function isSpecsStatus(v: unknown): v is SpecsStatus {
  if (!isRecord(v)) return false
  if (typeof v['present'] !== 'boolean') return false
  if (typeof v['root'] !== 'string') return false
  if (!isArrayOrMissing(v['files'])) return false
  if (!isArrayOrMissing(v['index_tasks'])) return false
  if (!isArrayOrMissing(v['adrs'])) return false
  if (!isArrayOrMissing(v['findings'])) return false
  if (v['counts'] !== undefined && v['counts'] !== null && !isRecord(v['counts'])) {
    return false
  }
  return true
}

/** A spec file is well-typed when its required key is a string and every
 *  optional field (section, title, key_files, cross_refs) is either absent,
 *  null, or of its declared shape. */
function isSpecFile(v: unknown): v is SpecFile {
  if (!isRecord(v)) return false
  if (typeof v['path'] !== 'string') return false
  const optionalString = (key: string): boolean =>
    v[key] === undefined || v[key] === null || typeof v[key] === 'string'
  if (!optionalString('section') || !optionalString('title')) return false
  if (!isStringArrayOrMissing(v['key_files'])) return false
  return isStringArrayOrMissing(v['cross_refs'])
}

function isSpecIndexTask(v: unknown): v is SpecIndexTask {
  if (!isRecord(v)) return false
  if (typeof v['task'] !== 'string') return false
  return isStringArrayOrMissing(v['specs'])
}

/** An ADR record is well-typed when every field parsed from the file name
 *  (number, slug, file) and the classified status is a string, and the
 *  optional title/superseded_by are well-typed-or-absent. */
function isSpecADRMeta(v: unknown): v is SpecADRMeta {
  if (!isRecord(v)) return false
  if (
    typeof v['number'] !== 'string' ||
    typeof v['slug'] !== 'string' ||
    typeof v['file'] !== 'string' ||
    typeof v['status'] !== 'string'
  ) {
    return false
  }
  const optionalString = (key: string): boolean =>
    v[key] === undefined || v[key] === null || typeof v[key] === 'string'
  return optionalString('title') && optionalString('superseded_by')
}

/** A finding needs all three fields as strings; the kind is NOT restricted
 *  to the four known tier-1 values — a future tier's kind passes through
 *  (typed as the union, rendered with a generic label) instead of costing
 *  the finding its place in the panel. */
function isSpecFinding(v: unknown): v is SpecFinding {
  if (!isRecord(v)) return false
  if (typeof v['kind'] !== 'string' || v['kind'] === '') return false
  if (typeof v['file'] !== 'string') return false
  return typeof v['detail'] === 'string'
}

/**
 * Normalize the wire payload to the stable store contract: every collection
 * a non-null array (malformed entries dropped per-entry, fail-closed) and
 * the counts object present — recomputed from the NORMALIZED collections,
 * so a dropped entry never leaves the panel's badges disagreeing with its
 * rendered lists.
 */
function normalizeSpecsStatus(status: SpecsStatus): SpecsStatus {
  const files = (status.files ?? []).filter(isSpecFile)
  const indexTasks = (status.index_tasks ?? []).filter(isSpecIndexTask)
  const adrs = (status.adrs ?? []).filter(isSpecADRMeta)
  const findings = (status.findings ?? []).filter(isSpecFinding)
  const findingsByKind: Record<string, number> = {}
  for (const finding of findings) {
    findingsByKind[finding.kind] = (findingsByKind[finding.kind] ?? 0) + 1
  }
  const counts: SpecsCounts = {
    files: files.length,
    adrs: adrs.length,
    findings: findings.length,
    findings_by_kind: findingsByKind,
  }
  return { ...status, files, index_tasks: indexTasks, adrs, findings, counts }
}
