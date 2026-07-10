package tools

import (
	"path/filepath"
	"strings"
)

// wellKnownOSSymlinks lists filesystem symlinks created by the operating
// system itself as mapping or backwards-compatibility infrastructure. These
// traversals are NOT user-created and are not security-relevant, so the
// symlink confirmation gate skips them to avoid spurious prompts (e.g. on
// macOS, where /tmp → /private/tmp, every temp-dir access would otherwise
// force a confirmation dialog).
//
// The map keys are normalized symlink paths; the values are the canonical
// targets (kept for documentation and verification). Matching is
// platform-independent: only the entries that actually exist as symlinks on
// the running OS can match a real Lstat/Readlink result, so including
// cross-platform entries is safe and avoids a runtime.GOOS switch.
//
// This map is the SINGLE source of truth for the well-known list — both the
// sp4rk symlink walker (walkSymlinkComponents) and the core registry gate
// (checkSymlinksAndConfirm) resolve through IsWellKnownOSSymlink /
// IsOSLevelSymlink, so the list must never be duplicated in core.
//
// Sources (researched, not from memory):
//   - macOS: root directories symlinked into /private
//     (etc/tmp/var/cores). See Apple's filesystem layout.
//   - Linux: the /usr merge (bin/sbin/lib{,32,64,x32} → /usr/...), adopted by
//     Fedora, RHEL, Debian, Ubuntu, Arch, openSUSE (systemd
//     "The Case for the /usr Merge"), and the /run tmpfs migration
//     (var/run → /run, var/lock → /run/lock; FHS 3.0).
//   - Windows: backwards-compatibility directory junctions created since
//     Vista ("Documents and Settings" → "Users").
var wellKnownOSSymlinks = map[string]string{
	// ── macOS (Darwin): root → /private ────────────────────────────────
	"/etc":   "/private/etc",
	"/tmp":   "/private/tmp",
	"/var":   "/private/var",
	"/cores": "/private/cores",

	// ── Linux: /usr merge (merged-usr distributions) ──────────────────
	"/bin":    "/usr/bin",
	"/sbin":   "/usr/sbin",
	"/lib":    "/usr/lib",
	"/lib32":  "/usr/lib32",
	"/lib64":  "/usr/lib64",
	"/libx32": "/usr/libx32",

	// ── Linux: /run migration (FHS 3.0 tmpfs runtime data) ────────────
	"/var/run":  "/run",
	"/var/lock": "/run/lock",

	// ── Windows: backwards-compatibility junctions (case-insensitive) ─
	`c:\documents and settings`: `C:\Users`,
}

// IsWellKnownOSSymlink reports whether symlinkPath is a well-known
// operating-system symlink whose traversal is filesystem-mapping
// infrastructure rather than a user-created, security-relevant traversal.
// Paths are normalized with filepath.Clean; Windows drive-letter paths are
// compared case-insensitively.
func IsWellKnownOSSymlink(symlinkPath string) bool {
	if symlinkPath == "" {
		return false
	}
	_, ok := wellKnownOSSymlinks[normalizeOSSymlinkPath(symlinkPath)]
	return ok
}

// IsOSLevelSymlink reports whether a symlink traversal is benign OS-level
// infrastructure that should NOT trigger a confirmation. It returns true when
// symlinkPath is a well-known OS symlink, or when symlinkPath is an ancestor
// of one of the given root directories (workspace / temp dir) — i.e. the root
// itself is reached through the symlink. roots may be empty.
//
// This is the single chokepoint used by both sp4rk's symlink walker and the
// core registry gate, keeping the classification consistent and the
// well-known list unduplicated.
func IsOSLevelSymlink(symlinkPath string, roots ...string) bool {
	if IsWellKnownOSSymlink(symlinkPath) {
		return true
	}
	cleaned := filepath.Clean(symlinkPath)
	// A bare "/" (or drive root) is not a meaningful symlink ancestor.
	if cleaned == string(filepath.Separator) || cleaned == "." {
		return false
	}
	symPrefix := cleaned + string(filepath.Separator)
	for _, root := range roots {
		if root == "" {
			continue
		}
		if strings.HasPrefix(filepath.Clean(root)+string(filepath.Separator), symPrefix) {
			return true
		}
	}
	return false
}

// normalizeOSSymlinkPath cleans the path and lowercases Windows drive-letter
// paths so that junction entries match case-insensitively. looksLikeWindowsDriveLetter
// is the shared helper defined in symlink.go (same package).
func normalizeOSSymlinkPath(p string) string {
	cleaned := filepath.Clean(p)
	if looksLikeWindowsDriveLetter(cleaned) {
		return strings.ToLower(cleaned)
	}
	return cleaned
}
