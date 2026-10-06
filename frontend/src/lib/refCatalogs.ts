// Display-time ref catalogs (issue #110): kind resolution for plain `/name`
// chips. Chip rendering needs to know whether a plain `/name` refers to an
// agent, a skill, or an MCP server, resolved from the CURRENT catalogs at
// display time (persisted messages may be older than the catalog). A
// module-level cache keeps every UserMessageContent instance from issuing
// its own RPC set; the catalog events drop it so a freshly created profile
// colors correctly without an app restart. `subscribe` is a no-op without
// the Wails runtime, so the invalidation wiring stays inert in tests.

import type { AgentDescriptor, SkillDescriptor } from '@/types/models'
import { listAgents } from '@/api/agents'
import { listSkills } from '@/api/skills'
import { getMCPMentionableServers, mentionableMCPNames } from '@/api/mcp'
import { subscribe } from '@/api/runtime'

export interface RefCatalogs {
  agentNames: Set<string>
  skillNames: Set<string>
  /** Mentionable MCP servers (auto + manual; disabled is inert and excluded),
   *  so a plain `/server` chip resolves to the MCP kind. */
  mcpNames: Set<string>
}

let refCatalogsCache: RefCatalogs | null = null
let refCatalogsInflight: Promise<RefCatalogs> | null = null
let refCatalogsSubscribed = false

function invalidateRefCatalogs() {
  refCatalogsCache = null
}

function ensureRefCatalogsSubscription() {
  if (refCatalogsSubscribed) return
  refCatalogsSubscribed = true
  subscribe('agents:changed', invalidateRefCatalogs)
  subscribe('skills:changed', invalidateRefCatalogs)
  subscribe('mcp:ready', invalidateRefCatalogs)
}

/** The cached catalogs, if already loaded (synchronous render fast path). */
export function cachedRefCatalogs(): RefCatalogs | null {
  return refCatalogsCache
}

/**
 * Load all three public catalogs once (listAgents + listSkills are
 * server-cached RPCs; getMCPMentionableServers is the secret-free
 * name+mode listing). A failing side degrades to an empty set for that kind
 * — an unknown name then renders as a neutral chip instead of blocking
 * message display.
 */
export function loadRefCatalogs(): Promise<RefCatalogs> {
  if (refCatalogsCache) return Promise.resolve(refCatalogsCache)
  if (!refCatalogsInflight) {
    ensureRefCatalogsSubscription()
    const agentsP: Promise<AgentDescriptor[]> = listAgents().catch(() => [])
    const skillsP: Promise<SkillDescriptor[]> = listSkills().catch(() => [])
    const mcpP = getMCPMentionableServers().then(mentionableMCPNames).catch(() => [])
    refCatalogsInflight = Promise.all([agentsP, skillsP, mcpP]).then(([agents, skills, mcpNames]) => {
      const catalogs: RefCatalogs = {
        agentNames: new Set(agents.map((a) => a.name)),
        skillNames: new Set(skills.map((s) => s.name)),
        mcpNames: new Set(mcpNames),
      }
      refCatalogsCache = catalogs
      refCatalogsInflight = null
      return catalogs
    })
  }
  return refCatalogsInflight
}
