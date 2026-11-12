//go:build windows

package storageprobe

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// driveRemote is GetDriveType's DRIVE_REMOTE: the volume is a network drive.
const driveRemote = 4

// statfsVolume is the windows counterpart of the darwin statfs reader: it
// classifies path's drive (GetDriveTypeW on the volume root — DRIVE_REMOTE
// means a network drive) and checks the path itself for cloud placeholder
// management (FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS, set on OneDrive Files
// On-Demand trees: reading an un-downloaded file stalls until it is fetched).
// Both reads are best-effort — a failed attribute read degrades to the
// drive-type verdict alone rather than failing the whole probe.
func statfsVolume(path string) (volumeClass, error) {
	vc := volumeClass{}

	rootPtr, err := windows.UTF16PtrFromString(volumeRoot(path))
	if err != nil {
		return vc, err
	}
	if t := windows.GetDriveType(rootPtr); driveTypeIsRemote(t) {
		vc.Network = true
		vc.NetworkDetail = "GetDriveTypeW reports DRIVE_REMOTE (network drive)"
	}

	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return vc, err
	}
	attrs, err := windows.GetFileAttributes(pathPtr)
	if err != nil {
		// Best-effort: keep whatever the drive type established.
		return vc, nil
	}
	if attrsHaveRecall(attrs) {
		vc.Placeholder = true
		vc.PlaceholderDetail = "FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS set (cloud placeholder files, e.g. OneDrive Files On-Demand)"
	}
	return vc, nil
}

// volumeRoot returns the drive root GetDriveTypeW expects: "C:\" for a
// lettered drive, the "\\server\share" prefix of a UNC path verbatim, or the
// path itself when it carries no volume prefix.
func volumeRoot(path string) string {
	vol := filepath.VolumeName(path)
	if vol == "" {
		return path
	}
	if len(vol) == 2 && vol[1] == ':' {
		return vol + "\\"
	}
	return vol
}

// driveTypeIsRemote reports whether a GetDriveType verdict means a network
// drive (pure decode helper — unit-tested under GOOS=windows).
func driveTypeIsRemote(t uint32) bool {
	return t == driveRemote
}

// attrsHaveRecall reports whether file attributes carry the cloud placeholder
// marker (pure decode helper — unit-tested under GOOS=windows).
func attrsHaveRecall(attrs uint32) bool {
	return attrs&windows.FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS != 0
}

// configDirFindings needs no darwin-style sync-root containment on windows:
// a synced data directory already announces itself through the shared
// placeholder check (and a network one through the drive type), so the
// platform hook adds nothing.
func configDirFindings(path string) []Finding {
	return nil
}
