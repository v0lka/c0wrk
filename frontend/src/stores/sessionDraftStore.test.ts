// Unit tests for sessionDraftStore — the pending "New Session" draft state
// (ADR-080 chat-side draft UX). The store itself is plain state + reducers;
// the commit path (createSessionFromDraft) lives in lib/sessionDraft.test.ts.
// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from 'vitest'
import { useSessionDraftStore } from '@/stores/sessionDraftStore'

function resetStore() {
  useSessionDraftStore.getState().clearDraft()
  useSessionDraftStore.getState().setCommitting(false)
}

describe('sessionDraftStore', () => {
  beforeEach(() => {
    resetStore()
  })

  it('starts with no draft and not committing', () => {
    const s = useSessionDraftStore.getState()
    expect(s.draft).toBeNull()
    expect(s.committing).toBe(false)
  })

  it('startDraft arms a local draft for the project', () => {
    useSessionDraftStore.getState().startDraft('proj-1')
    const s = useSessionDraftStore.getState()
    expect(s.draft).toEqual({ projectId: 'proj-1', workspace: { kind: 'local' } })
  })

  it('startDraft resets a previous workspace choice — every New Session starts local', () => {
    const { startDraft, setDraftWorkspace } = useSessionDraftStore.getState()
    startDraft('proj-1')
    setDraftWorkspace({ kind: 'branch', branch: 'feat/x', createBranch: true, startPoint: 'main' })
    startDraft('proj-1')
    expect(useSessionDraftStore.getState().draft).toEqual({
      projectId: 'proj-1',
      workspace: { kind: 'local' },
    })
  })

  it('setDraftWorkspace records a picked branch', () => {
    const { startDraft, setDraftWorkspace } = useSessionDraftStore.getState()
    startDraft('proj-1')
    setDraftWorkspace({ kind: 'branch', branch: 'feat/y', createBranch: false, startPoint: '' })
    const draft = useSessionDraftStore.getState().draft
    expect(draft?.workspace).toEqual({ kind: 'branch', branch: 'feat/y', createBranch: false, startPoint: '' })
  })

  it('setDraftWorkspace switches back to local', () => {
    const { startDraft, setDraftWorkspace } = useSessionDraftStore.getState()
    startDraft('proj-1')
    setDraftWorkspace({ kind: 'branch', branch: 'feat/y', createBranch: false, startPoint: '' })
    setDraftWorkspace({ kind: 'local' })
    expect(useSessionDraftStore.getState().draft?.workspace).toEqual({ kind: 'local' })
  })

  it('setDraftWorkspace is a no-op without an armed draft', () => {
    useSessionDraftStore.getState().setDraftWorkspace({ kind: 'branch', branch: 'b', createBranch: false, startPoint: '' })
    expect(useSessionDraftStore.getState().draft).toBeNull()
  })

  it('clearDraft drops the draft', () => {
    useSessionDraftStore.getState().startDraft('proj-1')
    useSessionDraftStore.getState().clearDraft()
    expect(useSessionDraftStore.getState().draft).toBeNull()
  })

  it('setCommitting toggles the in-flight flag', () => {
    useSessionDraftStore.getState().setCommitting(true)
    expect(useSessionDraftStore.getState().committing).toBe(true)
    useSessionDraftStore.getState().setCommitting(false)
    expect(useSessionDraftStore.getState().committing).toBe(false)
  })
})
