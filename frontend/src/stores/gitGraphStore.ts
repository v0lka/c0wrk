import { create } from 'zustand'

// App-lifetime expansion state of the file viewer's Git History page (the
// `c0wrk:graph` pseudo-tab), keyed per project.
//
// In-memory only — deliberately NO persist middleware: the state lives
// exactly for the application session. Closing and reopening the history
// tab, collapsing the viewer, switching projects (and back) or navigating
// sessions keeps the expanded card; an app restart starts collapsed.
//
// Keyed per project because the history RPCs act on the backend's active
// project: the page reads the entry of the project whose history it shows,
// so a card expanded in project A never leaks into project B's graph.
// Commit SHAs are content-addressed, which makes a persisted entry
// self-validating: it only affects rendering when that SHA is actually
// present in the loaded history (a rebased-away SHA degrades to "all
// collapsed" until the user expands something else). The payload caches
// (full message, files, diffs) stay page-local — only the expand/collapse
// DECISIONS persist here; remounting re-fetches the payloads through the
// page's deduped lazy loaders.

/** Expansion state of ONE project's history graph. */
export interface GitGraphExpansion {
  /** The single expanded commit card (null = all collapsed). */
  expandedSha: string | null
  /** Per-commit set of file paths whose inline diff is open. */
  expandedFiles: Record<string, ReadonlySet<string>>
}

interface GitGraphState {
  /** Expansion state per project id. */
  byProject: Record<string, GitGraphExpansion>
}

interface GitGraphActions {
  /** Expand one commit card (replacing any previously expanded one). */
  expandCard: (projectId: string, sha: string) => void
  /** Collapse the expanded card (keeps the per-commit file sets for a later re-expand). */
  collapseCard: (projectId: string) => void
  /** Toggle one changed-file's inline diff inside a commit card. */
  toggleFile: (projectId: string, sha: string, path: string) => void
}

export const useGitGraphStore = create<GitGraphState & GitGraphActions>()((set) => ({
  byProject: {},

  expandCard: (projectId, sha) =>
    set((s) => {
      // No active project — nothing to scope the decision to; drop it.
      if (!projectId) return s
      const prev = s.byProject[projectId]
      return {
        byProject: {
          ...s.byProject,
          [projectId]: { expandedSha: sha, expandedFiles: prev?.expandedFiles ?? {} },
        },
      }
    }),

  collapseCard: (projectId) =>
    set((s) => {
      if (!projectId) return s
      const prev = s.byProject[projectId]
      if (!prev || prev.expandedSha === null) return s
      return {
        byProject: {
          ...s.byProject,
          [projectId]: { expandedSha: null, expandedFiles: prev.expandedFiles },
        },
      }
    }),

  toggleFile: (projectId, sha, path) =>
    set((s) => {
      if (!projectId) return s
      const prev = s.byProject[projectId]
      const current = prev?.expandedFiles[sha] ?? new Set<string>()
      const next = new Set(current)
      if (next.has(path)) {
        next.delete(path)
      } else {
        next.add(path)
      }
      return {
        byProject: {
          ...s.byProject,
          [projectId]: {
            expandedSha: prev?.expandedSha ?? null,
            expandedFiles: { ...prev?.expandedFiles, [sha]: next },
          },
        },
      }
    }),
}))
