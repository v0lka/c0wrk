import { Combobox } from '@/components/ui/combobox'
import { Input } from '@/components/ui/input'
import type { GPUDeviceResponse } from '@/types/models'

interface VectorIndexDevicePickerProps {
  gpus: readonly GPUDeviceResponse[]
  gpusLoaded: boolean
  disabled: boolean
  value: number
  input: string
  error: string | null
  probeError: string | null
  onChange: (value: string) => void
}

/**
 * GPU device control for the vector-index settings: a named dropdown when
 * the driver reports GPUs, a validating numeric input when it doesn't (or
 * the probe failed). While the probe is in flight (bounded 2s) a muted
 * placeholder is shown so the control doesn't flash between shapes.
 */
export function VectorIndexDevicePicker({
  gpus,
  gpusLoaded,
  disabled,
  value,
  input,
  error,
  probeError,
  onChange,
}: VectorIndexDevicePickerProps) {
  const useDropdown = gpus.length > 0
  const options = gpus.map((gpu) => ({
    value: String(gpu.index),
    label: `${gpu.index} — ${gpu.name}`,
  }))

  return (
    <div className="flex flex-col gap-2">
      <label className="text-xs text-muted-foreground">GPU Device</label>
      {!gpusLoaded && !disabled ? (
        <div className="flex h-9 items-center rounded-md border border-input bg-background px-3 text-sm text-muted-foreground">
          Detecting GPUs...
        </div>
      ) : useDropdown ? (
        <Combobox
          ariaLabel="Embedder GPU device"
          value={String(value)}
          onChange={onChange}
          className="min-w-[180px]"
          options={options}
          disabled={disabled}
        />
      ) : (
        <Input
          type="number"
          min={0}
          step={1}
          aria-label="Embedder GPU device"
          placeholder="0"
          value={input}
          onChange={(e) => onChange(e.target.value)}
          disabled={disabled}
          aria-invalid={error !== null || undefined}
          className="h-9 text-sm"
        />
      )}
      <p className="text-xs text-muted-foreground">
        {disabled
          ? 'Device applies only when the provider resolves to CUDA; CPU ignores it.'
          : !gpusLoaded
            ? 'Probing the NVIDIA driver for visible GPUs...'
            : useDropdown
              ? 'Driver-reported NVIDIA GPUs. The index is the ONNX device id used by the config.'
              : 'No NVIDIA GPUs reported — enter the ONNX device index directly (0 = first GPU).'}
      </p>
      {probeError && !useDropdown && <p className="text-xs text-warning">{probeError}</p>}
      {error && !disabled && (
        <p className="text-xs text-destructive" role="alert">
          {error}
        </p>
      )}
    </div>
  )
}
