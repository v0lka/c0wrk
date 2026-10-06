# Fonts

## Purpose

Enumerates the font families installed on the host system and exposes them to the frontend as the `ListFontFamilies` RPC — the data source for any family picker (e.g. a UI-font selector). The call takes a `monospace` flag: `false` lists every installed family, `true` narrows to the families fontconfig tags as monospace (`fc-list :mono`) — the monospace picker's data source. The enumeration is Linux-only via fontconfig's `fc-list`; every other platform, and any Linux system where the probe is unusable, reports "unavailable" instead of failing.

## Key Files

- `backend/fontlist.go` — platform-neutral `parseFontFamilies`: converts raw `fc-list --format '%{family}\n'` output into the deduplicated, case-insensitively sorted family list (comma-split aliases, trim, case-insensitive dedupe keeping the FIRST spelling, case-insensitive sort); never returns nil
- `backend/fontlist_linux.go` — Linux reader `listFontFamilies`: `exec.LookPath("fc-list")` + `fc-list [:mono] --format '%{family}\n'` (the optional leading `:mono` pattern is the `monospace` flag) under a 5 s timeout (`fontListTimeout`, generous because a first run may rebuild the fontconfig cache); argv is compile-time constants
- `backend/fontlist_other.go` — `!linux` stub: `listFontFamilies` always returns `errFontListUnavailable`
- `backend/frontend_api_system.go` — the `ListFontFamilies(monospace bool)` RPC (`FontFamiliesResponse{Available, Families}`) and its seam dispatch; every "unavailable" outcome is the zero response, never an error
- `backend/frontend_api.go` — the `listFontFamiliesFn` test seam field on `FrontendAPI` (nil in production → the platform reader runs), mirroring `readSystemFontsFn`
- `backend/fontlist_test.go` — parser table (aliases / duplicates / empty output / sorting), RPC tests through the stubbed seam, live-fc-list test (skips when absent)

## Core Types

```go
// backend/frontend_api_system.go
type FontFamiliesResponse struct {
    Available bool     `json:"available"` // enumeration mechanism usable
    Families  []string `json:"families"`  // sorted, deduplicated; empty when !Available
}
```

## Flow

```
frontend (font picker)
   │  ListFontFamilies(monospace)
   ▼
FrontendAPI.ListFontFamilies ──seam──► listFontFamiliesFn (tests) / listFontFamilies (prod)
                                          │ linux: fc-list [:mono] --format '%{family}\n' (5 s bound)
                                          │ !linux: errFontListUnavailable
                                          ▼
                                     parseFontFamilies (fontlist.go)
                                          │ split ',' aliases → trim → dedupe (case-insensitive,
                                          │ keep-first) → sort (case-insensitive)
                                          ▼
                         FontFamiliesResponse{Available: true, Families: [...]}
                                     err → {Available: false, Families: nil} + Debug log
```

## Invariants

- `ListFontFamilies` never fails to the renderer: every reader error (non-Linux build, fc-list missing, probe failure, timeout) is the zero response `{available: false, families: []}` plus a Debug log.
- `Available` describes the MECHANISM, not the result: a successful listing on a fontless system is `Available=true` with an empty list.
- `Families` is never nil on the wire — a successful response always carries a real (possibly empty) slice, serialized as `[]`.
- Family names are deduplicated case-insensitively with the first spelling kept, and sorted case-insensitively; after the dedup no two names collide under the sort comparison, so the output order is deterministic.
- Nothing user- or model-controlled ever reaches the `fc-list` argv — the command and arguments are compile-time constants.
- `monospace=true` narrows the listing through fontconfig's `:mono` pattern (spacing=mono families): the narrowed list is a subset of the full listing — the monospace picker is a filtered view of the same installed-family space (pinned by the live-fc-list test, which asserts the subset invariant).
- The probe is bounded: `fontListTimeout` (5 s) caps the child process; a wedged fontconfig cache rebuild can never hang the RPC.
- The parser (`parseFontFamilies`) is platform-neutral pure string work, so its tests run on every build target.

## Configuration

None — the subsystem has no config.yaml surface. The probe binary (`fc-list`) is discovered from `PATH` at call time; its absence is the normal `Available=false` outcome, not an installation error.

## Extension Points

- **Another platform reader** (e.g. macOS/Windows font enumeration): add a build-tagged `listFontFamilies` following `backend/fontlist_linux.go` (argv as compile-time constants, bounded timeout, error = "unavailable"); the RPC, seam and parser stay untouched.
- **A font picker consumer**: the frontend wires its own RPC wrapper (`frontend/src/api/*`) and store; the backend surface is complete and read-only.

## Related Specs

- [domains/frontend/fonts.md](frontend/fonts.md) — the user font-selection feature built on this enumeration (`ListFontFamilies` as the picker's data source) plus the sibling desktop-font detection RPC (`GetSystemFonts`): reads what the desktop environment configured via gsettings; different question (what the DE configured) than this spec (what is installed)
- [contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — the RPC surface `ListFontFamilies` is cataloged in (System section)
