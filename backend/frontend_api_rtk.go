package backend

import (
	beRtk "github.com/user/agent/backend/rtk"
)

// CheckRtk checks if the rtk CLI tool is installed and returns its status.
func (f *FrontendAPI) CheckRtk() beRtk.RtkStatus {
	return beRtk.CheckRtk()
}

// InstallRtk downloads and installs the rtk CLI binary.
func (f *FrontendAPI) InstallRtk() error {
	progress := func(status string) {
		f.emitEvent(EventRtkInstallProgress, status)
	}

	installPath, err := beRtk.InstallRtk(progress, nil)
	if err != nil {
		return err
	}

	f.log().Info("rtk installed", "path", installPath)

	// Hot-update the running bash tool with the new rtk path
	if f.app != nil {
		f.app.SetBashRtkPath(installPath)
	}

	// Emit updated status so RtkBanner and settings panel update immediately
	status := beRtk.CheckRtk()
	f.emitEvent(EventRtkStatus, status)

	f.emitEvent(EventRtkInstallProgress, "done")
	return nil
}
