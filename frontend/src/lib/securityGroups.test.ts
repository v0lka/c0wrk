import { describe, it, expect } from 'vitest'
import {
  EXECUTE_GROUP,
  GROUP_ORDER,
  GROUP_META,
  POLICY_OPTIONS,
  DEFAULT_GROUP_POLICY,
} from './securityGroups'

// The security settings UI edits exactly the seven configurable groups. This
// pins the presentation metadata to that contract: the reserved "system"
// group must never appear, every group needs copy, and remote_write (which
// currently has no tools) must still be listed.
describe('securityGroups presentation metadata', () => {
  it('lists exactly the seven configurable groups', () => {
    expect(GROUP_ORDER).toHaveLength(7)
    expect(new Set(GROUP_ORDER).size).toBe(7)
  })

  it('never includes the reserved system group', () => {
    expect(GROUP_ORDER).not.toContain('system')
    expect(GROUP_META['system']).toBeUndefined()
  })

  it('includes remote_write (currently tool-less, still configurable)', () => {
    expect(GROUP_ORDER).toContain('remote_write')
    expect(GROUP_META['remote_write']!.title).toBe('Remote Write')
  })

  it('has metadata for every group in GROUP_ORDER and no extras', () => {
    expect(Object.keys(GROUP_META).sort()).toEqual([...GROUP_ORDER].sort())
    for (const group of GROUP_ORDER) {
      expect(GROUP_META[group]!.title, group).toBeTruthy()
      expect(GROUP_META[group]!.description, group).toBeTruthy()
    }
  })

  it('offers exactly the group policy enum in the dropdown options', () => {
    expect(POLICY_OPTIONS.map((o) => o.value)).toEqual(['allow', 'user_confirm', 'deny'])
  })

  it('marks execute as the only blacklist-capable group', () => {
    expect(EXECUTE_GROUP).toBe('execute')
    expect(GROUP_ORDER.filter((g) => g === EXECUTE_GROUP)).toHaveLength(1)
  })

  it('fails safe to user_confirm by default', () => {
    expect(DEFAULT_GROUP_POLICY).toBe('user_confirm')
  })
})
