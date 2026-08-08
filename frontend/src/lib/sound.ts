// Programmatic sound notifications via the Web Audio API.
//
// Sounds are synthesized at runtime from oscillators — no audio files — so they
// are byte-identical on every platform (the Wails webview — WKWebView on macOS,
// WebView2/Edge on Windows, WebKitGTK on Linux — implements the same Web Audio
// spec). This satisfies the "one identical sound across all three OSes"
// requirement without shipping per-platform assets.

import { useSoundStore } from '@/stores/soundStore'
import { logger } from '@/lib/logger'

/** The three notification categories. */
export type SoundKind = 'success' | 'attention' | 'error'

/** Constructor type for AudioContext (covers the standard API). */
type AudioContextCtor = new (contextOptions?: AudioContextOptions) => AudioContext

/** Lazily-created, reused AudioContext. Module-scoped so every tone shares one
 *  graph + thread pool. Stays null in non-browser (test) environments. */
let audioCtx: AudioContext | null = null
/** True once the first user-gesture unlock listener has been registered, so we
 *  never attach duplicate listeners. */
let unlockRegistered = false

/** Resolve the AudioContext constructor (standard + legacy webkit prefix). */
function getAudioContextCtor(): AudioContextCtor | null {
  if (typeof window === 'undefined') return null
  const w = window as unknown as {
    AudioContext?: AudioContextCtor
    webkitAudioContext?: AudioContextCtor
  }
  return w.AudioContext ?? w.webkitAudioContext ?? null
}

/** Lazily create (or return the cached) AudioContext. Returns null when the
 *  Web Audio API is unavailable (older webview / tests). */
function getCtx(): AudioContext | null {
  if (typeof window === 'undefined') return null
  if (audioCtx) return audioCtx
  const Ctor = getAudioContextCtor()
  if (!Ctor) return null
  try {
    audioCtx = new Ctor()
  } catch (err) {
    logger.warn('[sound] failed to create AudioContext', err)
    return null
  }
  return audioCtx
}

/** A single oscillator note within a multi-note cue. Times are relative to the
 *  start of the cue (seconds). */
interface NoteSpec {
  freq: number
  /** Offset from cue start, in seconds. */
  start: number
  /** Duration of the note, in seconds. */
  duration: number
  type: OscillatorType
  /** Peak gain (0..1). Kept conservative so cues never startle. */
  gain: number
}

function playNote(ctx: AudioContext, spec: NoteSpec): void {
  const osc = ctx.createOscillator()
  const gain = ctx.createGain()
  osc.type = spec.type
  osc.frequency.value = spec.freq

  const t0 = ctx.currentTime + spec.start
  const attack = Math.min(0.012, spec.duration * 0.2)
  // Attack → exponential decay envelope for a soft, natural tail.
  gain.gain.setValueAtTime(0.0001, t0)
  gain.gain.exponentialRampToValueAtTime(spec.gain, t0 + attack)
  gain.gain.exponentialRampToValueAtTime(0.0001, t0 + spec.duration)

  osc.connect(gain).connect(ctx.destination)
  osc.start(t0)
  osc.stop(t0 + spec.duration + 0.02)
}

// --- Tone presets -----------------------------------------------------------
// Equal-temperament frequencies (Hz):
//   C5 523.251  E5 659.255  G5 783.991  (major triad → positive)
//   A4 440.000                              (single neutral chime)
//   A4 440.000  A3 220.000                   (descending → alarming)

const SUCCESS_NOTES: NoteSpec[] = [
  { freq: 523.251, start: 0.0, duration: 0.14, type: 'sine', gain: 0.18 },
  { freq: 659.255, start: 0.1, duration: 0.14, type: 'sine', gain: 0.18 },
  { freq: 783.991, start: 0.2, duration: 0.26, type: 'sine', gain: 0.2 },
]

const ATTENTION_NOTES: NoteSpec[] = [
  { freq: 440.0, start: 0.0, duration: 0.3, type: 'sine', gain: 0.16 },
]

const ERROR_NOTES: NoteSpec[] = [
  { freq: 440.0, start: 0.0, duration: 0.18, type: 'triangle', gain: 0.22 },
  { freq: 220.0, start: 0.16, duration: 0.34, type: 'triangle', gain: 0.24 },
]

const PRESETS: Record<SoundKind, NoteSpec[]> = {
  success: SUCCESS_NOTES,
  attention: ATTENTION_NOTES,
  error: ERROR_NOTES,
}

/**
 * Play a notification cue. A no-op when the master toggle is off, when the Web
 * Audio API is unavailable, or when resuming a suspended context fails (sound
 * is best-effort: a silent cue must never break the task UI).
 */
export function playSound(kind: SoundKind): void {
  if (!useSoundStore.getState().enabled) return
  const ctx = getCtx()
  if (!ctx) return
  if (ctx.state === 'suspended') {
    void ctx.resume().catch((err) => {
      logger.debug('[sound] context resume failed', err)
    })
  }
  for (const note of PRESETS[kind]) playNote(ctx, note)
}

/**
 * Unlock audio on the first user gesture.
 *
 * Desktop webviews (notably macOS WKWebView) start the AudioContext suspended
 * until a user gesture occurs. Notification cues are fired by backend events,
 * not gestures, so the context can stay locked and the first cues play
 * silently. Registering one-shot pointer/keyboard/touch listeners that create
 * + resume the context on the user's first interaction guarantees subsequent
 * event-driven cues are audible. Idempotent — safe to call repeatedly.
 */
export function initSoundUnlock(): void {
  if (typeof window === 'undefined' || unlockRegistered) return
  unlockRegistered = true
  const unlock = (): void => {
    const ctx = getCtx()
    if (ctx && ctx.state === 'suspended') {
      void ctx.resume().catch(() => { /* best-effort */ })
    }
  }
  window.addEventListener('pointerdown', unlock, { once: true })
  window.addEventListener('keydown', unlock, { once: true })
  window.addEventListener('touchstart', unlock, { once: true })
}

/** Test-only: reset module state so unit tests start from a clean slate. */
export function __resetSoundModule(): void {
  audioCtx = null
  unlockRegistered = false
}
