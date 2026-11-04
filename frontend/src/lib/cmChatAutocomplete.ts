import {
  autocompletion,
  type Completion,
  type CompletionContext,
  type CompletionResult,
} from '@codemirror/autocomplete'
import type { Extension } from '@codemirror/state'
import { listSkills } from '@/api/skills'
import { listAgents } from '@/api/agents'
import { listCommands } from '@/api/commands'
import { getMCPMentionableServers } from '@/api/mcp'
import { getCapabilityModes } from '@/api/capabilities'
import { listDirectory, getSessionWorkspace } from '@/api/workspace'
import { subscribe } from '@/api/runtime'
import { logger } from '@/lib/logger'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { fuzzyFilter } from '@/lib/fuzzyMatch'
import { formatFileRefPath } from '@/lib/parseReferences'
import { isMentionableInContext, mentionableNamesInContext } from './capabilityModes'
import { activeSessionKind } from './sessionKind'
import { mentionTooltip, type MentionCompletion } from './cmMentionTooltip'
import type { SkillDescriptor, AgentDescriptor, CommandDescriptor, CapabilitiesInfo, MCPMentionableServer, FileEntry } from '@/types/models'

const DEFAULT_FILE_ICON = '\uf15b'
const DEFAULT_FOLDER_ICON = '\uf07b'

interface FileCompletion extends Completion {
  nerdIcon?: string
  nerdIconColor?: string
}

let skillsCache: SkillDescriptor[] = []
let skillsLoaded = false

let agentsCache: AgentDescriptor[] = []
let agentsLoaded = false

let commandsCache: CommandDescriptor[] = []
let commandsLoaded = false

let mcpCache: MCPMentionableServer[] = []
let mcpLoaded = false

// The capabilities section (skills/agents activation modes). The MCP modes
// ride on the mentionable servers themselves; the two file-backed catalogs
// need this listing to know what is disabled for the active session kind.
let capsCache: CapabilitiesInfo | null = null
let capsLoaded = false

let filesCache: FileEntry[] = []
let filesLoaded = false
let filesCacheRoot = ''
// Timestamp of the last failed listing fetch. While a fetch keeps failing
// (e.g. backend and frontend transiently disagree about the active project),
// every keystroke would otherwise fire a fresh ListDirectory RPC — a busy
// retry storm with no benefit, since the failure persists until the next
// switch. A short cooldown keeps self-healing (retry on the next trigger
// after it expires) while bounding the chatter.
let filesFetchFailedAt = 0
const FILES_FAILURE_COOLDOWN_MS = 1000

// Memoized completion root, keyed by the active session. resolveCompletionRoot
// runs on EVERY completion trigger (each keystroke of an @-query), so the
// GetSessionWorkspace RPC behind it must not: the memo serves repeated
// triggers, and invalidateFilesCache drops it — the transitions that refresh
// the listing caches (project/session switches, file-tree root changes,
// workspace tree changes) are exactly the ones that can change the
// backend-authoritative root or heal a memoized RPC-failure fallback.
let completionRootMemo: { sessionId: string; root: string | null } | null = null

/**
 * Resolve the workspace root for the @-file completion source.
 *
 * The backend is the authority: ListDirectory validates every path against
 * ITS active project, so the completion must ask the backend which root it
 * would accept. GetSessionWorkspace(activeSessionId) returns exactly that —
 * the session's workspace when it belongs to the active project, the active
 * project workspace otherwise — and is therefore immune to the transient
 * frontend/backend desyncs that follow rapid CHAT↔CODE / project / session
 * switches. Under those desyncs a root taken from fileTreeStore alone kept
 * failing containment on every call and @-hints stayed empty until an app
 * restart.
 *
 * Falls back to the file-tree root when no session is active or the RPC
 * fails (e.g. backend has no active project yet). The per-session memo above
 * bounds the RPC to real transitions; the fallback keeps read-live semantics
 * in the no-session case.
 */
async function resolveCompletionRoot(): Promise<string | null> {
  const sessionId = useSessionStore.getState().activeSessionId
  if (!sessionId) {
    // No session ⇒ no backend authority to ask: serve the live file-tree
    // root without memoizing (the store read is already fresh).
    return useFileTreeStore.getState().rootPath
  }
  if (completionRootMemo && completionRootMemo.sessionId === sessionId) {
    return completionRootMemo.root
  }
  let root: string | null = null
  try {
    const ws = await getSessionWorkspace(sessionId)
    if (ws) root = ws
  } catch (err) {
    logger.warn('completion root: GetSessionWorkspace failed; falling back to the file-tree root', err)
  }
  if (root === null) {
    root = useFileTreeStore.getState().rootPath
  }
  completionRootMemo = { sessionId, root }
  return root
}

function invalidateFilesCache() {
  filesLoaded = false
  // The memoized root shares the listing cache's lifecycle: every trigger
  // that can change the backend-authoritative root also refreshes the
  // listings, so dropping both here keeps a stale root — or a memoized
  // RPC-failure fallback — from outliving the transition.
  completionRootMemo = null
}

function invalidateSkillsCache() {
  skillsLoaded = false
}

function invalidateAgentsCache() {
  agentsLoaded = false
}

function invalidateCommandsCache() {
  commandsLoaded = false
}

function invalidateMCPCache() {
  mcpLoaded = false
}

function invalidateCapabilitiesCache() {
  capsLoaded = false
}

// Subscribe once to rootPath changes and filesystem/skills events.
let rootSubActive = false
function ensureRootSubscription() {
  if (rootSubActive) return
  // The flag is flipped only AFTER every subscription is registered:
  // subscribe() throws when the Wails runtime is not injected yet, and a
  // premature flag would permanently skip re-registration on later calls,
  // killing cache invalidation (and with it @-completions) for the whole
  // session. This function is fully synchronous, so no re-entrancy is
  // possible before the flag is set.
  let prevRoot = useFileTreeStore.getState().rootPath
  useFileTreeStore.subscribe((s) => {
    if (s.rootPath !== prevRoot) {
      prevRoot = s.rootPath
      invalidateFilesCache()
      invalidateSkillsCache()
      invalidateAgentsCache()
      invalidateCommandsCache()
      invalidateMCPCache()
      invalidateCapabilitiesCache()
    }
  })
  // Project and session switches change which workspace the backend expects
  // (and which root GetSessionWorkspace resolves to) even when the file-tree
  // root is momentarily unchanged or its writer (FileTreePanel) is unmounted
  // — e.g. collapsed sidebar or a non-explorer workspace tab. The completion
  // cache must not survive those transitions.
  let prevProjectId = useProjectStore.getState().activeProjectId
  useProjectStore.subscribe((s) => {
    if (s.activeProjectId !== prevProjectId) {
      prevProjectId = s.activeProjectId
      invalidateFilesCache()
      invalidateSkillsCache()
      invalidateAgentsCache()
      invalidateCommandsCache()
      invalidateMCPCache()
      invalidateCapabilitiesCache()
    }
  })
  let prevSessionId = useSessionStore.getState().activeSessionId
  useSessionStore.subscribe((s) => {
    if (s.activeSessionId !== prevSessionId) {
      prevSessionId = s.activeSessionId
      invalidateFilesCache()
      invalidateSkillsCache()
      invalidateAgentsCache()
      invalidateCommandsCache()
      invalidateMCPCache()
      invalidateCapabilitiesCache()
    }
  })
  // Filesystem changes inside the workspace (including project-local skills
  // and agents under .agents/) invalidate all caches so the next completion
  // request refetches fresh data.
  subscribe('workspace:tree_changed', () => {
    invalidateFilesCache()
    invalidateSkillsCache()
    invalidateAgentsCache()
    invalidateCommandsCache()
    invalidateMCPCache()
    invalidateCapabilitiesCache()
  })
  // Global skill directory changes (outside the workspace) invalidate the
  // skills cache only — files and agents are unaffected.
  subscribe('skills:changed', () => {
    invalidateSkillsCache()
  })
  // Global Subagent Profile directory changes (outside the workspace)
  // invalidate the agents cache only.
  subscribe('agents:changed', () => {
    invalidateAgentsCache()
  })
  // Global custom command directory changes (outside the workspace)
  // invalidate the commands cache only (ADR-096).
  subscribe('commands:changed', () => {
    invalidateCommandsCache()
  })
  // The MCP gateway finishing its (re)start is the one MCP lifecycle event
  // the backend emits — a fresh mentionable set is cheapest to pick up here.
  subscribe('mcp:ready', () => {
    invalidateMCPCache()
  })
  rootSubActive = true
}

async function getSkills(): Promise<SkillDescriptor[]> {
  if (skillsLoaded) return skillsCache
  try {
    skillsCache = await listSkills()
    skillsLoaded = true
  } catch {
    skillsCache = []
  }
  return skillsCache
}

async function getAgents(): Promise<AgentDescriptor[]> {
  if (agentsLoaded) return agentsCache
  try {
    agentsCache = await listAgents()
    agentsLoaded = true
  } catch {
    agentsCache = []
  }
  return agentsCache
}

async function getCommands(): Promise<CommandDescriptor[]> {
  if (commandsLoaded) return commandsCache
  try {
    commandsCache = await listCommands()
    commandsLoaded = true
  } catch {
    // A transient RPC hiccup hides the section for one trigger, never
    // blocks the others.
    commandsCache = []
  }
  return commandsCache
}

async function getMCPServers(): Promise<MCPMentionableServer[]> {
  if (mcpLoaded) return mcpCache
  // getMCPMentionableServers degrades to [] on failure (logged there), so a
  // transient RPC hiccup hides the section for one trigger, never blocks it.
  // The FULL listing is cached; the per-kind filtering (auto/manual offered,
  // disabled hidden for the ACTIVE session's kind) happens at use time in
  // slashSource, so a session switch re-filters without a refetch.
  mcpCache = await getMCPMentionableServers()
  mcpLoaded = true
  return mcpCache
}

/**
 * The capabilities section (skills/agents activation modes). A failure
 * resolves to null — the callers then filter nothing, because a transient
 * capabilities hiccup must degrade to "everything offered", never to
 * silently hiding whole catalogs. (GetCapabilityModes itself only throws on
 * an RPC failure; a malformed shape is already degraded to empty sections,
 * which likewise filter nothing — every missing entry reads as auto.)
 */
async function getCapabilities(): Promise<CapabilitiesInfo | null> {
  if (capsLoaded) return capsCache
  try {
    capsCache = await getCapabilityModes()
    capsLoaded = true
  } catch (err) {
    logger.warn('completion: GetCapabilityModes failed; offering all skills/agents', err)
    // A failed REFETCH must not resurrect the previous context's cache: after
    // an invalidation (project/session switch) the stale modes would keep
    // filtering the NEW catalogs. null — like the [] siblings above — filters
    // nothing, and capsLoaded stays false so the next trigger retries.
    capsCache = null
  }
  return capsCache
}

async function getFiles(): Promise<{ entries: FileEntry[]; root: string | null }> {
  const rootPath = await resolveCompletionRoot()
  if (!rootPath) return { entries: [], root: null }
  if (filesLoaded && filesCacheRoot === rootPath) return { entries: filesCache, root: rootPath }
  if (Date.now() - filesFetchFailedAt < FILES_FAILURE_COOLDOWN_MS) return { entries: [], root: rootPath }
  try {
    const entries = await listDirectory(rootPath, true)
    filesCache = entries
    // Cache only non-empty listings. An empty result may be a transient
    // failure — the workspace directory not yet materialized (No Project
    // sessions create it lazily), or an invalid RPC payload degraded to []
    // by the api guard. Caching it would suppress @-completions until the
    // next workspace:tree_changed event or an app restart (the reported
    // "hints stop appearing until restart" bug). Retrying on the next
    // completion trigger self-heals once the directory exists.
    filesLoaded = entries.length > 0
    filesCacheRoot = rootPath
    filesFetchFailedAt = 0
    return { entries, root: rootPath }
  } catch (err) {
    filesCache = []
    filesFetchFailedAt = Date.now()
    // Never silent: a completion root the backend keeps rejecting is exactly
    // the "hints are dead until restart" failure mode, and without a log it
    // is undiagnosable which side of the desync produced it.
    logger.error(`completion: ListDirectory failed for root ${rootPath}`, err)
    return { entries: [], root: rootPath }
  }
}

function relativePath(absPath: string, rootPath: string | null): string {
  if (rootPath && absPath.startsWith(rootPath + '/')) {
    return absPath.slice(rootPath.length + 1)
  }
  return absPath
}

/** The built-in /goal backend command surfaced in the Commands section. It is
 *  grammar, not a catalog entry: when any VISIBLE entry (agent, skill, MCP
 *  server, user command) is named "goal", the plain spelling resolves to that
 *  entry and the built-in is hidden (shadowing parity, ADR-076/ADR-096).
 *  specs/domains/goal-mode.md, specs/domains/commands.md. */
const BUILT_IN_COMMAND: CommandDescriptor = {
  name: 'goal',
  description: 'Built-in — run the task in goal mode (/goal:N sets a turn budget)',
  agent: '',
  model: '',
  template: '',
}

function visibleBuiltInCommands(
  agentNames: Set<string>,
  skillNames: Set<string>,
  mcpNames: Set<string>,
  commands: CommandDescriptor[],
): CommandDescriptor[] {
  const shadowed =
    agentNames.has(BUILT_IN_COMMAND.name) ||
    skillNames.has(BUILT_IN_COMMAND.name) ||
    mcpNames.has(BUILT_IN_COMMAND.name) ||
    commands.some((c) => c.name === BUILT_IN_COMMAND.name)
  return shadowed ? [] : [BUILT_IN_COMMAND]
}

/**
 * Text inserted into the chat input when a file is selected: a path made
 * relative to the workspace root (when the file lives inside it), formatted
 * for the @-ref syntax, plus the given suffix (e.g. `' '` or `'/'`). Paths
 * with spaces are single-quoted (@'my file.go'); see formatFileRefPath.
 * `forceQuoted` reintroduces the opening quote while completing inside an
 * already-open @'…' ref (the replacement range swallows the typed quote).
 * Files outside the workspace keep their absolute path.
 */
function chatApplyPath(absPath: string, rootPath: string | null, suffix: string, forceQuoted = false): string {
  const rel = relativePath(absPath, rootPath)
  // A DIRECTORY completed must keep the quote OPEN: the ref continues into the
  // child path (@'src/…). Closing it here (@'src'/) would make the trigger scan
  // treat the second quote as a closed ref and silently kill all further
  // completions. This applies whenever the applied path needs quoting — either
  // because the user already typed @' (forceQuoted), or because the directory
  // NAME contains a space, which formatFileRefPath would otherwise emit as the
  // CLOSED form (@'beta dir'/). Only a FILE closes the quote, since a file ends
  // the ref.
  if (suffix === '/' && !rel.includes("'") && (forceQuoted || rel.includes(' '))) {
    return `'${rel}/`
  }
  return formatFileRefPath(rel, forceQuoted) + suffix
}

// One `/` source for ALL capability kinds (issue #110 + the MCP mention
// flow): commands, subagents, MCP servers and skills share the single slash
// trigger, presented as four labelled sections (Commands, Subagents, MCP
// Servers, Skills). Commands are invocations (ADR-096): a picked command
// inserts its /name (or the collision-qualified /command: name when the name
// collides with a mention catalog) and the send path expands the template —
// they are not session-kind filtered (no capability modes exist for them;
// project-local commands simply do not exist in a No-Project session). The
// built-in /goal backend command rides the Commands section (visibleBuiltInCommands
// hides it when the plain spelling is shadowed by a catalog entry).
// A name present in more than one catalog is a collision — every colliding
// entry is offered in its collision-qualified spelling (`/agent: name`,
// `/mcp: name`, `/skill: name`, `/command: name`, the canonical spaced form)
// so the applied text is unambiguous on the send path. The mention sets are
// SESSION-KIND-AWARE: the kind of the ACTIVE session (No Project → chat, a
// real project → code) picks the capabilities column, so `auto` + `manual`
// entries are offered and `disabled` ones are hidden — mirroring what the
// backend would honor for a task started from this input. A session switch
// re-reads the kind on the next trigger and flips the offered sets.
async function slashSource(ctx: CompletionContext): Promise<CompletionResult | null> {
  // Scan backward for '/' trigger.
  const line = ctx.state.doc.lineAt(ctx.pos)
  const textBefore = line.text.slice(0, ctx.pos - line.from)

  let triggerIdx = -1
  for (let i = textBefore.length - 1; i >= 0; i--) {
    const ch = textBefore[i]
    if (ch === ' ' || ch === '\t') break
    if (ch === '/') {
      if (i === 0 || textBefore[i - 1] === ' ' || textBefore[i - 1] === '\t') {
        triggerIdx = i
      }
      break
    }
  }

  if (triggerIdx === -1) return null

  const from = line.from + triggerIdx + 1
  const query = textBefore.slice(triggerIdx + 1)

  const kind = activeSessionKind()
  const [agents, mcpServers, skills, caps, commands] = await Promise.all([
    getAgents(),
    getMCPServers(),
    getSkills(),
    getCapabilities(),
    getCommands(),
  ])
  const visibleAgentNames = new Set(mentionableNamesInContext(agents.map((a) => a.name), caps?.agents, kind))
  const visibleSkillNames = new Set(mentionableNamesInContext(skills.map((s) => s.name), caps?.skills, kind))
  const agentNames = visibleAgentNames
  const skillNames = visibleSkillNames
  // Collision detection runs over the KIND-VISIBLE sets: a name whose MCP
  // entry is disabled for the active session's kind no longer collides with
  // its skill/agent namesake there — the completion then offers the plain
  // spelling, exactly what the send path would resolve it to.
  const mcpNames = new Set(mcpServers.filter((m) => isMentionableInContext(kind, m)).map((m) => m.name))
  const visibleMCPServers = mcpServers.filter((m) => mcpNames.has(m.name))

  const options: MentionCompletion[] = []

  // Commands first: section rank 0 — the "run this" affordance answers the
  // `/` trigger like opencode's command palette (ADR-096). A command whose
  // name collides with a visible mention catalog entry is offered in its
  // collision-qualified spelling so the send path resolves it as a command
  // (the plain spelling would be the ambiguous no-op). No session-kind
  // filter: commands have no capability modes.
  for (const c of fuzzyFilter(query, [...commands, ...visibleBuiltInCommands(agentNames, skillNames, mcpNames, commands)], (c) => c.name)) {
    const collides = agentNames.has(c.name) || skillNames.has(c.name) || mcpNames.has(c.name)
    const detail = c.description !== '' ? c.description : 'Custom command'
    options.push({
      label: collides ? `/command: ${c.name}` : c.name,
      detail,
      mentionMarkdown: detail,
      type: 'command',
      section: { name: 'Commands', rank: 0 },
      apply: (collides ? `command: ${c.name}` : c.name) + ' ',
    })
  }

  // Subagents: section rank 1 orders the section above MCP Servers and
  // Skills, and the array order keeps agents ahead within the unfiltered
  // list.
  for (const a of fuzzyFilter(query, agents.filter((a) => agentNames.has(a.name)), (a) => a.name)) {
    const collides = skillNames.has(a.name) || mcpNames.has(a.name) || commands.some((c) => c.name === a.name)
    options.push({
      label: collides ? `/agent: ${a.name}` : a.name,
      detail: a.description,
      // Full description for the hover/selection markdown pane (cmMentionTooltip).
      mentionMarkdown: a.description,
      type: 'agent',
      section: { name: 'Subagents', rank: 1 },
      // The replacement range starts AFTER the typed '/', so the applied
      // text must not repeat it.
      apply: (collides ? `agent: ${a.name}` : a.name) + ' ',
    })
  }
  // MCP Servers between Subagents and Skills (rank 2): the kind-aware filter
  // above already dropped servers disabled for the active session's kind; a
  // server parked in `manual` THERE is the mention-triggered one (the detail
  // says so), an auto server is always connected but still mentionable to
  // surface the soft directive.
  for (const m of fuzzyFilter(query, visibleMCPServers, (m) => m.name)) {
    const collides = agentNames.has(m.name) || skillNames.has(m.name) || commands.some((c) => c.name === m.name)
    const manual = (kind === 'chat' ? m.chat_mode : m.code_mode) === 'manual'
    const summary = manual ? 'MCP server — mention to enable' : 'MCP server'
    options.push({
      label: collides ? `/mcp: ${m.name}` : m.name,
      detail: summary,
      mentionMarkdown: summary,
      type: 'mcp',
      section: { name: 'MCP Servers', rank: 2 },
      apply: (collides ? `mcp: ${m.name}` : m.name) + ' ',
    })
  }
  for (const s of fuzzyFilter(query, skills.filter((s) => skillNames.has(s.name)), (s) => s.name)) {
    const collides = agentNames.has(s.name) || mcpNames.has(s.name) || commands.some((c) => c.name === s.name)
    options.push({
      label: collides ? `/skill: ${s.name}` : s.name,
      detail: s.description,
      mentionMarkdown: s.description,
      type: 'skill',
      section: { name: 'Skills', rank: 3 },
      apply: (collides ? `skill: ${s.name}` : s.name) + ' ',
    })
  }
  if (options.length === 0) return null

  return { from, filter: false, options }
}

async function fileSource(ctx: CompletionContext): Promise<CompletionResult | null> {
  ensureRootSubscription()

  const line = ctx.state.doc.lineAt(ctx.pos)
  const textBefore = line.text.slice(0, ctx.pos - line.from)

  // Scan backward for the '@' trigger. Unlike the / and # sources, the scan
  // does NOT stop at whitespace: a quoted ref (@'my file) has spaces inside
  // the query. Instead the last boundary-valid '@' is found first and the
  // query shape decides whether a completion is active at all — an unquoted
  // query containing whitespace (prose after the ref, e.g. `@x.go and more`)
  // and a CLOSED quoted ref (@'my file') both yield no trigger.
  let triggerIdx = -1
  for (let i = textBefore.length - 1; i >= 0; i--) {
    if (textBefore[i] === '@') {
      if (i === 0 || textBefore[i - 1] === ' ' || textBefore[i - 1] === '\t') {
        triggerIdx = i
      }
      break
    }
  }

  if (triggerIdx === -1) return null

  // `from` covers everything after the '@' — including the typed opening
  // quote of an @'…' ref — so the applied completion replaces the whole
  // partial query (and reintroduces the quote; see chatApplyPath).
  const from = line.from + triggerIdx + 1
  const token = textBefore.slice(triggerIdx + 1)

  let query: string
  let quoted = false
  if (token.startsWith("'")) {
    // Quoted form in progress: stays open until the closing quote appears.
    if (token.indexOf("'", 1) !== -1) return null
    quoted = true
    query = token.slice(1)
  } else {
    // Unquoted refs end at whitespace — a query containing spaces is stale
    // prose trailing an earlier ref, not an active trigger.
    if (/\s/.test(token)) return null
    query = token
  }

  const { entries, root: rootPath } = await getFiles()
  if (entries.length === 0) return null

  const openTabs = useFileViewerStore.getState().openTabs
  const openTabsSet = new Set(openTabs)

  const options: FileCompletion[] = []

  if (query.length === 0) {
    // Show pinned tabs first, then remaining entries.
    for (const path of openTabs) {
      const entry = entries.find((e) => e.path === path)
      if (entry) {
        options.push({
          label: relativePath(entry.path, rootPath),
          type: 'file',
          boost: 10,
          apply: chatApplyPath(entry.path, rootPath, ' ', quoted),
          nerdIcon: entry.icon || DEFAULT_FILE_ICON,
          nerdIconColor: entry.icon_color,
        })
      }
    }
    const cap = 50 - options.length
    for (const f of entries) {
      if (options.length >= 50) break
      if (!openTabsSet.has(f.path)) {
        const suffix = f.is_dir ? '/' : ' '
        options.push({
          label: relativePath(f.path, rootPath),
          type: f.is_dir ? 'folder' : 'file',
          apply: chatApplyPath(f.path, rootPath, suffix, quoted),
          nerdIcon: f.is_dir ? (f.icon || DEFAULT_FOLDER_ICON) : (f.icon || DEFAULT_FILE_ICON),
          nerdIconColor: f.icon_color,
        })
      }
      if (options.length - openTabs.length >= cap) break
    }
  } else {
    const filtered = fuzzyFilter(query, entries, (f) => f.name)
    for (const f of filtered) {
      const pinned = openTabsSet.has(f.path)
      const suffix = f.is_dir ? '/' : ' '
      options.push({
        label: relativePath(f.path, rootPath),
        type: f.is_dir ? 'folder' : 'file',
        boost: pinned ? 10 : 0,
        apply: chatApplyPath(f.path, rootPath, suffix, quoted),
        nerdIcon: f.is_dir ? (f.icon || DEFAULT_FOLDER_ICON) : (f.icon || DEFAULT_FILE_ICON),
        nerdIconColor: f.icon_color,
      })
    }
  }

  if (options.length === 0) return null

  return { from, filter: false, options }
}

/**
 * CodeMirror autocomplete extension configured with the unified `/` source
 * (subagents + MCP servers + skills, issue #110 + the MCP mention flow) and
 * the @file source. `#` is no longer a trigger anywhere.
 *
 * The popup opens ABOVE the trigger line for BOTH lists (`aboveCursor` — the
 * view still falls back below when there is no room above), and the
 * mentionTooltip extension renders the full description of a hovered or
 * keyboard-selected `/` row as sanitized markdown.
 */
export function createChatAutocomplete(): Extension {
  ensureRootSubscription()
  return [
    autocompletion({
      override: [slashSource, fileSource],
      closeOnBlur: true,
      activateOnTyping: true,
      icons: false,
      aboveCursor: true,
      optionClass: (completion) =>
        completion.type === 'agent'
          ? 'agent-item'
          : completion.type === 'skill'
            ? 'skill-item'
            : completion.type === 'mcp'
              ? 'mcp-item'
              : completion.type === 'command'
                ? 'command-item'
                : 'file-item',
      addToOptions: [
        {
          render: (completion: Completion) => {
            const fc = completion as FileCompletion
            if (!fc.nerdIcon) return null
            const span = document.createElement('span')
            span.className = 'cm-completion-nerd-icon'
            span.textContent = fc.nerdIcon
            if (fc.nerdIconColor) {
              span.style.color = fc.nerdIconColor
            }
            return span
          },
          position: 20,
        },
      ],
    }),
    mentionTooltip(),
  ]
}
