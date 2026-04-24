import { create } from 'zustand'
import type { ProjectInfo } from '@/types/models'

// --- Helpers ---

function sortByActivity(projects: ProjectInfo[]): ProjectInfo[] {
  return [...projects].sort((a, b) => {
    const aTime = new Date(a.last_active_at || a.created_at).getTime()
    const bTime = new Date(b.last_active_at || b.created_at).getTime()
    return bTime - aTime
  })
}

// --- State types ---

interface ProjectState {
  projects: ProjectInfo[] | null // null = not yet loaded
  activeProjectId: string | null
}

interface ProjectActions {
  setProjects: (projects: ProjectInfo[]) => void
  setActiveProjectId: (id: string | null) => void
  addProject: (project: ProjectInfo) => void
  removeProject: (id: string) => void
  updateProject: (id: string, updates: Partial<ProjectInfo>) => void
}

// --- Store ---

export const useProjectStore = create<ProjectState & ProjectActions>((set) => ({
  projects: null,
  activeProjectId: null,

  setProjects: (projects) => set((s) => {
    const sorted = sortByActivity(projects)
    if (s.projects && s.projects.length === sorted.length &&
        s.projects.every((p, i) => p.id === sorted[i]?.id)) {
      return s
    }
    return { projects: sorted }
  }),

  setActiveProjectId: (id) => set((s) =>
    s.activeProjectId === id ? s : { activeProjectId: id }
  ),

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
}))
