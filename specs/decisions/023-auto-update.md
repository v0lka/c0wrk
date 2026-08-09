# ADR-023: Self-update (single-binary re-exec, SHA256-only, unsigned)

## Status

Accepted

## Context

c0wrk is shipped as three single-tree platform packages (a macOS `.app`
bundle, a Linux binary directory, and a Windows binary directory) published as
GitHub Release artifacts (`c0wrk-desktop-{macos-arm64.zip,linux-amd64.tar.gz,windows-amd64.zip}`)
alongside an auto-generated `SHA256SUMS` file (see
[`.github/workflows/release.yml`](../../.github/workflows/release.yml)). Until
now the only way to move to a newer release was to discover it manually,
download the archive, and replace the install tree by hand — friction that
keeps users on stale, potentially vulnerable builds.

A first-class self-update capability was added across
[`core/updater/`](../../core/updater/) (check / download / verify / stage /
swap), [`backend/frontend_api_updater.go`](../../backend/frontend_api_updater.go)
(frontend-callable API + typed events), and [`main.go`](../../main.go) (the
`--self-update` re-exec entry point). Self-update is a **supply-chain delivery
vector** (ASI04): the application downloads a binary it will execute as the
user and atomically swaps its own install tree. The design choices that shape
its risk must be recorded explicitly so the security posture is auditable.

### Forces

- **A binary cannot replace itself while it runs on most platforms.** On
  macOS/Windows the running executable is locked; an in-place overwrite either
  fails or corrupts the running process. A two-process re-exec dance is
  required.
- **GitHub Releases is the only distribution channel.** There is no separate
  CDN, package manager, or auto-updater service. Trust must therefore be
  anchored in GitHub's release authorship and TLS, not in an app-side signing
  key the project does not operate.
- **No code-signing infrastructure.** The macOS `.app` is **unsigned and
  unnotarized** (Gatekeeper friction is an accepted trade-off for a pre-1.0,
  single-maintainer project; see Consequences). Adding a signature/checksum
  *verification key* the app pins would create a key-management and
  rotation surface the project cannot yet sustain.
- **Integrity vs authenticity.** SHA256 detects *corruption* and *tamper that
  breaks the published checksum*, and pins the archive to the exact bytes the
  release pipeline produced. It does **not** detect an attacker who can publish
  a malicious archive *and* its matching checksum to the release (they control
  both halves). That class of attack is bounded by release-publishing
  permissions, not by the client.
- **Robustness on a desktop.** Updates can be interrupted (user quits, power
  loss), run from unsafe locations (Downloads, temp dirs, read-only mounts), or
  race a concurrent instance. Each must fail closed and leave a recoverable
  state.

## Decision

Adopt a **single-binary re-exec self-update model** with **SHA256-only
integrity verification**, **unsigned** release artifacts, **GitHub Releases**
as the trust anchor, and explicit **fallbacks** for interrupted/failed
updates. The model is accepted with the threat model below; no additional
app-side signature verification is added at this time.

### Architecture: the two-process re-exec dance

The running application never overwrites its own executable while executing
it:

1. **Prepare** — the app ([`core/updater/installer.go`](../../core/updater/installer.go)
   `PrepareSelfUpdate`) copies itself into a fresh staging directory and
   launches that *staging copy* with `--self-update --pid <PID> --stage <dir>
   --target <installdir>`, then initiates a **coordinated graceful quit**
   (`backend/frontend_api_updater.go` `ApplyUpdate` → `wailsRuntime.Quit`;
   shutdown hooks run, no `os.Exit`).
2. **Apply** — on the second invocation `main.go` detects `--self-update`
   **before the Wails lifecycle starts** and calls `ApplySelfUpdate`: it waits
   for the parent PID to exit, extracts the staged archive into a sibling dir,
   performs an atomic-ish swap (current tree → `<root>.old` backup, new tree →
   install root), relaunches the new app, and cleans up staging. The Wails loop
   never runs during an update.
3. **Cleanup** — on normal startup `main.go` calls `CleanupStaleUpdaters` to
   reap orphaned updater artifacts (the primary path on Windows, where a
   running `.exe` cannot self-delete).

The `.old` backup is retained after a successful swap (removed only at the
start of the *next* attempt) so a user can roll back manually if the new
version fails to start.

### Integrity: SHA256-only, fail-closed

- The release pipeline emits `SHA256SUMS` (`sha256sum *`) alongside the
  archives; the client fetches both over HTTPS and verifies the archive's
  digest against the per-platform entry before it is ever staged for apply
  ([`core/updater/verifier.go`](../../core/updater/verifier.go)).
- Comparison is constant-time (`crypto/subtle`); a **missing** per-platform
  entry or a **mismatch** is fail-closed: the archive is removed and an error
  returned — a partial/corrupt/tampered artifact is never left behind to be
  applied ([`core/updater/downloader.go`](../../core/updater/downloader.go)
  `Download`).
- Downloads are atomic at the file level (tmp + rename) so a crash or
  cancellation never leaves a half-written archive at the final path.

### macOS unsigned trade-off

The `.app` bundle is **not signed or notarized**. The updater swaps it via
`rename` and relaunches via `open`. Gatekeeper/quarantine friction on first
launch is an accepted trade-off (pre-1.0, single maintainer); it is **not** a
gap in the *update* mechanism — it is a property of the distribution as a
whole. See Consequences / Alternatives for the notarization path.

### Trust anchor: GitHub Releases + TLS

Asset and checksum URLs are HTTPS-only (the GitHub API scheme is hard-coded in
[`core/updater/checker.go`](../../core/updater/checker.go) so a misconfigured
proxy can never downgrade the check to plain HTTP). The HTTP client is
proxy-aware (`proxy.BuildClient`, supporting corporate proxies + custom CA
bundles). Trust that the downloaded bytes are the *released* bytes rests on:
GitHub account security (the release author), the repo's `contents: write`
permission boundary, and TLS protecting both the API call and the asset
download from network MITM.

### Fallbacks and recovery

- **Location safety** — `DiscoverInstallRoot` rejects temp dirs, Downloads
  folders, and read-only paths with `ErrNonStandardLocation`, so an update is
  never attempted against an install that should not be mutated in place.
- **Swap rollback** — `swapInstallTrees` moves the current tree to `.old`
  first; if the subsequent move of the new tree fails, it restores the backup
  (so the install tree is never left empty).
- **Relaunch failure is non-fatal to the swap** — if the relaunch fails the
  files are already correctly in place; the `.old` backup and staging
  artifacts are retained for manual recovery.
- **Archive safety** — extraction rejects zip-slip / tar-slip path traversal
  (`safeJoin`) and skips symlinks during update extraction.
- **Stale-updater reaping** — `CleanupStaleUpdaters` (called at normal
  startup) reaps orphaned updater copies from interrupted runs.

### Threat model

| Threat                                                                 | Class   | Mitigation                                                                                                                                                                  | Residual risk |
| ---------------------------------------------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------- |
| Compromised release publishes malicious archive **and** matching checksum | ASI04   | **None app-side** — SHA256 cannot detect an attacker who controls both halves. Bounded by release-publishing perms (`contents: write`) + GitHub account security (2FA).     | Accepted      |
| Network MITM alters archive or checksum in transit                     | ASI04   | HTTPS-only URLs (hard-coded scheme); proxy-aware client; TLS protects both the API and asset fetch. SHA256 detects any alteration that breaks the published checksum.        | Low           |
| Partial / corrupt / truncated download                                 | ASI04   | Atomic tmp+rename; fail-closed SHA256 verification; archive removed on mismatch — corrupt artifact never staged for apply.                                                  | Minimal       |
| Malicious archive entry escapes install dir during extraction          | ASI04   | `safeJoin` rejects `..`/absolute traversal (zip-slip / tar-slip); symlinks skipped during update extraction.                                                                 | Minimal       |
| Update applied against an unsafe install location                      | ASI04   | `ErrNonStandardLocation` rejects temp / Downloads / read-only roots before any swap.                                                                                        | Minimal       |
| Swap interrupted mid-way leaves an empty / corrupt install tree        | ASI08   | Two-step rename with `.old` backup; `swapInstallTrees` rolls back from `.old` if the new-tree move fails; relaunch failure keeps the backup.                                 | Low           |
| New version fails to start after a successful swap                     | ASI08   | `.old` backup retained through relaunch (removed only at the next attempt) for manual rollback.                                                                             | Low           |
| Orphaned staging/updater artifacts from an interrupted run             | —       | `CleanupStaleUpdaters` at normal startup reaps stale updater copies; staging dirs are per-attempt temp dirs.                                                                | Minimal       |
| Local hostile process swaps the staged archive before apply            | ASI04   | Staging lives under the per-user temp root; apply re-verifies against the downloaded SHA256SUMS only at download time (not re-read at apply). Defense is OS file perms.      | Accepted      |

## Consequences

**Positive:**

- Users can move to a fixed release in-app (check → download → apply) without
  manual archive juggling, reducing the population on stale builds.
- The two-process re-exec means the running app never overwrites its own
  executable; the swap is atomic-ish with a rollback path.
- SHA256 verification is fail-closed and constant-time; a corrupt or
  transit-altered archive is never applied.
- The `.old` backup + stale-updater reaping make interrupted updates
  recoverable rather than leaving a bricked install.
- Proxy-aware, HTTPS-only transport means corporate/proxied environments and
  custom CA bundles are honoured without weakening integrity.

**Negative / trade-offs:**

- **Unsigned artifacts.** The macOS `.app` is not signed/notarized; Gatekeeper
  quarantine friction persists on first launch. SHA256 ensures the bytes match
  the release but does **not** prove release authorship against an app-pinned
  key — a compromised GitHub release can ship a malicious build the client
  will happily verify and install. This is the central accepted trade-off.
- **Trust is outsourced to GitHub.** There is no app-side signature key, so
  the integrity guarantee collapses to "whatever GitHub Releases served, over
  TLS, matches the published SHA256". A GitHub-side or account compromise is
  not detectable by the updater.
- **Apply-time re-verification gap.** Integrity is checked at *download* time;
  the apply phase trusts the already-staged archive. A local process that can
  write the staging dir after download but before apply is not re-checked.
  Mitigated by OS file permissions on the per-user staging root; not
  eliminated.
- **Re-exec complexity.** The `--self-update` two-process dance + per-platform
  relaunch (`open` / detached `exec` / `cmd /c start`) is more moving parts
  than a single-process overwrite; the stale-updater cleanup and `.old`
  retention exist precisely to bound this complexity.
- **macOS Gatekeeper.** Unsigned updates may require the same first-launch
  bypass the initial install does; this is consistent with the initial install
  posture, not a regression introduced by the updater.

## Alternatives Considered

- **App-pinned Ed25519/cosign signature on each release (verify against a
  public key compiled into the binary).** Rejected for now: it would catch a
  release-channel compromise that posts a malicious archive + matching
  SHA256SUMS (the central gap above), but it introduces key management,
  rotation, and a signing step in the release pipeline that the project cannot
  yet sustain at pre-1.0. The hook remains: a future ADR can add signature
  verification by extending the `Verifier` interface
  ([`core/updater/verifier.go`](../../core/updater/verifier.go)), which was
  deliberately made an interface for exactly this evolution.
- **macOS notarization + Developer ID signing.** Rejected at this time: it
  removes Gatekeeper friction and is the long-term goal, but requires an Apple
  Developer Program membership and CI signing/notarization secrets the project
  does not have. The unsigned posture is a property of the *distribution*, not
  of the update mechanism; notarization would be layered on without changing
  the re-exec/SHA256 model.
- **In-place single-process self-overwrite.** Rejected: it is unreliable or
  impossible on macOS/Windows (locked/running executable) and risks corrupting
  the running process. The two-process re-exec is the robust general solution.
- **Package-manager distribution (Homebrew, apt/winget, auto-update service).**
  Rejected for the in-app path: it would outsource integrity and relaunch to a
  third party, fragment the update surface across platforms, and still require
  a fallback for users who installed the standalone archive. The GitHub
  Releases channel is the single existing distribution; the updater consumes
  it directly.
- **Download-then-prompt (apply requires a second user confirmation).**
  Partially adopted: the apply step is user-initiated (`ApplyUpdate`) and the
  app quits gracefully rather than force-exiting; auto-apply without consent is
  not implemented. Fully manual apply (no in-app automation) was rejected as it
  reintroduces the staleness friction this feature exists to remove.

## Related Specs

- [SECURITY.md](../../SECURITY.md) — Attack Surface (auto-update as a supply
  vector), Known Risks & Accepted Trade-offs, ASI04 (supply-chain integrity).
- [architecture/security-model.md](../architecture/security-model.md) — tool /
  download integrity, fail-closed checksums.
