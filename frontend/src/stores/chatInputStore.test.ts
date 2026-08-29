// Unit tests for chatInputStore — per-session chat-input slices (draft,
// optimize-in-flight flag, optimize error, send error) keyed by session id.
//
// Plain node environment: the store uses plain `create` (no persist
// middleware), so no jsdom/localStorage is needed.

import { describe, it, expect, beforeEach } from 'vitest'
import {
  useChatInputStore,
  getInputState,
  EMPTY_CHAT_INPUT,
  NULL_SESSION_KEY,
} from '@/stores/chatInputStore'

/** Reset the store to initial state before each test. */
function resetStore() {
  useChatInputStore.setState({ inputs: {} })
}

describe('chatInputStore', () => {
  beforeEach(() => {
    resetStore()
  })

  // ── Initial state ──

  it('starts with an empty inputs map', () => {
    expect(useChatInputStore.getState().inputs).toEqual({})
  })

  // ── setDraft ──

  it('setDraft creates a slice with defaults for an unknown session', () => {
    const { setDraft } = useChatInputStore.getState()
    setDraft('sess-a', 'hello')
    expect(useChatInputStore.getState().inputs['sess-a']).toEqual({
      draft: 'hello',
      isOptimizing: false,
      optimizeError: null,
      sendError: null,
    })
  })

  it('setDraft updates the draft without touching other sessions', () => {
    const { setDraft } = useChatInputStore.getState()
    setDraft('sess-a', 'a draft')
    setDraft('sess-b', 'b draft')
    setDraft('sess-a', 'a draft v2')
    const inputs = useChatInputStore.getState().inputs
    expect(inputs['sess-a']?.draft).toBe('a draft v2')
    expect(inputs['sess-b']?.draft).toBe('b draft')
  })

  it('setDraft with the same value keeps the record reference stable', () => {
    const { setDraft } = useChatInputStore.getState()
    setDraft('sess-a', 'same')
    const before = useChatInputStore.getState().inputs
    setDraft('sess-a', 'same')
    expect(useChatInputStore.getState().inputs).toBe(before)
  })

  it('setDraft of an empty string on an absent slice is a no-op (sparse map)', () => {
    const { setDraft } = useChatInputStore.getState()
    const before = useChatInputStore.getState().inputs
    setDraft('sess-a', '')
    expect(useChatInputStore.getState().inputs).toBe(before)
    expect('sess-a' in useChatInputStore.getState().inputs).toBe(false)
  })

  it('setDraft clears a draft to empty string when a slice exists', () => {
    const { setDraft } = useChatInputStore.getState()
    setDraft('sess-a', 'text')
    setDraft('sess-a', '')
    expect(useChatInputStore.getState().inputs['sess-a']?.draft).toBe('')
  })

  it('setDraft writes to the NULL_SESSION_KEY sentinel slot', () => {
    const { setDraft } = useChatInputStore.getState()
    setDraft(NULL_SESSION_KEY, 'scratch')
    expect(useChatInputStore.getState().inputs[NULL_SESSION_KEY]?.draft).toBe('scratch')
  })

  // ── setOptimizing ──

  it('setOptimizing toggles the flag on the given session only', () => {
    const { setOptimizing } = useChatInputStore.getState()
    setOptimizing('sess-a', true)
    expect(useChatInputStore.getState().inputs['sess-a']?.isOptimizing).toBe(true)
    expect(useChatInputStore.getState().inputs['sess-b']).toBeUndefined()
    setOptimizing('sess-a', false)
    expect(useChatInputStore.getState().inputs['sess-a']?.isOptimizing).toBe(false)
  })

  it('setOptimizing(false) on an absent slice is a no-op', () => {
    const { setOptimizing } = useChatInputStore.getState()
    const before = useChatInputStore.getState().inputs
    setOptimizing('sess-a', false)
    expect(useChatInputStore.getState().inputs).toBe(before)
  })

  // ── setOptimizeError ──

  it('setOptimizeError sets and clears the error on the given session', () => {
    const { setOptimizeError } = useChatInputStore.getState()
    setOptimizeError('sess-a', 'Optimization failed: boom')
    expect(useChatInputStore.getState().inputs['sess-a']?.optimizeError).toBe(
      'Optimization failed: boom',
    )
    setOptimizeError('sess-a', null)
    expect(useChatInputStore.getState().inputs['sess-a']?.optimizeError).toBeNull()
  })

  it('setOptimizeError(null) on an absent slice is a no-op', () => {
    const { setOptimizeError } = useChatInputStore.getState()
    const before = useChatInputStore.getState().inputs
    setOptimizeError('sess-a', null)
    expect(useChatInputStore.getState().inputs).toBe(before)
  })

  // ── setSendError ──

  it('setSendError sets and clears the error on the given session only', () => {
    const { setSendError } = useChatInputStore.getState()
    setSendError('sess-a', 'rpc down')
    expect(useChatInputStore.getState().inputs['sess-a']?.sendError).toBe('rpc down')
    expect(useChatInputStore.getState().inputs['sess-b']).toBeUndefined()
    setSendError('sess-a', null)
    expect(useChatInputStore.getState().inputs['sess-a']?.sendError).toBeNull()
  })

  it('setSendError writes to the NULL_SESSION_KEY sentinel slot', () => {
    const { setSendError } = useChatInputStore.getState()
    setSendError(NULL_SESSION_KEY, 'create failed')
    expect(useChatInputStore.getState().inputs[NULL_SESSION_KEY]?.sendError).toBe('create failed')
  })

  it('setSendError(null) on an absent slice is a no-op (stable reference)', () => {
    const { setSendError } = useChatInputStore.getState()
    const before = useChatInputStore.getState().inputs
    setSendError('sess-a', null)
    expect(useChatInputStore.getState().inputs).toBe(before)
  })

  // ── dropSessions ──

  it('dropSessions removes only the listed sessions', () => {
    const { setDraft, dropSessions } = useChatInputStore.getState()
    setDraft('sess-a', 'a')
    setDraft('sess-b', 'b')
    setDraft('sess-c', 'c')
    dropSessions(['sess-a', 'sess-c'])
    const inputs = useChatInputStore.getState().inputs
    expect(inputs['sess-a']).toBeUndefined()
    expect(inputs['sess-c']).toBeUndefined()
    expect(inputs['sess-b']?.draft).toBe('b')
  })

  it('dropSessions is a no-op for unknown ids (stable reference)', () => {
    const { setDraft, dropSessions } = useChatInputStore.getState()
    setDraft('sess-a', 'a')
    const before = useChatInputStore.getState().inputs
    dropSessions(['sess-nope'])
    expect(useChatInputStore.getState().inputs).toBe(before)
  })

  it('dropSessions never drops the NULL_SESSION_KEY sentinel slot', () => {
    const { setDraft } = useChatInputStore.getState()
    setDraft(NULL_SESSION_KEY, 'scratch')
    setDraft('sess-a', 'a')
    // The sentinel is only dropped if explicitly listed — deletion paths pass
    // real session ids, so the scratch slot survives session deletion.
    useChatInputStore.getState().dropSessions(['sess-a'])
    expect(useChatInputStore.getState().inputs[NULL_SESSION_KEY]?.draft).toBe('scratch')
  })

  // ── getInputState selector helper ──

  it('getInputState returns the stable empty default for unknown/null sessions', () => {
    const inputs = useChatInputStore.getState().inputs
    expect(getInputState(inputs, 'sess-a')).toBe(EMPTY_CHAT_INPUT)
    expect(getInputState(inputs, null)).toBe(EMPTY_CHAT_INPUT)
  })

  it('getInputState resolves null to the sentinel slot', () => {
    useChatInputStore.getState().setDraft(NULL_SESSION_KEY, 'scratch')
    expect(getInputState(useChatInputStore.getState().inputs, null).draft).toBe('scratch')
  })
})
