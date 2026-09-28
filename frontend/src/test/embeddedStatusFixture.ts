// Shared `EmbeddedLLMStatus` fixture for tests.
//
// The snapshot has ~30 required fields (the legacy lifecycle block plus the
// measured-topology / effective-plan extras), and every embedded-LLM surface
// test needs a complete one. Keeping the shape here means a DTO addition is
// fixed in one place instead of in six suites.
//
// The defaults describe an INSTALL-BUT-STOPPED model on a not-installed machine
// (state "not_installed", installed false) — the least interesting snapshot, so
// each test states only the fields it actually asserts on.

import type { EmbeddedLLMStatus } from '@/api/embedded'

/** A complete status snapshot with every field at its inert default. */
export function makeEmbeddedStatus(
  overrides: Partial<EmbeddedLLMStatus> = {},
): EmbeddedLLMStatus {
  return {
    state: 'not_installed',
    installed: false,
    installing: false,
    loading: false,
    loaded: false,
    packing: '',
    backend: '',
    port: 0,
    context_size: 0,
    auto_unload_enabled: false,
    auto_unload_minutes: 60,
    idle_remaining_seconds: 0,
    base_url: '',
    model_id: 'embedded/Bonsai 2 27B',
    model_name: 'Bonsai 2 27B',
    runtime_version: '',
    installed_at: '',
    model_file: '',
    pid: 0,
    error: '',
    install_error: '',
    available: true,
    devices: [],
    unified: false,
    host_ram_gib: 0,
    device_budget_mib: 0,
    host_budget_mib: 0,
    topology_probed_at: '',
    plan: {
      recorded: false,
      packing: '',
      kv_type: '',
      context_size: 0,
      fit: false,
      fit_arg: '',
      fit_target_mib: 0,
      fit_min_context: 0,
      offload_mode: 'auto',
      layers: -1,
      kv_offload: false,
      mmproj_offload: false,
      parallel: 0,
      cache_ram_mib: -1,
      gpu_family: '',
      device_budget_mib: 0,
      host_budget_mib: 0,
      expected_device_mib: 0,
      expected_host_mib: 0,
      notes: [],
    },
    reload_required: false,
    ...overrides,
  }
}

/** The fixture as an installed-but-stopped (cold) model — the state a service
 *  call would have to wait on. */
export function makeColdEmbeddedStatus(
  overrides: Partial<EmbeddedLLMStatus> = {},
): EmbeddedLLMStatus {
  return makeEmbeddedStatus({
    state: 'installed',
    installed: true,
    packing: 'PQ2_0',
    backend: 'metal',
    port: 43211,
    context_size: 131072,
    base_url: 'http://127.0.0.1:43211/v1',
    runtime_version: 'prism-b10735',
    installed_at: '2026-01-01T00:00:00Z',
    model_file: '/x.gguf',
    ...overrides,
  })
}
