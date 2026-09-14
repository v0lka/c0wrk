# ADR-045: Cheap status-dot animation for WebKitGTK software rendering

## Status

Accepted

## Context

On Linux, c0wrk's webview runs under WebKitGTK in **software rendering mode**
(Wails' default `WebviewGpuPolicyNever` — the shipped blank-webview safeguard
against wails#2977). In this mode every animated pixel is composited on the
CPU by `WebKitWebProcess`. The stock Tailwind `animate-ping` used by the chat
activity dot ("Thinking…", "Waiting for answer…") animates `transform: scale`
in an infinite loop, which forces the renderer to re-rasterize the element
every frame. Users measured `WebKitWebProcess` burning a full CPU core
whenever a session was active and the window visible — on a laptop this is
unacceptable. The same stock ping was used by the index-status dots in the
status bar.

Field testing (NVIDIA X11 desktop + AMD iGPU laptop, identical symptoms)
confirmed the load is animation-driven: minimizing the window or ending the
session drops it to zero.

Two remediation paths were evaluated:

1. Enable GPU compositing via `options.Linux.WebviewGpuPolicy` (env-gated
   through `C0WRK_WEBVIEW_GPU=never|always|on-demand`). **Rejected by field
   test**: `always` and `on-demand` both produced a permanently blank window
   on the test machines (the wails#2977 class of bugs), making the app
   unusable. The experiment was fully reverted; Wails' software-only default
   stays.
2. Make the animation itself cheap enough for software rendering.

## Decision

Status indicators (chat activity dot, index-build dots) use a plain
**opacity blink** — the stock Tailwind `animate-pulse` utility applied
directly to the dot/square element, the exact same animation the adjacent
status label text already uses:

- `frontend/src/components/chat/ActivityIndicator.tsx` — the thinking/waiting
  dot at the bottom of the chat area
- `frontend/src/components/layout/IndexingStatus.tsx` — the vector/lexical
  build dots in the status bar (pulse applied only in the `active` state)

No custom theme token is introduced; `animate-pulse` is used as-is.

**Why opacity-only is cheap**: opacity is a compositor property. WebKitGTK
can service the animation by alpha-blending the tiny pre-rasterized dot
without re-rasterizing the element or invalidating layout, keeping the
per-frame cost near zero even in software mode (measured ~0% CPU, down from
a full core).

**Scope guard**: `animate-spin` loaders remain transform-based. They are
short-lived, user-initiated, and co-located with real work (button presses,
fetches); the infinite idle-time animations that burned a core are exactly
the ones this ADR replaces. New long-lived indicators must use an
opacity-only pulse (`animate-pulse` or a custom opacity keyframe), never
`animate-ping`/`animate-spin`.

## Consequences

- A visible session no longer burns a CPU core on Linux laptops in software
  rendering mode; battery life during long agent runs is preserved.
- The activity cue is a gentle blink, visually consistent with the label
  text pulsing beside it (previously an expanding ping ring).
- Zero new CSS surface: the stock utility applies everywhere Tailwind does.
- GPU compositing remains disabled (Wails default). If WebKitGTK's blank
  -window bug class is ever fixed for this stack, revisit via a new ADR that
  supersedes this one.

## Alternatives Considered

- **`WebviewGpuPolicy` on-demand/always** — blank window on both test
  machines (wails#2977); unusable. Reverted.
- **Breathing halo (static halo ring twice the dot's size, opacity pulse on
  the halo layer)** — field-tested as the second iteration: CPU stayed at
  ~0%, but the soft halo around a solid dot read poorly ("looks bad"),
  especially next to the crisp blinking label text. Superseded by the plain
  blink for visual consistency.
- **Pure opacity pulse of the dot itself (first iteration)** — CPU ~0%, but
  the initial implementation pulsed an identically-sized layer underneath a
  fully opaque dot, so the blink was invisible and the dot looked static.
  The final design applies the pulse to the visible dot itself, making it a
  true blink.
- **Removing the animation entirely** — zero CPU but loses the "work in
  progress" cue that distinguishes an active session from a hung one.
- **JS-driven `setInterval` animation** — strictly worse: same repaints plus
  main-thread timer wakeups and re-render risk.
