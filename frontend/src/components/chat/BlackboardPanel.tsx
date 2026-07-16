import { useState, useMemo } from 'react'
import { ChevronDown, ChevronRight, ClipboardList, Search, Paperclip } from 'lucide-react'
import { cn } from '@/lib/utils'
import { formatBytes } from '@/lib/formatters'
import { useBlackboardState, useHasBlackboardState } from '@/stores/blackboardStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useUIStore } from '@/stores/uiStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import type { BlackboardState, BlackboardReflection } from '@/types/models'
import { StepTooltip } from './StepTooltip'

/** Builds the full markdown value of a reflection entry for the tooltip. */
function reflectionMarkdown(r: BlackboardReflection): string {
    return [
        r.summary,
        r.suggested_action && `**Action:** ${r.suggested_action}`,
        r.root_cause && `**Root cause:** ${r.root_cause}`,
        r.reasoning && `**Reasoning:** ${r.reasoning}`,
        r.failure_analysis && `**Failure analysis:** ${r.failure_analysis}`,
        r.action_plan && `**Action plan:** ${r.action_plan}`,
        r.hypotheses?.length
            ? `**Hypotheses:**\n${r.hypotheses.map(h => `- ${h}`).join('\n')}`
            : '',
    ].filter(Boolean).join('\n\n')
}

export function BlackboardPanel() {
    const activeSessionId = useSessionStore(s => s.activeSessionId)
    const bbState = useBlackboardState()
    const hasBB = useHasBlackboardState()
    const sidebarCollapsed = useUIStore(s => s.sidebarCollapsed)
    const viewerCollapsed = useFileViewerStore(s => s.collapsed)

    const [open, setOpen] = useState(false)
    const [search, setSearch] = useState('')

    if (!hasBB || !activeSessionId || !bbState) return null

    return (
        <div className={cn(
            'border-t border-x border-border bg-card',
            sidebarCollapsed && 'ml-1',
            viewerCollapsed && 'mr-1',
        )}>
            <div className="group">
                <button
                    onClick={() => setOpen(!open)}
                    className="flex items-center gap-2 w-full px-3 py-2 text-left text-foreground hover:bg-muted transition-colors rounded-sm"
                >
                    <span className="opacity-0 group-hover:opacity-100 transition-opacity inline-flex">
                        {open
                            ? <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
                            : <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />}
                    </span>
                    <ClipboardList className="h-3.5 w-3.5 text-muted-foreground" />
                    <span className="text-sm font-medium">Blackboard</span>
                    <BlackboardBadges state={bbState} />
                </button>
                {open && (
                    <div className="max-h-64 overflow-y-auto px-3 pb-2 custom-scrollbar">
                        <SearchBar value={search} onChange={setSearch} />
                        <BlackboardContent state={bbState} search={search} />
                    </div>
                )}
            </div>
        </div>
    )
}

function formatStepId(id: string): string {
    const match = id.match(/^step_(\d+)$/i)
    if (match) return `Step ${match[1]}`
    return id
}

function BlackboardBadges({ state }: { state: BlackboardState }) {
    const stepCount = Object.keys(state.step_results).length
    const factCount = state.facts.length
    const reflectionCount = state.reflections.length
    const attachmentCount = state.attachments.length

    return (
        <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
            {stepCount > 0 && <span>{stepCount} results</span>}
            {factCount > 0 && <span>{factCount} facts</span>}
            {reflectionCount > 0 && <span>{reflectionCount} reflections</span>}
            {attachmentCount > 0 && <span>{attachmentCount} files</span>}
        </span>
    )
}

function SearchBar({ value, onChange }: { value: string; onChange: (v: string) => void }) {
    return (
        <div className="relative mb-2">
            <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
            <input
                type="text"
                value={value}
                onChange={(e) => onChange(e.target.value)}
                placeholder="Search blackboard..."
                className="c0-input w-full pl-7 pr-2 py-1 text-xs border border-border rounded focus:outline-none focus:ring-1 focus:ring-primary"
            />
        </div>
    )
}

function BlackboardContent({ state, search }: { state: BlackboardState; search: string }) {
    const lowerSearch = search.toLowerCase()

    const filteredFacts = useMemo(
        () => state.facts.filter(f =>
            !search || f.content.toLowerCase().includes(lowerSearch) ||
            f.keywords.some(k => k.toLowerCase().includes(lowerSearch))
        ),
        [state.facts, lowerSearch, search],
    )

    const filteredReflections = useMemo(
        () => state.reflections.filter(r =>
            !search || r.summary.toLowerCase().includes(lowerSearch) ||
            (r.root_cause ?? '').toLowerCase().includes(lowerSearch)
        ),
        [state.reflections, lowerSearch, search],
    )

    const stepEntries = useMemo(() => Object.entries(state.step_results), [state.step_results])
    const filteredSteps = useMemo(
        () => stepEntries.filter(([id, sr]) =>
            !search || id.toLowerCase().includes(lowerSearch) ||
            sr.summary.toLowerCase().includes(lowerSearch)
        ),
        [stepEntries, lowerSearch, search],
    )

    const filteredAttachments = useMemo(
        () => state.attachments.filter(a =>
            !search ||
            a.original_name.toLowerCase().includes(lowerSearch) ||
            a.format.toLowerCase().includes(lowerSearch)
        ),
        [state.attachments, lowerSearch, search],
    )

    return (
        <div className="space-y-2 text-xs">
            {/* Step Results */}
            {filteredSteps.length > 0 && (
                <CollapsibleSection title="Step Results" count={filteredSteps.length}>
                    {filteredSteps.map(([id, sr]) => (
                        <StepTooltip
                            key={id}
                            description={sr.error ? `${sr.summary}\n\n**Error:** ${sr.error}` : sr.summary}
                            enabled={!!sr.summary}
                        >
                            <div className="py-0.5 pl-2 border-l border-border cursor-default">
                                <span className="font-medium text-foreground">{formatStepId(id)}</span>
                                {sr.error && <span className="ml-1.5 text-destructive">[error]</span>}
                                <p className="text-muted-foreground mt-0.5 line-clamp-2">{sr.summary}</p>
                            </div>
                        </StepTooltip>
                    ))}
                </CollapsibleSection>
            )}

            {/* Facts */}
            {filteredFacts.length > 0 && (
                <CollapsibleSection title="Facts" count={filteredFacts.length}>
                    {filteredFacts.map((f) => (
                        <StepTooltip
                            key={`${f.keywords.join(',')}-${f.author}-${f.content.slice(0, 40)}`}
                            description={f.content}
                            enabled={!!f.content}
                        >
                            <div className="py-0.5 pl-2 border-l border-border cursor-default">
                                <span className="text-info font-medium">[{f.keywords.join(', ')}]</span>
                                <span className="text-muted-foreground ml-1">{f.author}</span>
                                <p className="text-foreground mt-0.5 line-clamp-3">{f.content}</p>
                            </div>
                        </StepTooltip>
                    ))}
                </CollapsibleSection>
            )}

            {/* Attachments (committed — flushed from pending on send) */}
            {filteredAttachments.length > 0 && (
                <CollapsibleSection title="Attachments" count={filteredAttachments.length}>
                    {filteredAttachments.map((a) => (
                        <div
                            key={a.id}
                            className="py-0.5 pl-2 border-l border-border cursor-default flex items-center gap-1"
                        >
                            <Paperclip className="h-3 w-3 shrink-0 text-muted-foreground" />
                            <span className="text-foreground truncate">{a.original_name}</span>
                            <span className="text-muted-foreground shrink-0">({a.format})</span>
                            <span className="text-muted-foreground shrink-0">{formatBytes(a.size_bytes)}</span>
                        </div>
                    ))}
                </CollapsibleSection>
            )}

            {/* Reflections */}
            {filteredReflections.length > 0 && (
                <CollapsibleSection title="Reflections" count={filteredReflections.length}>
                    {filteredReflections.map((r) => (
                        <StepTooltip
                            key={`${r.summary.slice(0, 40)}-${r.root_cause ?? ''}`}
                            description={reflectionMarkdown(r)}
                            enabled={!!r.summary}
                        >
                            <div className="py-0.5 pl-2 border-l border-warning/40 cursor-default">
                                <p className="text-foreground">{r.summary}</p>
                                {r.suggested_action && (
                                    <span className="text-warning">Action: {r.suggested_action}</span>
                                )}
                                {r.root_cause && (
                                    <p className="text-muted-foreground mt-0.5">Root cause: {r.root_cause}</p>
                                )}
                            </div>
                        </StepTooltip>
                    ))}
                </CollapsibleSection>
            )}

            {/* Final Output */}
            {state.final_output && (!search || state.final_output.toLowerCase().includes(lowerSearch)) && (
                <CollapsibleSection title="Final Output" count={0}>
                    <StepTooltip description={state.final_output} enabled={!!state.final_output}>
                        <p className="text-foreground line-clamp-4 cursor-default">{state.final_output}</p>
                    </StepTooltip>
                </CollapsibleSection>
            )}
        </div>
    )
}

function CollapsibleSection({ title, count, children }: { title: string; count: number; children: React.ReactNode }) {
    const [open, setOpen] = useState(true)

    return (
        <div>
            <button
                onClick={() => setOpen(!open)}
                className="flex items-center gap-1 text-xs font-medium text-muted-foreground hover:text-foreground transition-colors w-full text-left"
            >
                {open ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
                <span>{title}</span>
                {count > 0 && <span className="text-muted-foreground/70">({count})</span>}
            </button>
            {open && <div className="mt-1 space-y-1">{children}</div>}
        </div>
    )
}
