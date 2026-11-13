//go:build !darwin && !windows

package storageprobe

// statfsVolume is the neutral fallback for platforms without a dedicated
// reader (linux builds): reading a mount class reliably means shelling out to
// df/stat -f, which is heavier than an advisory probe deserves and can hang
// on exactly the wedged mounts it looks for. Report nothing rather than guess.
func statfsVolume(path string) (volumeClass, error) {
	return volumeClass{}, nil
}

// configDirFindings has no platform-specific config-dir check outside darwin.
func configDirFindings(path string) []Finding {
	return nil
}
