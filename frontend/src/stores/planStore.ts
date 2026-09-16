import { create } from 'zustand'
import type { PlanGroup, PlanItem } from '@/types/models'
import type { WorkUnitBlockStatus } from '@/types/messages'
import type { AgentMetricsData } from '@/types/events'

// --- State types ---

export interface SessionStats {
  routingDomain?: string
  routingComplexity?: number
  attemptCount?: number
  maxAttempts?: number
  /** Last per-run agent quality report (agent_metrics event on finish/abort). */
  lastAgentMetrics?: AgentMetricsData
}

interface PlanState {
  planGroups: PlanGroup[] // newest first
  sessionStats: Record<string, SessionStats> // sessionId -> stats
}

interface PlanActions {
  setPlan: (plan: PlanGroup) => void
  updateStepStatus: (stepId: string, status: PlanItem['status'], duration?: number) => void
  applyWorkUnitStatuses: (statuses: Record<string, WorkUnitBlockStatus>) => void
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
    if (item.status === 'completed') count++
  }
  return count
}

const selectPlanFailed = (state: PlanState): number => {
  const latest = state.planGroups[0]
  if (!latest) return 0
  if (latest.failedCount !== undefined) return latest.failedCount
  let count = 0
  for (const item of latest.items) {
    if (item.status === 'failed') count++
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

export function usePlanFailed(): number {
  return usePlanStore(selectPlanFailed)
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
    const completedCount = updatedItems.filter((item) => item.status === 'completed').length
    const failedCount = updatedItems.filter((item) => item.status === 'failed').length
    return {
      planGroups: [{ ...latest, items: updatedItems, completedCount, failedCount }, ...rest],
    }
  }),

  // Reconcile the replayed plan panel with the durable work-unit snapshot: a
  // step the panel still shows 'running' (only its plan_step_start was
  // persisted before the restart) whose unit the ledger settled is corrected to
  // the durable status — exactly as the chat overlay corrects its own block — so
  // the two views agree instead of the panel spinning a settled step forever.
  // A step with a message-derived terminal status is left alone (it was never
  // 'running'), so the snapshot can only ever finish a step, never regress it.
  applyWorkUnitStatuses: (statuses) => set((s) => {
    if (s.planGroups.length === 0) return s
    const latest = s.planGroups[0]!
    let changed = false
    const items = latest.items.map((item) => {
      if (item.status !== 'running') return item
      const snapshot = statuses[item.id]
      if (!snapshot || snapshot === 'running') return item
      changed = true
      return { ...item, status: snapshot }
    })
    if (!changed) return s
    return {
      planGroups: [{
        ...latest,
        items,
        completedCount: items.filter((item) => item.status === 'completed').length,
        failedCount: items.filter((item) => item.status === 'failed').length,
      }, ...s.planGroups.slice(1)],
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
