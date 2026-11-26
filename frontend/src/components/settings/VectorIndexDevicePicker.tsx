import { Combobox } from '@/components/ui/combobox'
import { Input } from '@/components/ui/input'
import type { GPUDeviceResponse } from '@/types/models'

interface VectorIndexDevicePickerProps {
  gpus: readonly GPUDeviceResponse[]
  gpusLoaded: boolean
  disabled: boolean
  /**
   * When set, the control is pinned (disabled regardless of `disabled`)
   * and this exact reason replaces the note text — used for providers
   * where the device index has exactly one valid value (OpenVINO: 0).
   */
  pinnedReason?: string
  /**
   * The execution provider the saved device id was written for ("auto",
   * "cuda", "openvino"). Disambiguates the selected entry when several
   * driver scopes report the same index (CUDA 0 + OpenVINO 0 on a hybrid
   * machine): under "auto" the index addresses CUDA ordinals, under
   * "openvino" it is always the OpenVINO entry.
   */
  provider?: string
  value: number
  input: string
  error: string | null
  probeError: string | null
  onChange: (value: string) => void
}

/**
 * Option values are provider-scoped ("cuda:0", "openvino:0") so two driver
 * scopes reporting the same ONNX ordinal stay distinct combobox entries and
 * distinct React keys; provider-less entries (legacy backend) keep the bare
 * index. `decodeOptionValue` recovers the bare ordinal for the onChange
 * contract — the config's device_id is a plain number scoped by the
 * provider, never the composite.
 */
function optionValue(gpu: GPUDeviceResponse): string {
  return gpu.provider !== undefined ? `${gpu.provider}:${gpu.index}` : String(gpu.index)
}

function decodeOptionValue(v: string): string {
  const sep = v.indexOf(':')
  return sep === -1 ? v : v.slice(sep + 1)
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
  pinnedReason,
  provider,
  value,
  input,
  error,
  probeError,
  onChange,
}: VectorIndexDevicePickerProps) {
  const pinned = pinnedReason !== undefined
  const controlDisabled = disabled || pinned
  const showDeviceDropdown = gpus.length > 0
  const options = gpus.map((gpu) => ({
    value: optionValue(gpu),
    label:
      gpu.provider !== undefined
        ? `${gpu.index} — ${gpu.name} (${gpu.provider.toUpperCase()})`
        : `${gpu.index} — ${gpu.name}`,
  }))

  // The saved device id is a bare ordinal; when several scopes report the
  // same index, prefer the entry written for the saved provider (an exact
  // "cuda:N" match under auto/cuda, "openvino:0" under openvino), then any
  // entry with the ordinal, then the raw ordinal (legacy shape).
  const scopedValue = provider !== undefined ? `${provider}:${value}` : null
  const selectedValue =
    (scopedValue !== null && options.some((o) => o.value === scopedValue)
      ? scopedValue
      : options.find((o) => decodeOptionValue(o.value) === String(value))?.value) ??
    String(value)

  return (
    <div className="flex flex-col gap-2">
      <label className="text-xs text-muted-foreground">GPU Device</label>
      {!gpusLoaded && !controlDisabled ? (
        <div className="flex h-9 items-center rounded-md border border-input bg-background px-3 text-sm text-muted-foreground">
          Detecting GPUs...
        </div>
      ) : showDeviceDropdown ? (
        <Combobox
          ariaLabel="Embedder GPU device"
          value={selectedValue}
          onChange={(v) => onChange(decodeOptionValue(v))}
          className="min-w-[180px]"
          options={options}
          disabled={controlDisabled}
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
          disabled={controlDisabled}
          aria-invalid={error !== null || undefined}
          className="h-9 text-sm"
        />
      )}
      <p className="text-xs text-muted-foreground">
        {pinned
          ? pinnedReason
          : disabled
            ? 'Device applies only when the provider resolves to a GPU; CPU ignores it.'
            : !gpusLoaded
              ? 'Probing the drivers for visible GPUs...'
              : showDeviceDropdown
                ? 'Driver-reported NVIDIA and Intel GPUs. The index is the ONNX device id used by the config, scoped to the execution provider.'
                : 'No GPUs reported — enter the ONNX device index directly (0 = first GPU).'}
      </p>
      {probeError && !showDeviceDropdown && <p className="text-xs text-warning">{probeError}</p>}
      {error && !disabled && (
        <p className="text-xs text-destructive" role="alert">
          {error}
        </p>
      )}
    </div>
  )
}
