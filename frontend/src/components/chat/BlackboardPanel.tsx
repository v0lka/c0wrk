import { useState, useMemo } from 'react'
import { ChevronDown, ChevronRight, Brain, Search } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useBlackboardState, useHasBlackboardState } from '@/stores/blackboardStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useUIStore } from '@/stores/uiStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import type { BlackboardState } from '@/types/models'

export function BlackboardPanel() {
    const activeSessionId = useSessionStore(s => s.activeSessionId)
    const bbState = useBlackboardState()
    const hasBB = useHasBlackboardState()
    const sidebarCollapsed = useUIStore(s => s.sidebarCollapsed)
    const viewerCollapsed = useFileViewerStore(s => s.collapsed)
    const hasViewerTabs = useFileViewerStore(s => s.openTabs.length > 0)

    const [open, setOpen] = useState(false)
    const [search, setSearch] = useState('')

    if (!hasBB || !activeSessionId || !bbState) return null

    return (
        <div className={cn(
            'border-t border-x border-border bg-card mt-1',
            sidebarCollapsed && 'ml-1',
            viewerCollapsed && hasViewerTabs && 'mr-1',
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
                    <Brain className="h-3.5 w-3.5 text-muted-foreground" />
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

function BlackboardBadges({ state }: { state: BlackboardState }) {
    const stepCount = Object.keys(state.step_results).length
    const factCount = state.facts.length
    const reflectionCount = state.reflections.length

    return (
        <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
            {state.plan && <span>{state.plan.steps.length} steps</span>}
            {stepCount > 0 && <span>{stepCount} results</span>}
            {factCount > 0 && <span>{factCount} facts</span>}
            {reflectionCount > 0 && <span>{reflectionCount} reflections</span>}
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
                className="w-full pl-7 pr-2 py-1 text-xs bg-muted border border-border rounded text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-primary"
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

    return (
        <div className="space-y-2 text-xs">
            {/* Plan */}
            {state.plan && state.plan.steps.length > 0 && (
                <CollapsibleSection title="Plan" count={state.plan.steps.length}>
                    {state.plan.steps.map((s) => (
                        <div key={s.id} className="py-0.5 pl-2 border-l border-border">
                            <span className="font-medium text-foreground">{s.id}</span>
                            <span className="text-muted-foreground ml-1.5">{s.summary || s.description}</span>
                        </div>
                    ))}
                </CollapsibleSection>
            )}

            {/* Step Results */}
            {filteredSteps.length > 0 && (
                <CollapsibleSection title="Step Results" count={filteredSteps.length}>
                    {filteredSteps.map(([id, sr]) => (
                        <div key={id} className="py-0.5 pl-2 border-l border-border">
                            <span className="font-medium text-foreground">{id}</span>
                            {sr.error && <span className="ml-1.5 text-destructive">[error]</span>}
                            <p className="text-muted-foreground mt-0.5 line-clamp-2">{sr.summary}</p>
                        </div>
                    ))}
                </CollapsibleSection>
            )}

            {/* Facts */}
            {filteredFacts.length > 0 && (
                <CollapsibleSection title="Facts" count={filteredFacts.length}>
                    {filteredFacts.map((f, i) => (
                        <div key={i} className="py-0.5 pl-2 border-l border-border">
                            <span className="text-info font-medium">[{f.keywords.join(', ')}]</span>
                            <span className="text-muted-foreground ml-1">{f.author}</span>
                            <p className="text-foreground mt-0.5 line-clamp-3">{f.content}</p>
                        </div>
                    ))}
                </CollapsibleSection>
            )}

            {/* Reflections */}
            {filteredReflections.length > 0 && (
                <CollapsibleSection title="Reflections" count={filteredReflections.length}>
                    {filteredReflections.map((r, i) => (
                        <div key={i} className="py-0.5 pl-2 border-l border-warning/40">
                            <p className="text-foreground">{r.summary}</p>
                            {r.suggested_action && (
                                <span className="text-warning">Action: {r.suggested_action}</span>
                            )}
                            {r.root_cause && (
                                <p className="text-muted-foreground mt-0.5">Root cause: {r.root_cause}</p>
                            )}
                        </div>
                    ))}
                </CollapsibleSection>
            )}

            {/* Final Output */}
            {state.final_output && (!search || state.final_output.toLowerCase().includes(lowerSearch)) && (
                <CollapsibleSection title="Final Output" count={0}>
                    <p className="text-foreground line-clamp-4">{state.final_output}</p>
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
