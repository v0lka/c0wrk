// Specs panel API wrappers: the framework-aware SDD status of a project and
// the operator's framework selection.
//
// The backend detects which SDD frameworks are installed, resolves the active
// one, and parses that framework's DECLARED tree (backend/frontend_api_sdd.go
// → core/sdd). Nothing here knows a specification layout: the payload is
// validated at the RPC boundary (fail-closed outer shape, per-entry fail-soft)
// and normalized so the store and the panel never null-guard.

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type {
  SDDAction,
  SDDCounts,
  SDDDocument,
  SDDFramework,
  SDDFinding,
  SDDMatchedSkill,
  SDDNode,
  SDDRoot,
  SDDView,
  SpecsStatus,
} from '@/types/models'

/**
 * Get the SDD status of a project: the detected frameworks, the active one,
 * and that framework's parsed tree, findings and actions (GetSpecsStatus —
 * one light read-only parse per call, always fresh). No detected framework is
 * the neutral `present=false` shape; a detected framework whose roots are
 * absent reports `tree_present=false` while `present` stays true (the panel's
 * empty state) — never an error in either case. No Project gets the same
 * neutral state (the panel is not rendered there — the defensive contract).
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

/**
 * Persist the operator's SDD framework selection for a project. A non-empty id
 * must be DETECTED for that project, otherwise the backend rejects the call;
 * an empty id clears the selection and restores the detection default. The
 * selection is per project and survives an app restart.
 */
export async function setActiveSDD(projectId: string, sddId: string): Promise<void> {
  try {
    const app = getApp()
    await app.SetActiveSDD(projectId, sddId)
  } catch (err) {
    logger.error('Failed to set the active SDD framework:', err)
    throw err
  }
}

// --- boundary guards ---------------------------------------------------------

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null
}

/**
 * Go's encoding/json serializes a nil slice as `null`, not `[]`. The SDD DTO
 * promises non-nil collections, but the boundary accepts `[]`, `null`, and
 * missing alike and normalizes to `[]` below — a version-skewed or hand-rolled
 * payload degrades instead of throwing.
 */
function isArrayOrMissing(v: unknown): v is unknown[] | null | undefined {
  return v === undefined || v === null || Array.isArray(v)
}

/** `[]`, `null`, and missing are all accepted; anything else — or a
 *  non-string entry — fails the check. */
function isStringArrayOrMissing(v: unknown): v is string[] | null | undefined {
  if (v === undefined || v === null) return true
  if (!Array.isArray(v)) return false
  return v.every((entry) => typeof entry === 'string')
}

function isOptionalString(v: unknown): boolean {
  return v === undefined || v === null || typeof v === 'string'
}

/** An optional wire boolean: absent/null means "the payload predates the
 *  field", anything else will be read as a real answer, so a non-boolean must
 *  fail the entry closed instead of being coerced by a `=== false` test. */
function isOptionalBoolean(v: unknown): boolean {
  return v === undefined || v === null || typeof v === 'boolean'
}

/**
 * Validate the outer shape of the status so a malformed payload fails closed
 * at the RPC boundary instead of rendering as an uncaught store exception
 * later. Entry shapes are validated per-entry in the normalize step, which
 * drops malformed entries rather than rejecting the whole payload.
 */
function isSpecsStatus(v: unknown): v is SpecsStatus {
  if (!isRecord(v)) return false
  if (typeof v['present'] !== 'boolean') return false
  if (typeof v['tree_present'] !== 'boolean') return false
  if (typeof v['selector_needed'] !== 'boolean') return false
  if (typeof v['root'] !== 'string') return false
  if (typeof v['workspace'] !== 'string') return false
  for (const key of ['detected', 'roots', 'documents', 'nodes', 'views', 'findings', 'actions']) {
    if (!isArrayOrMissing(v[key])) return false
  }
  if (!isArrayOrMissing(v['warnings'])) return false
  if (!isOptionalString(v['active_id'])) return false
  if (v['counts'] !== undefined && v['counts'] !== null && !isRecord(v['counts'])) return false
  return true
}

function isMatchedSkill(v: unknown): v is SDDMatchedSkill {
  if (!isRecord(v)) return false
  return (
    typeof v['name'] === 'string' &&
    typeof v['dir'] === 'string' &&
    typeof v['scope'] === 'string'
  )
}

function isFramework(v: unknown): v is SDDFramework {
  if (!isRecord(v)) return false
  if (
    typeof v['id'] !== 'string' ||
    v['id'] === '' ||
    typeof v['label'] !== 'string' ||
    typeof v['origin'] !== 'string' ||
    typeof v['scope'] !== 'string'
  ) {
    return false
  }
  for (const key of ['tree_present', 'root_present', 'marker_present']) {
    if (typeof v[key] !== 'boolean') return false
  }
  if (!isOptionalString(v['description']) || !isOptionalString(v['docs_url'])) return false
  const skills = v['skills']
  if (skills === undefined || skills === null) return true
  return Array.isArray(skills) && skills.every(isMatchedSkill)
}

function isRoot(v: unknown): v is SDDRoot {
  if (!isRecord(v)) return false
  return (
    typeof v['id'] === 'string' &&
    typeof v['path'] === 'string' &&
    typeof v['required'] === 'boolean' &&
    typeof v['present'] === 'boolean' &&
    isOptionalString(v['label'])
  )
}

function isDocument(v: unknown): v is SDDDocument {
  if (!isRecord(v)) return false
  for (const key of ['path', 'root', 'concept', 'name', 'slug']) {
    if (typeof v[key] !== 'string') return false
  }
  for (const key of [
    'label',
    'section',
    'number',
    'title',
    'display_title',
    'date',
    'status',
    'superseded_by',
    'purpose',
  ]) {
    if (!isOptionalString(v[key])) return false
  }
  for (const key of [
    'headings',
    'key_files',
    'cross_refs',
    'requirements',
    'scenarios',
    'deltas',
    'principles',
    'user_stories',
    'missing',
  ]) {
    if (!isStringArrayOrMissing(v[key])) return false
  }
  for (const key of ['task_items', 'tasks']) {
    if (!isArrayOrMissing(v[key])) return false
  }
  return true
}

function isNode(v: unknown): v is SDDNode {
  if (!isRecord(v)) return false
  if (
    typeof v['id'] !== 'string' ||
    v['id'] === '' ||
    typeof v['kind'] !== 'string' ||
    typeof v['path'] !== 'string' ||
    typeof v['label'] !== 'string' ||
    typeof v['name'] !== 'string'
  ) {
    return false
  }
  if (!isOptionalString(v['concept']) || !isOptionalString(v['parent'])) return false
  return isStringArrayOrMissing(v['children']) && isStringArrayOrMissing(v['missing'])
}

function isView(v: unknown): v is SDDView {
  if (!isRecord(v)) return false
  return (
    typeof v['id'] === 'string' &&
    typeof v['label'] === 'string' &&
    typeof v['kind'] === 'string' &&
    isStringArrayOrMissing(v['nodes']) &&
    // Boundary guard for the section-membership set: SpecsPanel builds
    // `new Set(view.paths)` for the tree filter, so a non-array (or
    // non-string-element) payload must drop the whole view per-entry instead
    // of reaching the Set as arbitrary wire data.
    isStringArrayOrMissing(v['paths'])
  )
}

/** A finding needs its three strings; the kind is NOT restricted to a known
 *  vocabulary — the finding tokens belong to the framework document, so an
 *  unknown kind passes through and renders with its own label. */
function isFinding(v: unknown): v is SDDFinding {
  if (!isRecord(v)) return false
  if (typeof v['kind'] !== 'string' || v['kind'] === '') return false
  if (typeof v['file'] !== 'string' || typeof v['detail'] !== 'string') return false
  return isOptionalString(v['message']) && isOptionalString(v['severity'])
}

function isAction(v: unknown): v is SDDAction {
  if (!isRecord(v)) return false
  if (typeof v['kind'] !== 'string' || v['kind'] === '') return false
  if (typeof v['label'] !== 'string' || typeof v['available'] !== 'boolean') return false
  if (!isOptionalString(v['skill']) || !isOptionalString(v['prompt'])) return false
  if (!isOptionalString(v['concept'])) return false
  // The skill fact is a separate wire boolean (see SDDAction): validate it like
  // `available` so a malformed value cannot slip past the reason selection.
  if (!isOptionalBoolean(v['skill_available'])) return false
  return isStringArrayOrMissing(v['requires']) && isStringArrayOrMissing(v['unavailable_at'])
}

/**
 * Normalize the wire payload to the stable store contract: every collection a
 * non-null array (malformed entries dropped per-entry, fail-closed), the
 * counts recomputed from the NORMALIZED model so a dropped entry never leaves
 * a badge disagreeing with the rendered list, and the active framework record
 * consistent with `active_id` (a stale record is dropped rather than shown
 * beside a different active id).
 */
function normalizeSpecsStatus(status: SpecsStatus): SpecsStatus {
  const detected = (status.detected ?? []).filter(isFramework)
  const roots = (status.roots ?? []).filter(isRoot)
  const documents = (status.documents ?? []).filter(isDocument)
  // The guards above accept a nil/absent collection for every ENTRY field too
  // (Go serializes a nil slice as `null`), so the entry collections are
  // repaired here, not only at the top level: the render path iterates
  // `node.children`, reads `view.nodes` and evaluates `action.requires`, and
  // an absent array there throws inside render rather than degrading.
  const nodes = (status.nodes ?? []).filter(isNode).map((node) => ({
    ...node,
    children: node.children ?? [],
  }))
  const views = (status.views ?? []).filter(isView).map(({ paths, ...view }) => ({
    ...view,
    nodes: view.nodes ?? [],
    // `paths` is OPTIONAL (absent = no membership filter), so a null must
    // collapse to undefined rather than survive as a set-less null. The key is
    // destructured away first, so this conditional spread ADDS it on the
    // present case instead of failing to remove an already-copied null.
    ...(paths === undefined || paths === null ? {} : { paths }),
  }))
  const findings = (status.findings ?? []).filter(isFinding)
  const actions = (status.actions ?? []).filter(isAction).map((action) => ({
    ...action,
    requires: action.requires ?? [],
  }))
  const warnings = (status.warnings ?? []).filter((w): w is string => typeof w === 'string')
  const activeId = typeof status.active_id === 'string' ? status.active_id : ''

  const active =
    status.active !== undefined && status.active !== null && isFramework(status.active) &&
    (activeId === '' || status.active.id === activeId)
      ? status.active
      : detected.find((fw) => fw.id === activeId)

  const findingsByKind: Record<string, number> = {}
  for (const finding of findings) {
    findingsByKind[finding.kind] = (findingsByKind[finding.kind] ?? 0) + 1
  }
  // Instances per concept, directory concepts included — the same figure the
  // backend's parser reports, recomputed from the nodes the panel renders.
  const byConcept: Record<string, number> = {}
  for (const node of nodes) {
    const concept = node.concept ?? ''
    if (concept === '') continue
    byConcept[concept] = (byConcept[concept] ?? 0) + 1
  }
  const counts: SDDCounts = {
    documents: documents.length,
    findings: findings.length,
    by_concept: byConcept,
    findings_by_kind: findingsByKind,
  }

  return {
    ...status,
    detected,
    roots,
    documents,
    nodes,
    views,
    findings,
    actions,
    warnings,
    active_id: activeId,
    active,
    counts,
  }
}
