// The SPECS panel: a view over specsStore plus the refresh subscription.
//
// Nothing here knows a specification layout. The panel renders the ACTIVE
// framework's declared model: its view SECTIONS (each `tree` section as a
// nested node tree, each `findings`/`checks` section as the findings list), its
// declared ACTIONS (the quick-actions row, the per-node gestures, the findings
// gesture), and its own labels. Detection decides whether the surface exists at
// all; the framework selector decides which tree is parsed.
//
// Reads open in the file viewer; every mutating gesture dispatches a DECLARED
// skill. The panel never writes to the tree itself.

import { useCallback, useEffect, useState } from 'react'
import { AlertCircle, Info } from 'lucide-react'
import { subscribe } from '@/api/runtime'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useSpecsStore } from '@/stores/specsStore'
import { useSddStore } from '@/stores/sddStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { SpecsHeader } from './SpecsHeader'
import { SpecsQuickActions } from './SpecsQuickActions'
import { SpecsEmptyState } from './SpecsEmptyState'
import { SpecsSectionTree } from './SpecsSectionTree'
import { SpecsFindings } from './SpecsFindings'
import { isSpecsRelevantChange, joinWorkspacePath } from './specsPaths'
import {
  dispatchableAction,
  findingsFixAction,
  findingsFixDispatch,
  type SpecsDispatch,
} from './specsActions'
import { useSpecsDispatch } from './useSpecsDispatch'
import type { SDDFinding, SDDView, SpecsStatus } from '@/types/models'

/** One implicit tree section for a framework that declares NO views: the
 *  forest's root nodes, in model order. Module-level so the prop stays
 *  referentially stable across renders. */
const IMPLICIT_TREE_SECTION: SDDView = {
  id: 'all',
  label: 'Specification tree',
  kind: 'tree',
  nodes: [],
}

/**
 * Refresh model: one light read-only parse on mount — the tabpanel mounts only
 * while the Specs tab is active, so every tab activation refreshes — plus a
 * refetch on `workspace:tree_changed` events touching the active root.
 *
 * The subscription lives HERE, inside the panel, deliberately NOT in an
 * App-root bridge: this panel is the single consumer of the parsed status
 * (the sddStore projection exists for the tab strip and the selector, and it is
 * fed by this same fetch), so a bridge would be a second always-mounted
 * subscription for nothing. The panel unmounts with the tab and unsubscribes
 * with it.
 */
export function SpecsPanel() {
  const projectId = useProjectStore((s) => s.activeProjectId)
  // The parse is session-scoped on the backend (it walks the ACTIVE session's
  // workspace), so the panel follows the session exactly like it follows the
  // project. Primitive selector — referentially stable.
  const sessionId = useSessionStore((s) => s.activeSessionId)
  // Cross-project AND cross-session stale-status guard: the store's status
  // renders only while it belongs to the ACTIVE (project, session) pair.
  const status = useSpecsStore((s) =>
    projectId !== null && s.projectId === projectId && s.sessionId === sessionId
      ? s.status
      : null,
  )
  const isLoading = useSpecsStore((s) => s.isLoading)
  const error = useSpecsStore((s) => s.error)
  const fetchStatus = useSpecsStore((s) => s.fetchStatus)
  const selectFramework = useSpecsStore((s) => s.selectFramework)
  // This project's persisted tree expansion plus the store toggle: the tree
  // call sites get BOTH (the sidebar panel's contract — expansion survives
  // tab switches through the persisted slice).
  const treeExpanded = useSpecsStore((s) =>
    projectId !== null ? s.treeExpandedNodes[projectId] : undefined,
  )
  const toggleTreeNode = useSpecsStore((s) => s.toggleTreeNode)
  // Framework context (sddStore): the OPTIMISTIC selection the selector shows
  // while a switch re-parses, and whether that switch is still in flight. Only
  // primitives are selected, so the selectors stay referentially stable.
  const sddProjectId = useSddStore((s) => s.projectId)
  const sddActiveId = useSddStore((s) => s.activeId)
  const switching = useSddStore((s) => s.switching)
  const dispatch = useSpecsDispatch()

  // Mount / project switch / session switch: the initial parse. The backend
  // resolves the fetch against the ACTIVE session's workspace, so a session
  // switch is a refetch just like a project switch (the store's session stamp
  // keeps the previous session's parse from surviving it). Never in No-Project
  // (the tab strip is absent there; the guard below is the defensive neutral).
  useEffect(() => {
    if (projectId !== null) void fetchStatus(projectId)
  }, [projectId, fetchStatus, sessionId])

  // tree_changed → refetch, scoped to the active root when the payload names
  // paths (fail-open otherwise — see isSpecsRelevantChange). The root is read
  // from the store at event time, not captured at render. Re-keyed on the
  // session too: the subscription belongs to the same (project, session) parse
  // the mount fetch starts.
  useEffect(() => {
    if (projectId === null) return
    const onChange = (data: unknown) => {
      const store = useSpecsStore.getState()
      if (!isSpecsRelevantChange(data, store.status?.root ?? '')) return
      void store.fetchStatus(projectId)
    }
    return subscribe('workspace:tree_changed', onChange)
  }, [projectId, sessionId])

  // Read path: a model path is WORKSPACE-relative, so it resolves against the
  // parse's own workspace and opens through the existing file viewer — no new
  // file-read RPC. The workspace is read from the store AT CLICK TIME and only
  // from the CURRENT (project, session) parse: a stale cross-project or
  // cross-session workspace must never reach the viewer (defense in depth under
  // the render guard — the clickable rows themselves are already scoped).
  const openModelPath = useCallback(
    (modelPath: string) => {
      const store = useSpecsStore.getState()
      if (
        store.projectId !== projectId ||
        store.sessionId !== sessionId ||
        store.status === null
      )
        return
      const viewer = useFileViewerStore.getState()
      viewer.setCollapsed(false)
      viewer.openFile(joinWorkspacePath(store.status.workspace, modelPath))
    },
    [projectId, sessionId],
  )

  // The findings gesture: the framework's declared findings-fix action, with
  // the CURRENT parse's findings enumerated verbatim. Built at click time from
  // the store so the task sees exactly what the panel renders (a mid-flight
  // refetch updates the list). Null without a declared action — the badge and
  // the per-finding chips then render inert.
  const onFixFindings = useCallback(
    (newSession: boolean) => {
      const store = useSpecsStore.getState()
      if (
        store.projectId !== projectId ||
        store.sessionId !== sessionId ||
        store.status === null
      )
        return
      const pending = findingsFixDispatch(
        findingsFixAction(store.status.actions),
        store.status.findings,
      )
      if (pending !== null) setPendingDispatch({ dispatch: pending, newSession })
    },
    [projectId, sessionId],
  )

  // Manual refresh: the same refetch as the mount/tree_changed paths — a
  // same-(project, session) fetch keeps the previous status rendered (no
  // flicker) and the store's LWW ticket (fetchSeq) serializes overlapping
  // flights, so a slow response never clobbers a newer parse.
  const onRefresh = useCallback(() => {
    if (projectId !== null) void fetchStatus(projectId)
  }, [projectId, fetchStatus])

  const onSelectFramework = useCallback(
    (id: string) => {
      void selectFramework(id)
    },
    [selectFramework],
  )

  // Dispatch GATE: the prompt a gesture sends is DECLARED by framework
  // documents — including a project's operator DSL (specs.yaml), which is
  // repository content, not UI copy. Every mutating gesture therefore parks
  // its dispatch here instead of sending straight away, and the dialog below
  // shows the FULL prompt verbatim before it enters the chat as the user's
  // own message; confirm sends, dismissing (cancel, Esc, overlay) drops it.
  const [pendingDispatch, setPendingDispatch] = useState<{
    dispatch: SpecsDispatch
    newSession: boolean
  } | null>(null)
  const onDispatch = useCallback(
    (pending: SpecsDispatch, newSession: boolean) =>
      setPendingDispatch({ dispatch: pending, newSession }),
    [],
  )
  const confirmDispatch = useCallback(() => {
    if (pendingDispatch === null) return
    const { dispatch: pending, newSession } = pendingDispatch
    setPendingDispatch(null)
    dispatch(pending, newSession)
  }, [pendingDispatch, dispatch])

  // Defensive No-Project neutral render (the tab never mounts there).
  if (projectId === null) return null

  // The declared findings-fix gesture (or null): the ONE declaration the
  // badge, the per-finding chips and the click gate below all read, so no
  // surface can look live while the dispatch refuses it.
  const fixAction = status !== null ? findingsFixAction(status.actions) : null

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="specs-panel">
      {error !== null && (
        <div className="flex items-center gap-1.5 border-b border-destructive/20 bg-destructive/10 px-3 py-1.5 text-xs text-destructive">
          <AlertCircle className="size-3.5 shrink-0" />
          <span className="truncate" data-testid="specs-error">
            {error}
          </span>
        </div>
      )}

      {status === null ? (
        <div className="p-3 text-xs text-muted-foreground">
          {isLoading ? 'Loading specs…' : 'No specs data.'}
        </div>
      ) : (
        <>
          {/* The header renders in BOTH branches: the badge stays visible
              (inert without a declared findings-fix action) even when the body
              swaps, so the affordance never teleports in and out. */}
          <SpecsHeader
            status={status}
            frameworks={status.detected}
            activeId={sddProjectId === projectId ? sddActiveId : (status.active_id ?? '')}
            onSelectFramework={onSelectFramework}
            switching={switching}
            fixAction={fixAction}
            onFixFindings={dispatchableAction(fixAction) ? onFixFindings : undefined}
            onRefresh={onRefresh}
            isLoading={isLoading}
          />
          {status.present ? (
            <>
              <SpecsQuickActions actions={status.actions} onDispatch={onDispatch} />
              {status.warnings.length > 0 && (
                <div
                  data-testid="specs-warnings"
                  title={status.warnings.join('\n')}
                  className="flex items-center gap-1.5 border-b border-border bg-secondary/30 px-2 py-1 text-xs text-muted-foreground"
                >
                  <Info className="size-3 shrink-0" />
                  <span className="min-w-0 truncate">{status.warnings[0]}</span>
                  {status.warnings.length > 1 && (
                    <span className="shrink-0">+{status.warnings.length - 1}</span>
                  )}
                </div>
              )}
              <div className="custom-scrollbar flex min-h-0 flex-1 flex-col gap-2 overflow-auto px-1.5 py-1.5">
                {status.tree_present && status.documents.length > 0 ? (
                  <PanelSections
                    status={status}
                    onOpen={openModelPath}
                    onDispatch={onDispatch}
                    expandedNodes={treeExpanded}
                    onToggleNode={toggleTreeNode}
                  />
                ) : (
                  // Two different empty states share one surface: the tree is
                  // ABSENT (no declared root on disk) or it is PRESENT but
                  // holds no documents yet. Both name the root and offer the
                  // bootstrap gesture; the sections would render nothing at
                  // all in the second case, leaving a healthy-looking panel
                  // with an empty body.
                  <SpecsEmptyState
                    status={status}
                    onDispatch={onDispatch}
                    reason={status.tree_present ? 'empty-tree' : 'absent'}
                  />
                )}
              </div>
            </>
          ) : (
            // Defensive: the tab strip hides the surface without a detected
            // framework, so this branch only renders for a stale payload.
            <div className="p-3 text-xs text-muted-foreground">
              No specification framework detected in this workspace.
            </div>
          )}
        </>
      )}

      {/* The preview gate (see pendingDispatch): the framework-declared prompt
          is shown verbatim before it enters the chat as the user's message.
          Radix closes on Esc and overlay through onOpenChange(false) — the
          same dismiss path as the Cancel button. */}
      <Dialog
        open={pendingDispatch !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDispatch(null)
        }}
      >
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle data-testid="specs-dispatch-title">
              Run “{pendingDispatch?.dispatch.skill ?? ''}” in the chat?
            </DialogTitle>
            <DialogDescription>
              This prompt is declared by the active framework&apos;s specification
              documents. Review it — sending places it into the chat as your
              message with the skill activated.
            </DialogDescription>
          </DialogHeader>
          <pre
            data-testid="specs-dispatch-prompt"
            className="custom-scrollbar max-h-48 overflow-auto whitespace-pre-wrap rounded bg-secondary px-2 py-1.5 text-left text-xs text-foreground"
          >
            {pendingDispatch?.dispatch.prompt ?? ''}
          </pre>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              data-testid="specs-dispatch-cancel"
              onClick={() => setPendingDispatch(null)}
            >
              Cancel
            </Button>
            <Button type="button" data-testid="specs-dispatch-confirm" onClick={confirmDispatch}>
              Send
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** The declared view sections, in declaration order: `tree` sections render
 *  the node forest, `findings`/`checks` sections the failed checks. A framework
 *  that declares NO views falls back to one implicit tree over the forest's
 *  roots plus a findings section — the model is always renderable, and a
 *  framework document that forgot its views still shows its tree.
 *
 *  The parse model does not carry a finding→section membership (a finding
 *  names a file, not the declared section that failed), so every non-tree
 *  section would render the SAME full list — a framework declaring both
 *  `findings` and `checks` showed one card twice under two headings. The
 *  list renders ONCE, under the FIRST declared non-tree section; any further
 *  non-tree section renders nothing (it has no list of its own to show, and
 *  an empty section renders no header at all). */
function PanelSections({
  status,
  onOpen,
  onDispatch,
  expandedNodes,
  onToggleNode,
}: {
  status: SpecsStatus
  onOpen: (modelPath: string) => void
  onDispatch: (pending: SpecsDispatch, newSession: boolean) => void
  expandedNodes?: Record<string, boolean>
  onToggleNode?: (id: string) => void
}) {
  const fixAction = findingsFixAction(status.actions)
  const firstFindingsView = status.views.findIndex((view) => view.kind !== 'tree')
  const sectionFindings = (view: SDDView): SDDFinding[] =>
    status.views.indexOf(view) === firstFindingsView ? status.findings : []
  if (status.views.length === 0) {
    const implicit: SDDView = {
      ...IMPLICIT_TREE_SECTION,
      nodes: status.nodes.filter((node) => (node.parent ?? '') === '').map((node) => node.id),
    }
    return (
      <>
        <SpecsSectionTree
          section={implicit}
          nodes={status.nodes}
          documents={status.documents}
          actions={status.actions}
          expandedNodes={expandedNodes}
          onToggleNode={onToggleNode}
          onOpenDocument={onOpen}
          onDispatch={onDispatch}
        />
        {status.findings.length > 0 && (
          <SpecsFindings
            section={{ ...implicit, id: 'findings', label: 'Findings', kind: 'findings' }}
            findings={status.findings}
            fixAction={fixAction}
            onOpen={onOpen}
            onDispatch={onDispatch}
          />
        )}
      </>
    )
  }
  return (
    <>
      {status.views.map((view) =>
        view.kind === 'tree' ? (
          <SpecsSectionTree
            key={view.id}
            section={view}
            nodes={status.nodes}
            documents={status.documents}
            actions={status.actions}
            expandedNodes={expandedNodes}
            onToggleNode={onToggleNode}
            memberPaths={view.paths !== undefined ? new Set(view.paths) : undefined}
            onOpenDocument={onOpen}
            onDispatch={onDispatch}
          />
        ) : (
          <SpecsFindings
            key={view.id}
            section={view}
            findings={sectionFindings(view)}
            fixAction={fixAction}
            onOpen={onOpen}
            onDispatch={onDispatch}
          />
        ),
      )}
    </>
  )
}
