// Autocomplete popup shown above the chat input for skill and file references.
// Rendered via portal to escape overflow-hidden containers.

import { useRef, useEffect, useLayoutEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { Zap, FileText } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { AutocompleteItem } from '@/hooks/useAutocomplete'

interface AutocompletePopupProps {
    items: AutocompleteItem[]
    selectedIndex: number
    onSelect: (index: number) => void
    anchorRef: React.RefObject<HTMLElement | null>
}

export function AutocompletePopup({ items, selectedIndex, onSelect, anchorRef }: AutocompletePopupProps) {
    const listRef = useRef<HTMLDivElement>(null)
    const selectedRef = useRef<HTMLDivElement>(null)
    const [style, setStyle] = useState<React.CSSProperties>({ position: 'fixed', visibility: 'hidden' })

    // Position the popup above the anchor element.
    useLayoutEffect(() => {
        const anchor = anchorRef.current
        if (!anchor) return
        const rect = anchor.getBoundingClientRect()
        setStyle({
            position: 'fixed',
            bottom: window.innerHeight - rect.top + 4,
            left: rect.left,
            width: rect.width,
        })
    }, [anchorRef, items.length])

    // Scroll selected item into view.
    useEffect(() => {
        selectedRef.current?.scrollIntoView({ block: 'nearest' })
    }, [selectedIndex])

    return createPortal(
        <div
            ref={listRef}
            style={style}
            className="z-50 max-h-[240px] overflow-y-auto rounded-md border border-border bg-popover shadow-lg custom-scrollbar"
            role="listbox"
        >
            {items.map((item, i) => (
                <div
                    key={`${item.type}-${item.value}`}
                    ref={i === selectedIndex ? selectedRef : undefined}
                    role="option"
                    aria-selected={i === selectedIndex}
                    className={cn(
                        'flex items-center gap-2 px-3 py-1.5 cursor-pointer text-sm',
                        i === selectedIndex ? 'bg-muted text-foreground' : 'text-muted-foreground hover:bg-muted/50',
                    )}
                    onMouseDown={(e) => {
                        e.preventDefault() // Prevent textarea blur
                        onSelect(i)
                    }}
                >
                    {item.type === 'skill' ? (
                        <Zap className="size-3.5 shrink-0 text-warning" />
                    ) : (
                        <FileText className="size-3.5 shrink-0 text-info" />
                    )}
                    <span className="shrink-0 font-mono text-xs">{item.label}</span>
                    {item.description && (
                        <span className="truncate text-xs text-muted-foreground">
                            {item.description}
                        </span>
                    )}
                </div>
            ))}
        </div>,
        document.body,
    )
}
