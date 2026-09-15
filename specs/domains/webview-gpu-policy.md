# Linux Webview GPU Policy

## Purpose

Sets the Linux webview's (WebKitGTK) hardware-acceleration policy explicitly instead of relying on Wails' nil-options default, and exposes it as an environment override (`C0WRK_WEBVIEW_GPU_POLICY`). The conversion into Wails' enum deliberately compensates for a Go-enum/C-layer mismatch in the pinned Wails v2.15.0, so the policy named on the environment variable is the one WebKit actually receives.

## Key Files

- `desktop/webview_gpu_policy.go` — the whole subsystem: `WebviewGpuPolicyEnvVar`, `webviewGpuPolicy` enum, `parseWebviewGpuPolicy`, `wailsWebviewGpuPolicy` (the compensation mapping), `WebviewGpuPolicyOptions` / `webviewGpuPolicyOptions`
- `desktop/webview_gpu_policy_test.go` — pins the compensation by C-layer numeric value plus an enum-order canary (see Invariants)
- `main.go` — the only production call site: `Linux: desktop.WebviewGpuPolicyOptions(slog.Default())` in the `wails.Run` options

## Core Types

```go
// desktop/webview_gpu_policy.go — internal policy, deliberately decoupled
// from Wails' enum ordering.
type webviewGpuPolicy int

const (
    gpuPolicyNever    webviewGpuPolicy = iota // default
    gpuPolicyAlways
    gpuPolicyOnDemand
)

// wailsWebviewGpuPolicy converts to the value handed to Wails.
// v2.15.0 mapping (desired → Go enum → C switch in window.c → WebKit):
//   always    → WebviewGpuPolicyOnDemand (0) → case 0 → POLICY_ALWAYS
//   on-demand → WebviewGpuPolicyAlways  (1)  → case 1 → POLICY_ON_DEMAND
//   never     → WebviewGpuPolicyNever   (2)  → case 2 → POLICY_NEVER
```

## Flow

```
C0WRK_WEBVIEW_GPU_POLICY (env)
        │
        ▼
WebviewGpuPolicyOptions(logger)               [main.go, wails.Run options]
        │  goos ≠ linux → nil (macOS/Windows untouched; Debug note if set)
        │  empty/whitespace → explicit WebviewGpuPolicyNever (same policy
        │      Wails' nil-options workaround applies, made deliberate)
        │  unparseable → Warn + explicit never
        ▼
parseWebviewGpuPolicy   always | on-demand | ondemand | never
        │  (case-insensitive, whitespace-tolerant)
        ▼
wailsWebviewGpuPolicy  ← the compensation mapping (see Core Types)
        ▼
linux.Options.WebviewGpuPolicy → wails.Run → SetupWebview(window.c switch) → WebKit
```

## Invariants

- The env var accepts exactly `always`, `on-demand`, `ondemand` (alias), `never` — case-insensitive, whitespace-tolerant; every other value (including empty) resolves to `never` with a Warn log.
- The default is `never`, matching the workaround Wails applies for [wails#2977](https://github.com/wailsapp/wails/issues/2977) when no Linux options are passed; passing the options explicitly pins that behavior against a future Wails default change.
- `wailsWebviewGpuPolicy` is the single place aware of the v2.15.0 Go-enum/C-layer mismatch (`linux.go`: OnDemand=0, Always=1, Never=2 vs `window.c` `SetupWebview`: 0→ALWAYS, 1→ON_DEMAND, 2→NEVER); like-named constants are deliberately NOT used there, and the mapping yields the C-layer integer that selects the WebKit policy matching the env-var name.
- Tests pin the compensation by C-layer numeric value (`0/1/2`), and an enum-order canary (`TestWailsWebviewGpuPolicyEnumOrderCanary`) pins the Go enum's numeric order — so a Wails upgrade that reorders either side fails the desktop test suite loudly instead of silently swapping `always`/`on-demand` at runtime.
- macOS and Windows receive nil Linux options — the override affects the WebKitGTK frontend only.
- On every Wails upgrade, `window.c` (`SetupWebview`) and `pkg/options/linux/linux.go` are re-verified; if upstream aligns the C switch with the Go enum, the two non-default arms of `wailsWebviewGpuPolicy` are swapped back or dropped and the numeric pins updated to match.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `C0WRK_WEBVIEW_GPU_POLICY` | unset → `never` | `always` \| `on-demand` (alias `ondemand`) \| `never`. Linux-only; unset and invalid values fall back to `never` (invalid logs a Warn). |

## Extension Points

- New policy values: extend `parseWebviewGpuPolicy` and `webviewGpuPolicy.String()` together with the `wailsWebviewGpuPolicy` mapping and the numeric pins in the test; every accepted value maps to a WebKit policy integer.
- Platform-scoped variants (e.g. a macOS equivalent): keep `webviewGpuPolicyOptions`'s `goos` switch fail-closed — unknown platforms return nil options.

## Related Specs

- [crash-logging.md](crash-logging.md) - the sibling env-var escape hatch pattern (`C0WRK_DISABLE_CRASH_CAPTURE`), also Linux-shell ergonomics
- [018-macos-webview-recovery.md](../decisions/018-macos-webview-recovery.md) - the other platform-specific webview workaround, decision-record form
