/**
 * Host-platform detection, shared across the app.
 *
 * `navigator.platform` is the primary signal (WebKit composes it from
 * uname(): "Linux x86_64" on amd64, "Linux aarch64" on arm64 — a
 * first-class release platform, ADR-027), with `navigator.userAgent` as
 * the fallback for engines that leave platform empty. The match is
 * deliberately substring-based ("Linux"), never an exact arch string, so
 * every architecture passes.
 */

/** Reports whether the app runs on a Linux host. */
export function isLinuxHost(): boolean {
  return (
    typeof navigator !== 'undefined' &&
    /Linux/i.test(navigator.platform || navigator.userAgent)
  )
}
