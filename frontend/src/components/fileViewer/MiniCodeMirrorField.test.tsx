// Unit tests for MiniCodeMirrorField's callback/props contract.
//
// The CodeMirror view is created exactly once per mount, which historically
// captured the mount-time `onChange` inside the update listener: a caller
// whose handler closes over mutable state (e.g. an editable draft object)
// had every later keystroke write that STALE mount-time state back. These
// tests pin the fix — the listener must always invoke the latest onChange —
// plus the external value-sync effect.
//
// jsdom environment: mounts a real CodeMirror EditorView (geometry polyfills
// are installed globally in src/test/setup.ts). Doc changes are driven with
// programmatic transactions, which fire the same updateListener path as
// user typing.
// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { EditorView } from '@codemirror/view'

import { MiniCodeMirrorField } from './MiniCodeMirrorField'
import { useFontStore } from '@/stores/fontStore'

let activeRoot: Root | null = null

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
  document.body.replaceChildren()
  vi.clearAllMocks()
})

async function renderField(props: {
  value: string
  onChange: (value: string) => void
}): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(<MiniCodeMirrorField {...props} />)
  })
  return container
}

/** Grab the live EditorView rendered inside the field's container. */
function viewOf(container: HTMLElement): EditorView {
  const view = EditorView.findFromDOM(container)
  if (!view) throw new Error('EditorView not found in container')
  return view
}

/** Append text as a docChanged transaction (fires the updateListener). */
function typeIn(view: EditorView, text: string) {
  act(() => {
    view.dispatch({
      changes: { from: view.state.doc.length, insert: text },
    })
  })
}

describe('MiniCodeMirrorField — onChange freshness', () => {
  it('invokes the LATEST onChange, never the mount-time closure', async () => {
    const mountTime = vi.fn()
    const later = vi.fn()
    const container = await renderField({ value: 'a', onChange: mountTime })

    const view = viewOf(container)
    typeIn(view, 'b')
    expect(mountTime).toHaveBeenCalledWith('ab')

    // Re-render with a NEW onChange closure — what a parent editing other
    // draft fields does on every keystroke. The mount-time callback must
    // never be invoked again.
    await act(async () => {
      ;(activeRoot as Root).render(
        <MiniCodeMirrorField value="ab" onChange={later} />,
      )
    })

    typeIn(view, 'c')
    expect(later).toHaveBeenCalledWith('abc')
    expect(mountTime).toHaveBeenCalledTimes(1)
  })

  it('reports the full document text, not just the inserted part', async () => {
    const onChange = vi.fn()
    const container = await renderField({ value: 'head', onChange })

    typeIn(viewOf(container), '-tail')
    expect(onChange).toHaveBeenCalledWith('head-tail')
  })
})

describe('MiniCodeMirrorField — external value sync', () => {
  it('replaces the document when the value prop changes externally', async () => {
    const onChange = vi.fn()
    const container = await renderField({ value: 'old', onChange })

    await act(async () => {
      ;(activeRoot as Root).render(
        <MiniCodeMirrorField value="replaced" onChange={onChange} />,
      )
    })

    expect(viewOf(container).state.doc.toString()).toBe('replaced')
  })
})

describe('MiniCodeMirrorField — font-driven theme reconfigure', () => {
  it('reconfigures the view when the mono font changes', async () => {
    const container = await renderField({ value: 'x', onChange: vi.fn() })
    expect(viewOf(container)).toBeDefined()

    // The theme's font rides the --font-mono var, but CodeMirror measures
    // line heights lazily: the component must dispatch a compartment
    // reconfigure when the choice changes, or the new font renders on
    // stale measurements. Nothing else dispatches after mount here, so the
    // store flip must cause exactly one dispatch. NB: spy on the INSTANCE —
    // the constructor binds dispatch as an own property, so a prototype spy
    // never sees the call.
    const dispatchSpy = vi.spyOn(viewOf(container), 'dispatch')
    try {
      await act(async () => {
        useFontStore.getState().setMonoFontFamily('JetBrains Mono')
      })

      expect(dispatchSpy).toHaveBeenCalledTimes(1)
      const spec = dispatchSpy.mock.calls[0]?.[0]
      expect(spec !== undefined && 'effects' in spec && spec.effects !== undefined).toBe(true)
    } finally {
      dispatchSpy.mockRestore()
      // Restore the store and the inline var the action wrote to <html>,
      // so neither leaks into other tests in this file.
      await act(async () => {
        useFontStore.setState({ monoFontFamily: null })
        document.documentElement.style.removeProperty('--font-mono')
      })
    }
  })
})
