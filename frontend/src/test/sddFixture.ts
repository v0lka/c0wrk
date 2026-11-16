// Builders for the framework-aware SDD payload (GetSpecsStatus) used by the
// Specs-panel and workspace-tab test suites. The shapes mirror
// backend/frontend_api_sdd.go, so a component test asserts against the same
// model the RPC boundary produces (after api/specs.ts normalization).
import type {
  SDDAction,
  SDDDocument,
  SDDFramework,
  SDDFinding,
  SDDNode,
  SDDView,
  SpecsStatus,
} from '@/types/models'

export function makeFramework(
  id: string,
  label: string,
  overrides: Partial<SDDFramework> = {},
): SDDFramework {
  return {
    id,
    label,
    origin: 'builtin',
    scope: 'project',
    tree_present: true,
    root_present: true,
    marker_present: true,
    skills: [],
    ...overrides,
  }
}

export function makeDocument(overrides: Partial<SDDDocument> = {}): SDDDocument {
  const path = overrides.path ?? 'specs/INDEX.md'
  const name = path.split('/').pop() ?? path
  return {
    path,
    root: 'specs',
    concept: 'index',
    name,
    slug: name.replace(/\.md$/, ''),
    headings: [],
    key_files: [],
    cross_refs: [],
    requirements: [],
    scenarios: [],
    deltas: [],
    principles: [],
    user_stories: [],
    task_items: [],
    tasks: [],
    missing: [],
    ...overrides,
  }
}

export function makeNode(overrides: Partial<SDDNode> = {}): SDDNode {
  const path = overrides.path ?? 'specs/INDEX.md'
  const name = path.split('/').pop() ?? path
  return {
    id: path,
    kind: 'document',
    path,
    label: name,
    concept: 'index',
    name,
    children: [],
    ...overrides,
  }
}

export function makeView(overrides: Partial<SDDView> = {}): SDDView {
  return { id: 'tree', label: 'Specification tree', kind: 'tree', nodes: [], ...overrides }
}

export function makeAction(overrides: Partial<SDDAction> = {}): SDDAction {
  return {
    kind: 'check',
    label: 'Check',
    skill: 'vibespec-check',
    prompt: 'Check the specifications for drift.',
    requires: ['root'],
    available: true,
    ...overrides,
  }
}

export function makeFinding(overrides: Partial<SDDFinding> = {}): SDDFinding {
  return {
    kind: 'INDEX_STALE',
    file: 'specs/domains/x.md',
    detail: 'not listed in INDEX.md',
    severity: 'error',
    ...overrides,
  }
}

/**
 * A complete status payload. `activeId` selects which detected framework the
 * payload reports as active (defaults to the first detected one; "" means
 * none), so a test can model the "two frameworks, one selected" state without
 * hand-writing the whole DTO.
 */
export function makeSpecsStatus(
  overrides: Partial<SpecsStatus> = {},
): SpecsStatus {
  const detected = overrides.detected ?? [makeFramework('sdd', 'Specification System')]
  const activeId =
    overrides.active_id !== undefined ? overrides.active_id : (detected[0]?.id ?? '')
  const documents = overrides.documents ?? [makeDocument()]
  const nodes = overrides.nodes ?? [makeNode()]
  const findings = overrides.findings ?? []
  const byConcept: Record<string, number> = {}
  for (const node of nodes) {
    const concept = node.concept ?? ''
    if (concept === '') continue
    byConcept[concept] = (byConcept[concept] ?? 0) + 1
  }
  const findingsByKind: Record<string, number> = {}
  for (const finding of findings) {
    findingsByKind[finding.kind] = (findingsByKind[finding.kind] ?? 0) + 1
  }
  const defaultViews: SDDView[] = [
    makeView({ id: 'tree', nodes: nodes.filter((n) => (n.parent ?? '') === '').map((n) => n.id) }),
  ]
  return {
    present: true,
    tree_present: true,
    root: '/ws/p1/specs',
    workspace: '/ws/p1',
    selector_needed: false,
    roots: [
      { id: 'specs', path: 'specs', label: 'Specification tree', required: true, present: true },
    ],
    actions: [makeAction()],
    warnings: [],
    ...overrides,
    detected,
    active_id: activeId,
    active: detected.find((fw) => fw.id === activeId),
    documents,
    nodes,
    findings,
    views: overrides.views ?? defaultViews,
    counts: overrides.counts ?? {
      documents: documents.length,
      findings: findings.length,
      by_concept: byConcept,
      findings_by_kind: findingsByKind,
    },
  }
}
