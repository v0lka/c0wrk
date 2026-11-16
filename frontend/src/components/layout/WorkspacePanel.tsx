import { useCallback, useEffect, useRef } from "react";
import { subscribe } from "@/api/runtime";
import { TooltipProvider } from "@/components/ui/tooltip";
import { SegmentedControl, type SegmentedControlItem } from "@/components/ui/segmented-control";
import { FileTreePanel } from "./FileTreePanel";
import { VectorStorePanel } from "./VectorStorePanel";
import { GitPanel } from "@/components/GitPanel";
import { BranchPicker } from "@/components/GitPanel/BranchPicker";
import { ResearchPanel } from "@/components/research";
import { SpecsPanel } from "@/components/specs";
import { useProjectStore, selectIsNoProject } from "@/stores/projectStore";
import { useSessionStore } from "@/stores/sessionStore";
import { useSddStore } from "@/stores/sddStore";
import { useSpecsStore } from "@/stores/specsStore";
import { useGitPanelStore } from "@/stores/gitPanelStore";
import { useUIStore, selectWorkspaceTab, type WorkspaceTab } from "@/stores/uiStore";
import { useProjectGitRepo } from "@/hooks/useProjectGitRepo";
import { useIndexingSettings } from "@/hooks/useIndexingSettings";
import { useSidebarPacks } from "@/hooks/useSidebarPacks";
import { focusFileExplorer } from "@/lib/workspaceLayout";
import { FolderTree, GitBranch, Search, FlaskConical, BookOpen } from "lucide-react";

/** Stable ids wiring each tab to its panel (the ARIA tabs pattern, same
 *  shape as the Research panel's segment wiring). */
function workspaceTabId(tab: WorkspaceTab): string {
  return `workspace-tab-${tab}`;
}
function workspacePanelId(tab: WorkspaceTab): string {
  return `workspace-panel-${tab}`;
}

/** Trailing-debounce window for the workspace-level detection re-probe. The
 *  probe is a full backend parse RPC, and `workspace:tree_changed` fires per
 *  file an agent writes (plus checkouts, re-indexing, watcher bursts) —
 *  without coalescing every event re-walks the tree while the projection is
 *  negative. Detection invalidation is a visibility gate (a tab appearing),
 *  not live state, so a sub-second trailing window keeps discovery prompt
 *  while collapsing each burst into ONE probe. Virtual-time tests advance by
 *  exactly this constant. */
export const SPECS_DETECTION_REPROBE_DEBOUNCE_MS = 500;

/** itemProps passed to every SegmentedControl item: the shared control
 *  cannot know panel ids, so each tab carries its own `id` + `aria-controls`
 *  onto the rendered tab button. */
function tabItemProps(tab: WorkspaceTab) {
  return {
    id: workspaceTabId(tab),
    "aria-controls": workspacePanelId(tab),
  };
}

export function WorkspacePanel() {
  const isNoProject = useProjectStore(selectIsNoProject);
  const activeProjectId = useProjectStore((s) => s.activeProjectId);
  // Indexing master switch (config `vector_index.enabled`): the Search
  // (semantics) tab is the vector-index surface and hides from BOTH tab
  // layouts while indexing is off — there is no index to search. The hook
  // must run before the CHAT early-return below (hooks are unconditional).
  const indexingEnabled = useIndexingSettings();
  // Sidebar surface gates (config `research.enabled` / `specs.enabled`): the
  // Research and Specs tabs hide from the tab strip while their gate is off.
  // Only the Research gate carries a pack half — its toggle unseeds the
  // research skill/agent packs. The Specs gate is pure visibility: c0wrk ships
  // no spec pack, so `SetSpecsEnabled` is a config save plus an event
  // (ADR-115, as amended by ADR-126).
  const { research: researchEnabled, specs: specsEnabled } = useSidebarPacks();
  // Detection decides the Specs tab as much as the gate does: a workspace with
  // no recognised SDD framework has no surface to show (zero detected
  // frameworks = no tab). The answer must exist BEFORE the panel mounts — a
  // hidden tab never mounts it — so the projection comes from sddStore, which
  // specsStore feeds with every parse. Until this project's first answer lands
  // the tab stays visible: the panel's own parse is what discovers detection,
  // and hiding it would make detection unreachable. Primitive selectors only.
  const sddProjectId = useSddStore((s) => s.projectId);
  const sddPresent = useSddStore((s) => s.present);
  const sddKnown = useSddStore((s) => s.known);
  const specsDetected =
    sddProjectId === activeProjectId && sddKnown ? sddPresent : true;
  const specsVisible = specsEnabled && specsDetected;
  // Each project remembers its own workspace tab. Deriving (rather than
  // storing a scalar) means a project switch instantly yields the incoming
  // project's tab without any transient wrong-layout frame.
  const workspaceTab = useUIStore((s) => selectWorkspaceTab(s, activeProjectId));
  const setWorkspaceTab = useUIStore((s) => s.setWorkspaceTab);

  // One detection probe per project switch, through the SAME single RPC the
  // panel uses (no second source of truth, no extra parse when this project
  // already has one). Without it a workspace whose only framework is detected
  // through files the tab strip cannot see would never show its tab.
  useEffect(() => {
    if (activeProjectId === null || !specsEnabled) return;
    const specs = useSpecsStore.getState();
    // A fetch for THIS project is already in flight or done (the panel mounts
    // as a child, so its own effect runs first) — never parse twice. This
    // stamp is the only skip: it is written at fetch START, so it always
    // names the newest flight — the sdd projection lands only with a
    // response and would skip a probe on a stale-positive match, letting a
    // superseded cross-project response land and strand a phantom Specs tab.
    if (specs.projectId === activeProjectId) return;
    void specs.fetchStatus(activeProjectId);
  }, [activeProjectId, specsEnabled]);

  // Detection invalidation at a PERMANENTLY MOUNTED level. The panel's own
  // tree_changed subscription dies with the tab — and the Specs tab does not
  // even EXIST while the projection says `present=false` — so a framework
  // installed into a workspace AFTER a negative answer would never be
  // discovered: the negative result was stale forever. The mirror case is the
  // POSITIVE projection: on any other workspace tab (Git, Explorer, Search,
  // Research) the panel is unmounted with its subscription, so a removed
  // `specs/` directory or a removed skill would leave a dead Specs tab that
  // only exists to refuse itself when clicked. This workspace panel is always
  // mounted, so IT refreshes BOTH projections on a tree or skill-set change
  // through the same single fetchStatus funnel; the LWW ticket absorbs the
  // overlap with the panel's own refresh and the debounce keeps it to one
  // probe per burst. `unknown` (no answer yet) stays first-discovery
  // territory.
  const refreshSpecsDetection = useCallback(() => {
    if (activeProjectId === null || !specsEnabled) return;
    const sdd = useSddStore.getState();
    if (sdd.projectId !== activeProjectId || !sdd.known) return;
    if (useSpecsStore.getState().isLoading) return;
    void useSpecsStore.getState().fetchStatus(activeProjectId);
  }, [activeProjectId, specsEnabled]);

  // Both invalidation events share ONE trailing debounce so a write burst
  // collapses into a single probe (see SPECS_DETECTION_REPROBE_DEBOUNCE_MS);
  // the guards inside refreshSpecsDetection re-run at FIRE time, so a burst
  // that ends after a project switch schedules nothing stale, and a probe
  // already in flight is not doubled. The pending timer dies with the effect
  // (unmount and every activeProjectId change), never leaking across projects.
  const reprobeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    const scheduleReprobe = () => {
      if (reprobeTimerRef.current !== null) clearTimeout(reprobeTimerRef.current);
      reprobeTimerRef.current = setTimeout(() => {
        reprobeTimerRef.current = null;
        refreshSpecsDetection();
      }, SPECS_DETECTION_REPROBE_DEBOUNCE_MS);
    };
    const unsubTree = subscribe("workspace:tree_changed", scheduleReprobe);
    const unsubSkills = subscribe("skills:changed", scheduleReprobe);
    return () => {
      if (reprobeTimerRef.current !== null) {
        clearTimeout(reprobeTimerRef.current);
        reprobeTimerRef.current = null;
      }
      unsubTree();
      unsubSkills();
    };
  }, [refreshSpecsDetection]);
  // A session switch can move the parse to a different workspace tree (the
  // backend scans the ACTIVE session's workspace) with no tree_changed
  // event of its own — re-check a negative projection on session change too.
  const activeSessionId = useSessionStore((s) => s.activeSessionId);
  useEffect(() => {
    refreshSpecsDetection();
  }, [activeSessionId, refreshSpecsDetection]);

  // Eager per-project git-repo detection. Mounted before the CHAT
  // early-return because hooks cannot be conditional; the hook itself skips
  // the RPC for No Project, where the Git panel does not exist anyway.
  useProjectGitRepo();

  // The check result is only valid for the project it was checked against:
  // pairing the two fields prevents a stale answer from a previously active
  // project leaking into the layout during rapid project switches. While no
  // completed check belongs to this project the selector yields false, so
  // the non-git layout (the pre-feature default) renders until it lands.
  const isGitRepo = useGitPanelStore(
    (s) => s.isGitRepo && s.gitRepoProjectId === activeProjectId,
  );

  // Whether a completed repo check actually belongs to the CURRENTLY active
  // project. While false, `isGitRepo` above means "not yet known" rather
  // than a real "not a repository" answer.
  const repoKnown = useGitPanelStore(
    (s) => s.gitRepoProjectId === activeProjectId,
  );

  // Keep the per-project workspaceTab valid for the current layout: the
  // Explorer tab exists only for non-git projects (a git project hosts the
  // explorer inside the Git panel's "files" section), the Git tab only
  // for git projects, and the Search (semantics) tab only while indexing
  // is enabled. A remembered research/specs tab falls back the same way
  // while its sidebar surface gate is off. Runs on layout flips, project
  // switches and gating toggles — but ONLY once the active project's repo
  // check has landed.
  // Acting on the fail-closed `isGitRepo === false` while the check is
  // still pending would clobber a freshly switched project's remembered
  // tab (e.g. a git project whose memory is 'git' would be reset to
  // 'explorer' before its own check confirms the repo). The inline
  // effectiveTab remap below covers the pending window instead.
  useEffect(() => {
    if (!repoKnown || activeProjectId === null) return;
    if (isGitRepo && workspaceTab === "explorer") {
      focusFileExplorer();
    } else if (!isGitRepo && workspaceTab === "git") {
      setWorkspaceTab(activeProjectId, "explorer");
    } else if (!indexingEnabled && workspaceTab === "semantics") {
      // Indexing off: the Search tab no longer exists — fall back to the
      // layout's primary tab (Git for a repo, Explorer otherwise).
      setWorkspaceTab(activeProjectId, isGitRepo ? "git" : "explorer");
    } else if (!researchEnabled && workspaceTab === "research") {
      // Research surface off: the tab no longer exists — the same primary-
      // tab fallback (the backend unseeded the surface's pack in the same
      // toggle).
      setWorkspaceTab(activeProjectId, isGitRepo ? "git" : "explorer");
    } else if (!specsVisible && workspaceTab === "specs") {
      // Specs surface off (gate off, or no SDD framework detected): the same
      // fallback.
      setWorkspaceTab(activeProjectId, isGitRepo ? "git" : "explorer");
    }
  }, [repoKnown, isGitRepo, workspaceTab, activeProjectId, setWorkspaceTab, indexingEnabled, researchEnabled, specsVisible]);

  // In CHAT (No Project) mode, hide the tab strip entirely — only show the file
  // explorer with file-name search. Git and Semantics are unavailable anyway.
  if (isNoProject) {
    return (
      <TooltipProvider>
        <div className="flex h-full flex-col overflow-hidden">
          <FileTreePanel />
        </div>
      </TooltipProvider>
    );
  }

  // The tab actually rendered this frame. Until the effect above corrects
  // the store, a freshly switched project may still hold a tab value with
  // no trigger in the new layout (e.g. 'explorer' in a git project) — Radix
  // would render no content for it, so remap it inline for this render.
  // The same inline fallback covers a remembered 'semantics' while indexing
  // is off (the effect persists the corrected value once the repo check has
  // landed).
  let effectiveTab = isGitRepo
    ? workspaceTab === "explorer"
      ? "git"
      : workspaceTab
    : workspaceTab === "git"
      ? "explorer"
      : workspaceTab;
  if (!indexingEnabled && effectiveTab === "semantics") {
    effectiveTab = isGitRepo ? "git" : "explorer";
  }
  // The same inline fallback covers a remembered research/specs tab while
  // its surface gate is off (the effect persists the corrected value).
  if (!researchEnabled && effectiveTab === "research") {
    effectiveTab = isGitRepo ? "git" : "explorer";
  }
  if (!specsVisible && effectiveTab === "specs") {
    effectiveTab = isGitRepo ? "git" : "explorer";
  }

  // The Search (semantics) tab is the vector-index surface: it renders only
  // while the indexing master switch is on, in BOTH layouts (git and
  // non-git). With indexing off there is no index to search, so the entry
  // point hides — exactly like the status-bar indexing indicator and the
  // viewer's "Find similar" action.
  const tabs: Array<SegmentedControlItem<WorkspaceTab>> = [
    isGitRepo
      ? {
          value: "git",
          icon: <GitBranch className="size-4" />,
          label: "Git",
          itemProps: tabItemProps("git"),
        }
      : {
          value: "explorer",
          icon: <FolderTree className="size-4" />,
          label: "Explorer",
          itemProps: tabItemProps("explorer"),
        },
    ...(indexingEnabled
      ? [
          {
            value: "semantics" as const,
            icon: <Search className="size-4" />,
            label: "Search",
            itemProps: tabItemProps("semantics"),
          },
        ]
      : []),
    ...(researchEnabled
      ? [
          {
            value: "research" as const,
            icon: <FlaskConical className="size-4" />,
            label: "Research",
            itemProps: tabItemProps("research"),
          },
        ]
      : []),
    ...(specsVisible
      ? [
          {
            value: "specs" as const,
            icon: <BookOpen className="size-4" />,
            label: "Specs",
            itemProps: tabItemProps("specs"),
          },
        ]
      : []),
  ];

  return (
    <TooltipProvider>
      <div className="flex h-full flex-col gap-0">
        <div className="@container shrink-0 pb-1 border-b border-border bg-secondary/20">
        <SegmentedControl
          items={tabs}
          value={effectiveTab}
          onValueChange={(v) => {
            if (activeProjectId !== null) {
              setWorkspaceTab(activeProjectId, v as WorkspaceTab);
            }
          }}
          fullWidth
          size="md"
          ariaLabel="Workspace view"
          labelClassName="hidden @min-[280px]:inline"
          className="mx-1"
        />
        </div>

        {isGitRepo ? (
          effectiveTab === 'git' && (
            <div
              role="tabpanel"
              id={workspacePanelId('git')}
              aria-labelledby={workspaceTabId('git')}
              className="flex-1 min-h-0 overflow-hidden"
            >
              <GitPanel />
            </div>
          )
        ) : (
          effectiveTab === 'explorer' && (
            <div
              role="tabpanel"
              id={workspacePanelId('explorer')}
              aria-labelledby={workspaceTabId('explorer')}
              className="flex-1 min-h-0 flex flex-col overflow-hidden"
            >
              <FileTreePanel />
            </div>
          )
        )}

        {effectiveTab === 'semantics' && (
          <div
            role="tabpanel"
            id={workspacePanelId('semantics')}
            aria-labelledby={workspaceTabId('semantics')}
            className="flex-1 min-h-0 flex flex-col overflow-hidden"
          >
            <VectorStorePanel />
          </div>
        )}

        {effectiveTab === 'research' && (
          <div
            role="tabpanel"
            id={workspacePanelId('research')}
            aria-labelledby={workspaceTabId('research')}
            className="flex-1 min-h-0 overflow-hidden"
          >
            <ResearchPanel />
          </div>
        )}

        {effectiveTab === 'specs' && (
          <div
            role="tabpanel"
            id={workspacePanelId('specs')}
            aria-labelledby={workspaceTabId('specs')}
            className="flex-1 min-h-0 overflow-hidden"
          >
            <SpecsPanel />
          </div>
        )}

        {/* The branch picker dialog is mounted at this level — not inside
            GitPanel — so the chat toolbar's session-draft `branch…` entry can
            open it regardless of which workspace tab is active. It renders
            nothing while closed (gitPanelStore.isBranchPickerOpen gates the
            Dialog). */}
        <BranchPicker />
      </div>
    </TooltipProvider>
  );
}
