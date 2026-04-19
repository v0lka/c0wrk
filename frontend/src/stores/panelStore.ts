import { create } from 'zustand'
import type { ChatMessageUI } from './chatStore'

// Types matching backend event data structures
export interface PlanItem {
  id: string        // step_id from backend (1-indexed string)
  title: string     // short display label (summary or description fallback)
  description?: string // full What-How-Where text for tooltips
  summary?: string  // short 5-7 word label for UI display
  status: 'pending' | 'running' | 'completed' | 'failed'
  duration?: number // milliseconds, from plan_step_complete
  dependsOn: string[] // DAG dependency IDs
}

export interface PlanGroup {
  id: number
  items: PlanItem[]
  // Backend-computed progress fields (when available)
  progress?: number          // 0.0–1.0
  completedCount?: number
  totalCount?: number
}

interface SessionStats {
  routingDomain: string
  routingComplexity: string
  attempt: number
  maxAttempts: number
}

const defaultStats: SessionStats = {
  routingDomain: '',
  routingComplexity: '',
  attempt: 1,
  maxAttempts: 3,
}

interface PanelState {
  planGroups: PlanGroup[]  // newest first
  sessionStats: SessionStats
  _planGroupCounter: number
  
  // Actions
  addPlanGroup: (steps: Array<{ description: string; status?: string }>, progress?: { progress?: number; completed_count?: number; total_count?: number }) => void
  updatePlanItemStatus: (stepId: string, status: PlanItem['status'], duration?: number) => void
  updateStats: (update: Partial<SessionStats>) => void
  resetPanels: () => void
  resetPlanStatuses: () => void
  rebuildFromEvents: (messages: ChatMessageUI[]) => void
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null
}
function isStepArray(v: unknown): v is Array<{ id?: string; description: string; status?: string }> {
  return Array.isArray(v) && v.every(s => isRecord(s) && 'description' in s)
}

const validPlanStatuses = ['pending', 'running', 'completed', 'failed'] as const
type ValidPlanStatus = typeof validPlanStatuses[number]
function toValidStatus(s: unknown): ValidPlanStatus {
  return typeof s === 'string' && (validPlanStatuses as readonly string[]).includes(s)
    ? (s as ValidPlanStatus) : 'pending'
}

export const usePanelStore = create<PanelState>((set) => ({
  planGroups: [],
  sessionStats: { ...defaultStats },
  _planGroupCounter: 0,

  addPlanGroup: (steps, progress) => {
    if (!steps) return  // Guard against null/undefined from backend nil slices
    set((state) => {
      const counter = state._planGroupCounter + 1
      const newGroup: PlanGroup = {
        id: counter,
        items: steps.map((s, i) => ({
          id: (isRecord(s) && typeof (s as Record<string, unknown>).id === 'string' ? (s as Record<string, unknown>).id as string : undefined) || String(i + 1),
          title: (isRecord(s) && typeof (s as Record<string, unknown>).summary === 'string' && ((s as Record<string, unknown>).summary as string).trim() ? (s as Record<string, unknown>).summary as string : undefined) || s.description,
          description: s.description,
          summary: isRecord(s) && typeof (s as Record<string, unknown>).summary === 'string' ? (s as Record<string, unknown>).summary as string : undefined,
          status: toValidStatus(s.status),
          dependsOn: isRecord(s) && Array.isArray((s as Record<string, unknown>).depends_on) ? (s as Record<string, unknown>).depends_on as string[] : [],
        })),
        progress: progress?.progress,
        completedCount: progress?.completed_count,
        totalCount: progress?.total_count,
      }
      return {
        _planGroupCounter: counter,
        planGroups: [newGroup],
      }
    })
  },

  updatePlanItemStatus: (stepId, status, duration) => {
    set((state) => {
      if (state.planGroups.length === 0) return state
      
      // Update the latest (first) plan group
      const latestGroup = state.planGroups[0]! // Safe: length > 0 checked above
      const rest = state.planGroups.slice(1)
      const updatedItems = latestGroup.items.map((item) =>
        item.id === stepId
          ? { ...item, status, ...(duration !== undefined ? { duration } : {}) }
          : item
      )

      // Recompute completedCount from items to keep group-level counter in sync
      const completedCount = updatedItems.filter(
        (item) => item.status === 'completed' || item.status === 'failed'
      ).length

      return {
        planGroups: [{
          ...latestGroup,
          items: updatedItems,
          completedCount,
        }, ...rest],
      }
    })
  },

  updateStats: (update) => set((s) => ({
    sessionStats: { ...s.sessionStats, ...update },
  })),

  resetPanels: () => {
    set({ planGroups: [], sessionStats: { ...defaultStats }, _planGroupCounter: 0 })
  },

  resetPlanStatuses: () => {
    set((state) => {
      if (state.planGroups.length === 0) return state
      const latest = state.planGroups[0]! // Safe: length > 0 checked above
      const rest = state.planGroups.slice(1)
      return {
        planGroups: [{
          ...latest,
          items: latest.items.map(item => ({ ...item, status: 'pending' as const, duration: undefined })),
        }, ...rest],
      }
    })
  },

  rebuildFromEvents: (messages) => {
    let planCounter = 0
    
    let planGroups: PlanGroup[] = []
    
    // Iterate chronologically to rebuild state
    for (const msg of messages) {
      switch (msg.type) {
        case 'plan': {
          const meta = isRecord(msg.metadata) ? msg.metadata : undefined
          const rawSteps = isStepArray(meta?.steps) ? meta.steps : []
          const group: PlanGroup = {
            id: ++planCounter,
            items: rawSteps.map((s, i) => ({
              id: s.id || String(i + 1),
              title: (isRecord(s) && typeof (s as Record<string, unknown>).summary === 'string' ? (s as Record<string, unknown>).summary as string : undefined) || s.description,
              description: s.description,
              summary: isRecord(s) && typeof (s as Record<string, unknown>).summary === 'string' ? (s as Record<string, unknown>).summary as string : undefined,
              status: toValidStatus(s.status),
              dependsOn: isRecord(s) && Array.isArray((s as Record<string, unknown>).depends_on) ? (s as Record<string, unknown>).depends_on as string[] : [],
            })),
          }
          planGroups = [group]
          break
        }

        case 'plan_step_start': {
          const meta = isRecord(msg.metadata) ? msg.metadata : undefined
          const stepId = typeof meta?.step_id === 'string' ? meta.step_id : undefined
          if (planGroups.length > 0 && stepId) {
            const latest = planGroups[planGroups.length - 1]! // Safe: length > 0 checked
            const updatedItems = latest.items.map((s) =>
              s.id === stepId ? { ...s, status: 'running' as const } : s
            )
            planGroups = planGroups.map((g, i) =>
              i === planGroups.length - 1 ? { ...latest, items: updatedItems } : g
            )
          }
          break
        }

        case 'plan_step_complete': {
          const meta = isRecord(msg.metadata) ? msg.metadata : undefined
          const stepId = typeof meta?.step_id === 'string' ? meta.step_id : undefined
          const success = typeof meta?.success === 'boolean' ? meta.success : undefined
          const duration = typeof meta?.duration === 'number' ? meta.duration : undefined
          if (planGroups.length > 0 && stepId) {
            const latest = planGroups[planGroups.length - 1]! // Safe: length > 0 checked
            const updatedItems = latest.items.map((s) =>
              s.id === stepId
                ? {
                    ...s,
                    status: (success ? 'completed' : 'failed') as PlanItem['status'],
                    ...(duration !== undefined ? { duration } : {}),
                  }
                : s
            )
            planGroups = planGroups.map((g, i) =>
              i === planGroups.length - 1 ? { ...latest, items: updatedItems } : g
            )
          }
          break
        }

        default:
          break
      }
    }
    
    // Store newest first
    set({
      planGroups: planGroups.reverse(),
      _planGroupCounter: planCounter,
    })
  },
}))

// Stable module-level selectors for computed values.
// These return primitive numbers so Zustand's default Object.is comparison
// is sufficient — no useShallow needed. Keeping selector references stable
// ensures useSyncExternalStore reliably detects store changes and triggers
// re-renders (fixes collapsed panel header counter not updating).

const selectPlanCompleted = (state: PanelState): number => {
  // Use backend-provided count from latest group if available
  const latestPlanForCompleted = state.planGroups[0]
  if (latestPlanForCompleted?.completedCount !== undefined) {
    return latestPlanForCompleted.completedCount
  }
  // Fallback: compute from items
  let count = 0
  for (const group of state.planGroups) {
    for (const item of group.items) {
      if (item.status === 'completed' || item.status === 'failed') {
        count++
      }
    }
  }
  return count
}

const selectPlanTotal = (state: PanelState): number => {
  // Use backend-provided count from latest group if available
  const latestPlanForTotal = state.planGroups[0]
  if (latestPlanForTotal?.totalCount !== undefined) {
    return latestPlanForTotal.totalCount
  }
  // Fallback: compute from items
  let count = 0
  for (const group of state.planGroups) {
    count += group.items.length
  }
  return count
}

export function usePlanCompleted(): number {
  return usePanelStore(selectPlanCompleted)
}

export function usePlanTotal(): number {
  return usePanelStore(selectPlanTotal)
}
