import { create } from 'zustand'
import type { PlanGroup, PlanItem } from '@/types/models'

// --- State types ---

export interface SessionStats {
  routingDomain?: string
  routingComplexity?: number
  attemptCount?: number
  maxAttempts?: number
}

interface PlanState {
  planGroups: PlanGroup[] // newest first
  sessionStats: Record<string, SessionStats> // sessionId -> stats
}

interface PlanActions {
  setPlan: (plan: PlanGroup) => void
  updateStepStatus: (stepId: string, status: PlanItem['status'], duration?: number) => void
  setSessionStats: (sessionId: string, stats: Partial<SessionStats>) => void
  clearPlan: () => void
  clearAll: () => void
}

// --- Selectors ---

const selectPlanCompleted = (state: PlanState): number => {
  const latest = state.planGroups[0]
  if (!latest) return 0
  if (latest.completedCount !== undefined) return latest.completedCount
  let count = 0
  for (const item of latest.items) {
    if (item.status === 'completed' || item.status === 'failed') count++
  }
  return count
}

const selectPlanTotal = (state: PlanState): number => {
  const latest = state.planGroups[0]
  if (!latest) return 0
  if (latest.totalCount !== undefined) return latest.totalCount
  return latest.items.length
}

export function usePlanCompleted(): number {
  return usePlanStore(selectPlanCompleted)
}

export function usePlanTotal(): number {
  return usePlanStore(selectPlanTotal)
}

// --- Store ---

export const usePlanStore = create<PlanState & PlanActions>((set) => ({
  planGroups: [],
  sessionStats: {},

  setPlan: (plan) => set({ planGroups: [plan] }),

  updateStepStatus: (stepId, status, duration) => set((s) => {
    if (s.planGroups.length === 0) return s
    const latest = s.planGroups[0]!
    const rest = s.planGroups.slice(1)
    const updatedItems = latest.items.map((item) =>
      item.id === stepId
        ? { ...item, status, ...(duration !== undefined ? { duration } : {}) }
        : item
    )
    const completedCount = updatedItems.filter(
      (item) => item.status === 'completed' || item.status === 'failed'
    ).length
    return {
      planGroups: [{ ...latest, items: updatedItems, completedCount }, ...rest],
    }
  }),

  setSessionStats: (sessionId, stats) => set((s) => ({
    sessionStats: {
      ...s.sessionStats,
      [sessionId]: { ...s.sessionStats[sessionId], ...stats },
    },
  })),

  clearPlan: () => set({ planGroups: [] }),

  clearAll: () => set({ planGroups: [], sessionStats: {} }),
}))
