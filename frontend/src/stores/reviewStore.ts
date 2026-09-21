import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import * as reviewApi from '@/api/review'
import { logger } from '@/lib/logger'

export type ReviewStatus = 'active' | 'submitted' | 'approved'

export interface SessionReviewState {
  status: ReviewStatus
  generalComment: string
  hunkComments: Record<string, string> // key: `${filePath}::${hunkId}`
  fileComments: Record<string, string> // key: filePath
  loaded: boolean
}

export const REVIEW_TAB_PATH = 'c0wrk:review'

export function hunkCommentKey(filePath: string, hunkId: string): string {
  return `${filePath}::${hunkId}`
}

function emptySessionState(): SessionReviewState {
  return { status: 'active', generalComment: '', hunkComments: {}, fileComments: {}, loaded: false }
}

interface ReviewState {
  bySession: Record<string, SessionReviewState>
  reviewPageOpen: boolean
  activeReviewSession: string | null
  reviewLoopActive: Record<string, boolean>
  diffViewMode: 'unified' | 'split'
}

interface ReviewActions {
  loadReview: (sessionId: string) => Promise<void>
  setGeneralComment: (sessionId: string, text: string) => void
  setHunkComment: (sessionId: string, filePath: string, hunkId: string, text: string) => void
  setFileComment: (sessionId: string, filePath: string, text: string) => void
  removeHunkComment: (sessionId: string, filePath: string, hunkId: string) => void
  setStatus: (sessionId: string, status: ReviewStatus) => void
  openReviewPage: (sessionId: string) => void
  closeReviewPage: () => void
  enterReviewLoop: (sessionId: string) => void
  exitReviewLoop: (sessionId: string) => void
  clearSessionReview: (sessionId: string) => void
  setDiffViewMode: (mode: 'unified' | 'split') => void
}

function ensureSession(state: ReviewState, sessionId: string): SessionReviewState {
  return state.bySession[sessionId] ?? emptySessionState()
}

export const useReviewStore = create<ReviewState & ReviewActions>()(
  persist(
    (set) => ({
      bySession: {},
      reviewPageOpen: false,
      activeReviewSession: null,
      reviewLoopActive: {},
      diffViewMode: 'unified',

      loadReview: async (sessionId) => {
        try {
          const data = await reviewApi.getReview(sessionId)
          const hunkComments: Record<string, string> = {}
          for (const hc of data.hunk_comments) {
            hunkComments[hunkCommentKey(hc.file_path, hc.hunk_id)] = hc.body
          }
          const fileComments: Record<string, string> = {}
          for (const fc of data.file_comments) {
            fileComments[fc.file_path] = fc.body
          }
          set((s) => ({
            bySession: {
              ...s.bySession,
              [sessionId]: {
                status: (data.status as ReviewStatus) || 'active',
                generalComment: data.general_comment || '',
                hunkComments,
                fileComments,
                loaded: true,
              },
            },
          }))
        } catch (err) {
          logger.error('loadReview failed:', err)
        }
      },

      setGeneralComment: (sessionId, text) =>
        set((s) => ({
          bySession: {
            ...s.bySession,
            [sessionId]: { ...ensureSession(s, sessionId), generalComment: text },
          },
        })),

      setHunkComment: (sessionId, filePath, hunkId, text) =>
        set((s) => {
          const prev = ensureSession(s, sessionId)
          const hunkComments = { ...prev.hunkComments }
          const key = hunkCommentKey(filePath, hunkId)
          if (text) {
            hunkComments[key] = text
          } else {
            delete hunkComments[key]
          }
          return {
            bySession: { ...s.bySession, [sessionId]: { ...prev, hunkComments } },
          }
        }),

      removeHunkComment: (sessionId, filePath, hunkId) =>
        set((s) => {
          const prev = ensureSession(s, sessionId)
          const hunkComments = { ...prev.hunkComments }
          delete hunkComments[hunkCommentKey(filePath, hunkId)]
          return {
            bySession: { ...s.bySession, [sessionId]: { ...prev, hunkComments } },
          }
        }),

      setFileComment: (sessionId, filePath, text) =>
        set((s) => {
          const prev = ensureSession(s, sessionId)
          const fileComments = { ...prev.fileComments }
          if (text) {
            fileComments[filePath] = text
          } else {
            delete fileComments[filePath]
          }
          return {
            bySession: { ...s.bySession, [sessionId]: { ...prev, fileComments } },
          }
        }),

      setStatus: (sessionId, status) =>
        set((s) => ({
          bySession: {
            ...s.bySession,
            [sessionId]: { ...ensureSession(s, sessionId), status },
          },
        })),

      openReviewPage: (sessionId) =>
        set({ reviewPageOpen: true, activeReviewSession: sessionId }),

      closeReviewPage: () =>
        set({ reviewPageOpen: false, activeReviewSession: null }),

      enterReviewLoop: (sessionId) =>
        set((s) => ({
          reviewLoopActive: { ...s.reviewLoopActive, [sessionId]: true },
        })),

      exitReviewLoop: (sessionId) =>
        set((s) => {
          const next = { ...s.reviewLoopActive }
          delete next[sessionId]
          return { reviewLoopActive: next }
        }),

      clearSessionReview: (sessionId) =>
        set((s) => {
          const next = { ...s.bySession }
          delete next[sessionId]
          return { bySession: next }
        }),

      setDiffViewMode: (mode) => set({ diffViewMode: mode }),
    }),
    {
      name: 'c0wrk-review',
      version: 1,
      // v0→v1: the removed post-task review prompt persisted a
      // `promptShownForTask` key that zustand's default shallow rehydrate
      // merge would re-inject into state from older localStorage snapshots;
      // strip it so the stale key is gone deterministically at first load
      // (nothing reads it since ADR-055).
      migrate: (persistedState, _version) => {
        const state = { ...(persistedState ?? {}) } as Record<string, unknown>
        delete state.promptShownForTask
        return state as unknown as ReviewState & ReviewActions
      },
      partialize: (state) => ({
        reviewLoopActive: state.reviewLoopActive,
        diffViewMode: state.diffViewMode,
      }),
    },
  ),
)

/** Count total comments (general + files + hunks) for a session — used for button label derivation. */
export function totalCommentCount(state: SessionReviewState): number {
  let count = state.generalComment.trim() ? 1 : 0
  count += Object.values(state.fileComments).filter((c) => c.trim()).length
  count += Object.values(state.hunkComments).filter((c) => c.trim()).length
  return count
}
