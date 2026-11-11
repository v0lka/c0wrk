//go:build !darwin

package updater

// stagingLaunchPath is the non-macOS counterpart of the darwin .app wrapper:
// the staged binary IS the launch path. The LaunchServices launch the darwin
// path exists for has no equivalent here — a Setsid-detached child of the
// quitting app survives its exit on Linux and Windows, and the Windows dance
// additionally relies on the startup reap rather than self-deletion.
func stagingLaunchPath(_, updaterPath string) (string, error) {
	return updaterPath, nil
}
