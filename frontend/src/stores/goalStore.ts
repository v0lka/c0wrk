import { create } from 'zustand'
import type { GoalProposalData } from '@/types/events'

// --- Per-session goal state shapes ---

/**
 * The active (committed) goal for a session — the result of a user-approved
 * proposal plus the latest status/progress snapshot. Kept as a single stored
 * object so selectors can return the direct store reference (React 19
 * useSyncExternalStore stability contract — AGENTS.md §2.7).
 */
export interface ActiveGoal {
  /** The user-facing success condition (possibly user-edited on confirm). */
  condition: string
  /** How the goal is verified as met. */
  verify?: string
  /** Current lifecycle status (e.g. active, paused, met, exhausted, blocked_idle). */
  status: string
  /** Completed goal-loop turns so far. */
  turn: number
  /** Optional turn budget cap (0 = unlimited). */
  maxTurns?: number
  /** Agent's last self-declared verdict status, if any. */
  verdict?: string
  /** Agent's last verdict reason, if any. */
  reason?: string
}

/** Live mid-loop progress telemetry for a session's goal (goal_progress). */
export interface GoalProgress {
  turn: number
  maxTurns?: number
  condition: string
}

// --- State types ---

interface GoalState {
  /** sessionId -> the active/committed goal (latest snapshot). */
  activeGoal: Record<string, ActiveGoal>
  /** sessionId -> current goal status string. */
  goalStatus: Record<string, string>
  /** sessionId -> a pending proposal awaiting user sign-off. */
  pendingProposal: Record<string, GoalProposalData>
  /** sessionId -> latest mid-loop progress telemetry. */
  goalProgress: Record<string, GoalProgress>
}

interface GoalActions {
  setActiveGoal: (sessionId: string, goal: ActiveGoal) => void
  setGoalStatus: (sessionId: string, status: string) => void
  setGoalProgress: (sessionId: string, progress: GoalProgress) => void
  setPendingProposal: (sessionId: string, proposal: GoalProposalData) => void
  clearPendingProposal: (sessionId: string) => void
  /** Clear all goal state for a session (called on ClearGoal RPC). */
  clearGoal: (sessionId: string) => void
  /** Clear every session's goal state. */
  clearAll: () => void
}

// --- Stable selectors (hooks) ---
//
// Each selector returns a PRIMITIVE or a DIRECT store reference (an existing
// entry object or undefined) — never a freshly allocated array/object. This
// honors the React 19 useSyncExternalStore stability contract: a new array or
// object on every selector call causes an infinite re-render loop (#185).
// See AGENTS.md §2.7. Returning `state.map[sessionId]` is safe because it is a
// direct property access whose reference only changes when that entry is
// replaced.

export function useActiveGoal(sessionId: string | null): ActiveGoal | undefined {
  return useGoalStore((s) => (sessionId ? s.activeGoal[sessionId] : undefined))
}

export function useGoalStatus(sessionId: string | null): string | undefined {
  return useGoalStore((s) => (sessionId ? s.goalStatus[sessionId] : undefined))
}

export function usePendingGoalProposal(sessionId: string | null): GoalProposalData | undefined {
  return useGoalStore((s) => (sessionId ? s.pendingProposal[sessionId] : undefined))
}

export function useGoalProgress(sessionId: string | null): GoalProgress | undefined {
  return useGoalStore((s) => (sessionId ? s.goalProgress[sessionId] : undefined))
}

// --- Store ---

export const useGoalStore = create<GoalState & GoalActions>((set) => ({
  activeGoal: {},
  goalStatus: {},
  pendingProposal: {},
  goalProgress: {},

  setActiveGoal: (sessionId, goal) =>
    set((s) => ({
      activeGoal: { ...s.activeGoal, [sessionId]: goal },
      // Keep the status map in sync with the latest status carried by the goal
      // snapshot so the two never drift.
      goalStatus: { ...s.goalStatus, [sessionId]: goal.status },
    })),

  setGoalStatus: (sessionId, status) =>
    set((s) => ({
      goalStatus: { ...s.goalStatus, [sessionId]: status },
      activeGoal: s.activeGoal[sessionId]
        ? { ...s.activeGoal, [sessionId]: { ...s.activeGoal[sessionId]!, status } }
        : s.activeGoal,
    })),

  setGoalProgress: (sessionId, progress) =>
    set((s) => ({
      goalProgress: { ...s.goalProgress, [sessionId]: progress },
      activeGoal: s.activeGoal[sessionId]
        ? {
            ...s.activeGoal,
            [sessionId]: {
              ...s.activeGoal[sessionId]!,
              turn: progress.turn,
              maxTurns: progress.maxTurns,
              condition: progress.condition,
            },
          }
        : s.activeGoal,
    })),

  setPendingProposal: (sessionId, proposal) =>
    set((s) => ({
      pendingProposal: { ...s.pendingProposal, [sessionId]: proposal },
    })),

  clearPendingProposal: (sessionId) =>
    set((s) => {
      if (!(sessionId in s.pendingProposal)) return s
      const next = { ...s.pendingProposal }
      delete next[sessionId]
      return { pendingProposal: next }
    }),

  clearGoal: (sessionId) =>
    set((s) => {
      const activeGoal = { ...s.activeGoal }
      const goalStatus = { ...s.goalStatus }
      const pendingProposal = { ...s.pendingProposal }
      const goalProgress = { ...s.goalProgress }
      delete activeGoal[sessionId]
      delete goalStatus[sessionId]
      delete pendingProposal[sessionId]
      delete goalProgress[sessionId]
      return { activeGoal, goalStatus, pendingProposal, goalProgress }
    }),

  clearAll: () =>
    set({ activeGoal: {}, goalStatus: {}, pendingProposal: {}, goalProgress: {} }),
}))
