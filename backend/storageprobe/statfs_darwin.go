//go:build darwin

package storageprobe

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/v0lka/sp4rk/pathutil"
)

// mntLocal mirrors Darwin's MNT_LOCAL (sys/mount.h): "filesystem is stored
// locally". Go's syscall package does not export the MNT_* flag set on
// darwin, so the stable ABI value is pinned here (verified against the macOS
// SDK: #define MNT_LOCAL 0x00001000). Network mounts (NFS, SMB) and other
// non-local filesystems lack the flag; local APFS/HFS volumes carry it.
const mntLocal = 0x00001000

// statfsVolume reads the mount class of path's volume from statfs flags:
// flags lacking MNT_LOCAL mean a network (or otherwise non-local) mount.
func statfsVolume(path string) (volumeClass, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return volumeClass{}, err
	}
	if st.Flags&mntLocal == 0 {
		return volumeClass{
			Network:       true,
			NetworkDetail: "statfs flags lack MNT_LOCAL (network or otherwise non-local mount)",
		}, nil
	}
	return volumeClass{}, nil
}

// userHomeDir is a test seam over os.UserHomeDir so the sync-root containment
// can run against a fake home without touching the real one.
var userHomeDir = os.UserHomeDir

// darwinSyncRoots returns the cloud-provider containment roots inside home:
// iCloud Drive's backing store ("Library/Mobile Documents") and the
// FileProvider mount point every third-party sync provider (OneDrive,
// Dropbox, …) presents as "Library/CloudStorage". A config directory that
// resolves under either lives behind a sync engine.
func darwinSyncRoots(home string) []string {
	return []string{
		filepath.Join(home, "Library", "Mobile Documents"),
		filepath.Join(home, "Library", "CloudStorage"),
	}
}

// resolveRealPath resolves symlinks (a ~/.c0wrk symlinked into a synced root
// must still be caught) and falls back to the cleaned path when the target
// does not exist or the walk fails.
func resolveRealPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// configDirFindings is the darwin config-dir check: does the directory
// resolve under a FileProvider/iCloud sync root? Containment uses
// pathutil.IsWithinPathFold (the centralized containment primitive,
// case-insensitive like the default APFS/NTFS filesystems).
func configDirFindings(path string) []Finding {
	home, err := userHomeDir()
	if err != nil || home == "" {
		return nil
	}
	resolved := resolveRealPath(path)
	for _, root := range darwinSyncRoots(home) {
		within, err := pathutil.IsWithinPathFold(resolveRealPath(root), resolved)
		if err == nil && within {
			return []Finding{{
				Kind:   KindSyncedConfigDir,
				Detail: fmt.Sprintf("config dir resolves under %s (FileProvider/iCloud sync)", filepath.Base(root)),
			}}
		}
	}
	return nil
}
