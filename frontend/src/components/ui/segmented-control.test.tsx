// @vitest-environment jsdom
// SegmentedControl — the app-wide segmented switcher.
//
// Covers the contract every migrated call site relies on: render shape,
// aria-selected/aria-checked per semantic flavor, click behavior, roving
// tabindex, arrow-key navigation (the ResearchPanel control's keyboard model,
// generalized), sizes, and the fullWidth layout class.
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest'

import { SegmentedControl } from './segmented-control'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
})

function item(value: string): HTMLButtonElement {
  const el = container.querySelector<HTMLButtonElement>(`[data-segment-value="${value}"]`)
  if (!el) throw new Error(`segment item "${value}" not rendered`)
  return el
}

function keydown(key: string): void {
  const list = container.querySelector('[role="tablist"], [role="radiogroup"]')!
  act(() => {
    list.dispatchEvent(
      new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }),
    )
  })
}

const items = [
  { value: 'a', label: 'Alpha' },
  { value: 'b', label: 'Beta' },
  { value: 'c', label: 'Gamma' },
]

describe('SegmentedControl — tabs semantic', () => {
  it('renders the tablist/tab pattern with aria-selected and roving tabindex', () => {
    act(() => {
      root.render(
        <SegmentedControl items={items} value="b" onValueChange={() => {}} ariaLabel="Views" />,
      )
    })
    const list = container.querySelector('[role="tablist"]')
    expect(list).not.toBeNull()
    expect(list!.getAttribute('aria-label')).toBe('Views')
    expect(list!.getAttribute('aria-orientation')).toBe('horizontal')
    expect(item('a').getAttribute('role')).toBe('tab')
    expect(item('a').getAttribute('aria-selected')).toBe('false')
    expect(item('a').getAttribute('tabindex')).toBe('-1')
    expect(item('b').getAttribute('aria-selected')).toBe('true')
    expect(item('b').getAttribute('tabindex')).toBe('0')
  })

  it('clicking a segment fires onValueChange with its value', () => {
    const onValueChange = vi.fn()
    act(() => {
      root.render(
        <SegmentedControl items={items} value="a" onValueChange={onValueChange} ariaLabel="Views" />,
      )
    })
    act(() => {
      item('c').click()
    })
    expect(onValueChange).toHaveBeenCalledTimes(1)
    expect(onValueChange).toHaveBeenCalledWith('c')
  })

  it('renders icons and verbatim labels', () => {
    act(() => {
      root.render(
        <SegmentedControl
          items={[
            { value: 'git', icon: <span data-icon />, label: 'files' },
            { value: 'raw', label: 'raw' },
          ]}
          value="git"
          onValueChange={() => {}}
          ariaLabel="Views"
        />,
      )
    })
    expect(item('git').querySelector('[data-icon]')).not.toBeNull()
    expect(item('git').textContent).toBe('files')
    expect(item('raw').textContent).toBe('raw')
  })

  it('exposes data-testid hooks and the title attribute', () => {
    act(() => {
      root.render(
        <SegmentedControl
          items={[{ value: 'x', label: 'X', testId: 'ctl-x', title: 'eXtended' }]}
          value="x"
          onValueChange={() => {}}
          ariaLabel="Views"
        />,
      )
    })
    expect(container.querySelector('[data-testid="ctl-x"]')).not.toBeNull()
    expect(item('x').getAttribute('title')).toBe('eXtended')
  })

  it('spreads itemProps onto the item button; the control invariants win over the spread', () => {
    const onValueChange = vi.fn()
    act(() => {
      root.render(
        <SegmentedControl
          items={[
            {
              value: 'x',
              label: 'X',
              itemProps: {
                id: 'x-tab',
                'aria-controls': 'x-panel',
                // Both are deliberate sabotage — the control's own contract
                // must override them (role/aria-selected for the tabs
                // semantic, disabled for the group-wide flag).
                role: 'radio',
                disabled: true,
              },
            },
          ]}
          value="x"
          onValueChange={onValueChange}
          ariaLabel="Views"
        />,
      )
    })
    const el = item('x')
    // Caller props reach the button: the ARIA tabs id wiring...
    expect(el.getAttribute('id')).toBe('x-tab')
    expect(el.getAttribute('aria-controls')).toBe('x-panel')
    // ...while the invariants survive the spread:
    expect(el.getAttribute('role')).toBe('tab')
    expect(el.getAttribute('aria-selected')).toBe('true')
    expect(el.disabled).toBe(false)
    act(() => {
      el.click()
    })
    expect(onValueChange).toHaveBeenCalledTimes(1)
    expect(onValueChange).toHaveBeenCalledWith('x')
  })

  it('ArrowRight/ArrowLeft advance and wrap the selection (controlled re-render between keys)', () => {
    const onValueChange = vi.fn()
    const render = (value: 'a' | 'b' | 'c') => {
      act(() => {
        root.render(
          <SegmentedControl items={items} value={value} onValueChange={onValueChange} ariaLabel="Views" />,
        )
      })
    }
    render('a')
    keydown('ArrowRight')
    expect(onValueChange).toHaveBeenCalledWith('b')
    render('c')
    keydown('ArrowRight')
    expect(onValueChange).toHaveBeenCalledWith('a')
    render('c')
    keydown('ArrowLeft')
    expect(onValueChange).toHaveBeenCalledWith('b')
  })

  it('ArrowRight moves focus to the new segment SYNCHRONOUSLY (no rAF deferral)', () => {
    // Regression: the focus hand-off used to sit inside requestAnimationFrame.
    // React can commit the controlled re-render AFTER the rAF callback runs,
    // so the callback focused a node still carrying tabIndex=-1 post-commit
    // and the focus was stripped — ejecting the keyboard user from the
    // control. The hand-off must be synchronous inside the key handler.
    const onValueChange = vi.fn()
    act(() => {
      root.render(
        <SegmentedControl items={items} value="a" onValueChange={onValueChange} ariaLabel="Views" />,
      )
    })
    // Establish a real focus start point inside the control.
    item('a').focus()
    expect(document.activeElement).toBe(item('a'))

    keydown('ArrowRight')
    // onValueChange fired; focus must have moved in the SAME task — before any
    // re-render/commit — with no rAF round-trip in between.
    expect(onValueChange).toHaveBeenCalledWith('b')
    expect(document.activeElement).toBe(item('b'))

    // The subsequent controlled commit promotes the new segment's tabindex.
    act(() => {
      root.render(
        <SegmentedControl items={items} value="b" onValueChange={onValueChange} ariaLabel="Views" />,
      )
    })
    expect(item('b').getAttribute('tabindex')).toBe('0')
    expect(document.activeElement).toBe(item('b'))
  })

  it('Home/End jump to first/last', () => {
    const onValueChange = vi.fn()
    act(() => {
      root.render(
        <SegmentedControl items={items} value="b" onValueChange={onValueChange} ariaLabel="Views" />,
      )
    })
    keydown('Home')
    expect(onValueChange).toHaveBeenCalledWith('a')
    keydown('End')
    expect(onValueChange).toHaveBeenCalledWith('c')
  })

  it('other keys do not fire onValueChange and are not swallowed', () => {
    const onValueChange = vi.fn()
    act(() => {
      root.render(
        <SegmentedControl items={items} value="a" onValueChange={onValueChange} ariaLabel="Views" />,
      )
    })
    const list = container.querySelector('[role="tablist"]')!
    const event = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    act(() => {
      list.dispatchEvent(event)
    })
    expect(onValueChange).not.toHaveBeenCalled()
    expect(event.defaultPrevented).toBe(false)
  })

  it('value=null keeps the group keyboard-reachable: the first item anchors the roving tabindex', () => {
    const onValueChange = vi.fn()
    act(() => {
      root.render(
        <SegmentedControl items={items} value={null} onValueChange={onValueChange} ariaLabel="Views" />,
      )
    })
    // No item is aria-selected, yet the FIRST item carries tabIndex=0 so the
    // tab order reaches the group; arrow keys from it select as usual.
    for (const v of ['a', 'b', 'c']) {
      expect(item(v).getAttribute('aria-selected')).toBe('false')
    }
    expect(item('a').getAttribute('tabindex')).toBe('0')
    expect(item('b').getAttribute('tabindex')).toBe('-1')
    expect(item('c').getAttribute('tabindex')).toBe('-1')
    keydown('ArrowRight')
    expect(onValueChange).toHaveBeenCalledWith('b')
  })

  it('an empty items list makes arrow keys a safe no-op (no NaN index, default not swallowed)', () => {
    const onValueChange = vi.fn()
    act(() => {
      root.render(
        <SegmentedControl items={[]} value={null} onValueChange={onValueChange} ariaLabel="Empty" />,
      )
    })
    expect(container.querySelector('[role="tablist"]')).not.toBeNull()
    const list = container.querySelector('[role="tablist"]')!
    const event = new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true })
    act(() => {
      list.dispatchEvent(event)
    })
    expect(onValueChange).not.toHaveBeenCalled()
    // The guard returns BEFORE preventDefault: an unmatched key must keep the
    // browser's default behavior, same as the unrecognized-key case above.
    expect(event.defaultPrevented).toBe(false)
  })

  it('a value matching no item keeps the group keyboard-reachable (first item anchors)', () => {
    const onValueChange = vi.fn()
    act(() => {
      root.render(
        <SegmentedControl items={items} value="ghost" onValueChange={onValueChange} ariaLabel="Views" />,
      )
    })
    for (const v of ['a', 'b', 'c']) {
      expect(item(v).getAttribute('aria-selected')).toBe('false')
    }
    expect(item('a').getAttribute('tabindex')).toBe('0')
    expect(item('b').getAttribute('tabindex')).toBe('-1')
    expect(item('c').getAttribute('tabindex')).toBe('-1')
    keydown('ArrowRight')
    // indexOf('ghost') is -1; the wrap-around arithmetic lands on index 0.
    expect(onValueChange).toHaveBeenCalledWith('a')
  })

  it('size sm → text-xs, md → text-sm font-medium; fullWidth stretches the track', () => {
    act(() => {
      root.render(
        <div>
          <SegmentedControl items={items} value="a" onValueChange={() => {}} ariaLabel="S" />
          <SegmentedControl items={items} value="a" onValueChange={() => {}} ariaLabel="M" size="md" />
          <SegmentedControl
            items={items}
            value="a"
            onValueChange={() => {}}
            ariaLabel="F"
            fullWidth
            data-testid="full"
          />
        </div>,
      )
    })
    const groups = container.querySelectorAll('[role="tablist"]')
    expect(groups[0]!.className).not.toContain('w-full')
    expect(groups[1]!.className).not.toContain('w-full')
    expect(item('a').className).toContain('text-xs')
    expect(item('a').className).toContain('h-5')
    const mdItem = groups[1]!.querySelector('[data-segment-value="a"]')!
    expect(mdItem.className).toContain('text-sm')
    expect(mdItem.className).toContain('font-medium')
    expect(mdItem.className).toContain('h-7')
    expect(groups[2]!.className).toContain('w-full')
  })
  it('disabled disables every item and blocks clicks', () => {
    const onValueChange = vi.fn()
    act(() => {
      root.render(
        <SegmentedControl items={items} value="a" onValueChange={onValueChange} ariaLabel="Views" disabled />,
      )
    })
    expect(item('b').disabled).toBe(true)
    act(() => {
      item('b').click()
    })
    expect(onValueChange).not.toHaveBeenCalled()
  })
})

describe('SegmentedControl — radio semantic', () => {
  it('renders radiogroup/radio with aria-checked instead of aria-selected', () => {
    act(() => {
      root.render(
        <SegmentedControl
          items={items}
          value="b"
          onValueChange={() => {}}
          semantic="radio"
          ariaLabel="Modes"
        />,
      )
    })
    const group = container.querySelector('[role="radiogroup"]')
    expect(group).not.toBeNull()
    expect(group!.getAttribute('aria-label')).toBe('Modes')
    expect(item('a').getAttribute('role')).toBe('radio')
    expect(item('a').getAttribute('aria-checked')).toBe('false')
    expect(item('b').getAttribute('aria-checked')).toBe('true')
    // radio items never carry aria-selected
    expect(item('b').hasAttribute('aria-selected')).toBe(false)
  })

  it('click and arrow keys behave the same (controlled re-render between keys)', () => {
    const onValueChange = vi.fn()
    const render = (value: 'a' | 'b' | 'c') => {
      act(() => {
        root.render(
          <SegmentedControl
            items={items}
            value={value}
            onValueChange={onValueChange}
            semantic="radio"
            ariaLabel="Modes"
          />,
        )
      })
    }
    render('a')
    act(() => {
      item('b').click()
    })
    expect(onValueChange).toHaveBeenCalledWith('b')
    render('b')
    keydown('ArrowRight')
    expect(onValueChange).toHaveBeenCalledWith('c')
  })
})
