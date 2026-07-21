import { create } from 'zustand'
import type { ProjectInfo } from '@/types/models'

// --- Helpers ---

function sortByActivity(projects: ProjectInfo[]): ProjectInfo[] {
  return [...projects].sort((a, b) => {
    // No Project always first
    if (a.is_no_project && !b.is_no_project) return -1
    if (!a.is_no_project && b.is_no_project) return 1
    const aTime = new Date(a.last_active_at || a.created_at).getTime()
    const bTime = new Date(b.last_active_at || b.created_at).getTime()
    return bTime - aTime
  })
}

// --- State types ---

interface ProjectState {
  projects: ProjectInfo[] | null // null = not yet loaded
  activeProjectId: string | null
  lastRealProjectId: string | null // last active non-No-Project project (for CODE toggle)
  createDialogOpen: boolean // Create Project dialog visibility (opened on startup with no real projects / via CODE toggle)
}

interface ProjectActions {
  setProjects: (projects: ProjectInfo[]) => void
  setActiveProjectId: (id: string | null) => void
  addProject: (project: ProjectInfo) => void
  removeProject: (id: string) => void
  updateProject: (id: string, updates: Partial<ProjectInfo>) => void
  setCreateProjectDialogOpen: (open: boolean) => void
}

// --- Store ---

export const useProjectStore = create<ProjectState & ProjectActions>((set) => ({
  projects: null,
  activeProjectId: null,
  lastRealProjectId: null,
  createDialogOpen: false,

  setProjects: (projects) => set((s) => {
    const sorted = sortByActivity(projects)
    if (s.projects && s.projects.length === sorted.length &&
        s.projects.every((p, i) => p.id === sorted[i]?.id)) {
      return s
    }
    return { projects: sorted }
  }),

  setActiveProjectId: (id) => set((s) => {
    if (s.activeProjectId === id) return s
    const target = id ? s.projects?.find((p) => p.id === id) : null
    return {
      activeProjectId: id,
      lastRealProjectId: target && !target.is_no_project ? id : s.lastRealProjectId,
    }
  }),

  addProject: (project) => set((s) => {
    const existing = (s.projects ?? []).find((p) => p.id === project.id)
    if (existing) return s
    return { projects: sortByActivity([project, ...(s.projects ?? [])]) }
  }),

  removeProject: (id) => set((s) => ({
    projects: (s.projects ?? []).filter(p => p.id !== id),
    activeProjectId: s.activeProjectId === id ? null : s.activeProjectId,
  })),

  updateProject: (id, updates) => set((s) => ({
    projects: sortByActivity(
      (s.projects ?? []).map(p => p.id === id ? { ...p, ...updates } : p)
    ),
  })),

  setCreateProjectDialogOpen: (open) => set({ createDialogOpen: open }),
}))
