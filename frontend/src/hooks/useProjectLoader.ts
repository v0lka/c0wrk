// Project loader — handles initial project loading and project lifecycle events.

import { useEffect } from 'react'
import { subscribe } from '@/api/runtime'
import { listProjects, switchProject } from '@/api/projects'
import { useProjectStore } from '@/stores/projectStore'
import type { ProjectInfo } from '@/types/models'

export function useProjectLoader(): void {
  useEffect(() => {
    let cancelled = false
    const cleanups: Array<() => void> = []
    const store = () => useProjectStore.getState()

    /** Fetch projects and auto-select the first one if none is active. */
    function loadAndActivate(): void {
      listProjects()
        .then((projects) => {
          if (cancelled) return
          store().setProjects(projects)
          if (!store().activeProjectId && projects.length > 0) {
            const firstId = projects[0]!.id
            switchProject(firstId)
              .then(() => { if (!cancelled) store().setActiveProjectId(firstId) })
              .catch(() => {})
          }
        })
        .catch(() => { /* will retry on backend:ready */ })
    }

    // Subscribe to backend:ready (authoritative signal — backend is initialized)
    cleanups.push(subscribe('backend:ready', loadAndActivate))

    // Project lifecycle events
    cleanups.push(
      subscribe('project:created', (data: unknown) => {
        if (cancelled) return
        const project = data as ProjectInfo | undefined
        if (project?.id) store().addProject(project)
      }),
    )

    cleanups.push(
      subscribe('project:deleted', (data: unknown) => {
        if (cancelled) return
        const id = data as string | undefined
        if (id) store().removeProject(id)
      }),
    )

    cleanups.push(
      subscribe('project:renamed', (data: unknown) => {
        if (cancelled) return
        const d = data as { id?: string; name?: string } | undefined
        if (d?.id && d.name) store().updateProject(d.id, { name: d.name })
      }),
    )

    cleanups.push(
      subscribe('project:switched', (data: unknown) => {
        if (cancelled) return
        const p = data as ProjectInfo | undefined
        if (p?.id) store().setActiveProjectId(p.id)
      }),
    )

    // Attempt initial load immediately (backend may already be ready)
    loadAndActivate()

    return () => {
      cancelled = true
      cleanups.forEach(fn => fn())
    }
  }, [])
}
