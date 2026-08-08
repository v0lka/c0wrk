import { Volume2, VolumeX } from 'lucide-react'
import { useSoundStore } from '@/stores/soundStore'
import { Toggle } from './SmallLLMControls'
import { playSound, type SoundKind } from '@/lib/sound'

/**
 * General-tab control for sound notifications. A single master toggle governs
 * ALL cues (the user explicitly asked for one switch covering every event).
 * Re-enabling plays a short preview so the user can confirm it works.
 */
export function SoundSettings() {
  const enabled = useSoundStore((s) => s.enabled)
  const setEnabled = useSoundStore((s) => s.setEnabled)

  const handleChange = (next: boolean): void => {
    setEnabled(next)
    // When turning sound on, give immediate audible feedback (also serves as
    // the user gesture that unlocks the AudioContext on gesture-locked
    // webviews). Preview with the neutral cue — least startling.
    if (next) playSound('attention' as SoundKind)
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        {enabled ? (
          <Volume2 className="h-4 w-4 text-muted-foreground" />
        ) : (
          <VolumeX className="h-4 w-4 text-muted-foreground" />
        )}
        <span className="text-sm font-medium">Sound Notifications</span>
      </div>
      <Toggle
        checked={enabled}
        onChange={handleChange}
        label={enabled ? 'Enabled' : 'Disabled'}
        description="Play a tone when a task finishes, when your input is needed, or on errors. Sounds are generated locally and are identical on every OS."
      />
    </div>
  )
}
